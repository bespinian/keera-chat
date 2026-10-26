package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/bespinian/keera-chat/internal/gateway"
)

// pingEvery keeps a quiet stream alive through proxies that close idle
// connections.
const pingEvery = 15 * time.Second

type replyIn struct {
	Model string `json:"model"`
	// ModelName is the display name, for errors.
	ModelName string `json:"model_name"`
	KeyID     string `json:"key_id"`
	// Instructions are sent as the system prompt.
	Instructions string `json:"instructions"`
	// Messages is the whole conversation, ending with the message to answer.
	Messages []turn `json:"messages"`
}

// reply streams the model's answer as server-sent events: "delta" for each
// piece, then "done" with the whole reply. A gateway refusal, such as a spent
// budget, is a "done" with an error. Chat stores nothing.
func (s *Server) reply(w http.ResponseWriter, r *http.Request, c *caller) {
	var in replyIn
	if !s.readJSON(w, r, &in) {
		return
	}
	if msg := check(in); msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	body, err := json.Marshal(map[string]any{
		"model":    in.Model,
		"messages": wire(strings.TrimSpace(in.Instructions), in.Messages),
		"stream":   true,
		// Token usage, so the page can show how full the context is.
		"stream_options": map[string]bool{"include_usage": true},
	})
	if err != nil {
		s.failed(w, r, err)
		return
	}

	ctx := r.Context()
	if in.ModelName == "" {
		in.ModelName = in.Model
	}
	reply := Message{Role: "assistant", Model: in.Model, CreatedAt: time.Now().UTC()}
	res, err := s.gw.Chat(ctx, c.token, in.KeyID, body)
	if gateway.IsUnauthorized(err) {
		s.gatewayFailed(w, r, err)
		return
	}

	out := newEvents(w)
	switch {
	case err != nil && ctx.Err() != nil:
		reply.Stopped = true
	case err != nil:
		reply.Error = err.Error()
		if e, ok := errors.AsType[*gateway.Error](err); ok {
			reply.Error, reply.ErrorDetail = e.Naming(in.ModelName).Message, e.Detail
		}
	default:
		s.stream(ctx, res, out, &reply, in.ModelName)
	}
	reply.Content, reply.Reasoning = finish(reply.Content, reply.Reasoning)
	out.send("done", reply)
}

// check returns why a conversation cannot be sent, or "".
func check(in replyIn) string {
	if in.Model == "" || in.KeyID == "" {
		return "choose a model to send to"
	}
	var last turn
	if n := len(in.Messages); n > 0 {
		last = in.Messages[n-1]
	}
	if last.Role != "user" {
		return "there is no message to answer"
	}
	if strings.TrimSpace(last.Content) == "" && len(last.Images) == 0 && len(last.Files) == 0 {
		return "write a message first"
	}
	for _, m := range in.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return "the conversation could not be read; reload the page"
		}
		if len(m.Images) > maxImages {
			return fmt.Sprintf("attach at most %d images to one message", maxImages)
		}
		for _, img := range m.Images {
			if !validImage(img) {
				return "images must be PNG, JPEG, GIF or WebP"
			}
		}
		if len(m.Files) > maxFiles {
			return fmt.Sprintf("attach at most %d files to one message", maxFiles)
		}
		for _, f := range m.Files {
			if len(f.Text) > maxFileBytes {
				return fmt.Sprintf("%s is larger than %d KB", f.Name, maxFileBytes>>10)
			}
		}
	}
	return ""
}

func finish(content, reasoning string) (string, string) {
	answer, thought := splitThink(content)
	if thought != "" {
		if reasoning != "" {
			reasoning += "\n\n"
		}
		reasoning += thought
	}
	return answer, reasoning
}

// ------------------------------------------------------------- streaming

// chunk is one piece of an OpenAI chat completions stream, or a whole
// completion when the backend did not stream.
type chunk struct {
	Choices []struct {
		Delta   delta  `json:"delta"`
		Message *delta `json:"message"`
		Finish  string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string       `json:"message"`
		Code    gateway.Code `json:"code"`
	} `json:"error"`
	// Usage comes last, in a chunk with no choices.
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type delta struct {
	Content string `json:"content"`
	// Newer vLLM uses "reasoning"; older vLLM and llama.cpp use
	// "reasoning_content".
	Reasoning        string `json:"reasoning"`
	ReasoningContent string `json:"reasoning_content"`
}

// stream forwards the answer to the page and gathers it into reply. Stopping
// cancels ctx, which also ends the request to the gateway.
func (s *Server) stream(ctx context.Context, res *http.Response, out *events, reply *Message, model string) {
	defer func() { _ = res.Body.Close() }()

	var text, thought strings.Builder
	apply := func(c chunk) {
		// An error in the stream comes from the model server.
		if c.Error != nil && c.Error.Message != "" {
			e := gateway.NewError(0, c.Error.Code, c.Error.Message).Naming(model)
			reply.Error, reply.ErrorDetail = e.Message, e.Detail
		}
		if c.Usage != nil {
			reply.Tokens = c.Usage.PromptTokens + c.Usage.CompletionTokens
		}
		if len(c.Choices) == 0 {
			return
		}
		ch := c.Choices[0]
		d := ch.Delta
		if ch.Message != nil {
			d = *ch.Message
		}
		more := d.Reasoning + d.ReasoningContent
		if d.Content != "" || more != "" {
			text.WriteString(d.Content)
			thought.WriteString(more)
			out.send("delta", map[string]string{"content": d.Content, "reasoning": more})
		}
		if ch.Finish != "" {
			reply.Finish = ch.Finish
		}
	}
	defer func() { reply.Content, reply.Reasoning = text.String(), thought.String() }()

	// A backend that ignored stream:true answers with one JSON completion.
	if mt, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type")); mt == "application/json" {
		var c chunk
		if err := json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(&c); err != nil {
			reply.Error = "the reply could not be read; try again"
			return
		}
		apply(c)
		return
	}

	chunks := make(chan chunk)
	var readErr error
	go func() {
		defer close(chunks)
		readErr = readEvents(res.Body, func(c chunk) bool {
			select {
			case chunks <- c:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()

	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case c, ok := <-chunks:
			if !ok {
				// readErr is set before the channel closes.
				switch {
				case ctx.Err() != nil:
					reply.Stopped = true
				case readErr != nil && reply.Error == "":
					s.log.Warn("a reply was cut off", "error", readErr)
					reply.Error = "the reply was cut off before it finished; try again"
				}
				return
			}
			apply(c)
		case <-ping.C:
			out.comment("ping")
		}
	}
}

// readEvents calls each for every JSON event until [DONE], the end, or each
// returns false.
func readEvents(body io.Reader, each func(chunk) bool) error {
	br := bufio.NewReaderSize(body, 64<<10)
	for {
		line, err := br.ReadString('\n')
		if data, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data:"); ok {
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				return nil
			}
			var c chunk
			// Skip chunks that are not JSON.
			if data != "" && json.Unmarshal([]byte(data), &c) == nil && !each(c) {
				return nil
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// events writes server-sent events, each flushed at once.
type events struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func newEvents(w http.ResponseWriter) *events {
	rc := http.NewResponseController(w)
	// A reply can take minutes, longer than the listener's write timeout.
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	// Stop nginx from buffering the stream.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	return &events{w: w, rc: rc}
}

// send ignores write errors. If the page is gone, its context ends the reply.
func (e *events) send(event string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, data)
	_ = e.rc.Flush()
}

func (e *events) comment(text string) {
	_, _ = fmt.Fprintf(e.w, ": %s\n\n", text)
	_ = e.rc.Flush()
}

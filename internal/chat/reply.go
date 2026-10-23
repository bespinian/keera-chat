package chat

import (
	"bufio"
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
// connections, while a model thinks before its first token.
const pingEvery = 15 * time.Second

type replyIn struct {
	Model string `json:"model"`
	// ModelName is the model as the page names it, for errors.
	ModelName string `json:"model_name"`
	KeyID     string `json:"key_id"`
	// Instructions are the person's custom instructions, sent ahead of the
	// conversation like a system prompt. The gateway may add its own before.
	Instructions string `json:"instructions"`
	// Messages is the whole conversation, ending with the person's message
	// to answer.
	Messages []turn `json:"messages"`
}

// reply has the model answer a conversation through the gateway and streams
// the answer back. Chat keeps nothing: the page sends the whole conversation
// with each message, and keeps the reply.
//
// The page reads server-sent events: "delta" for each piece of the answer,
// then "done" with the whole reply. A refusal by the gateway, such as a spent
// budget, arrives as a "done" whose reply has an error, and the page keeps it
// with the conversation like any reply.
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
		// The gateway passes the model server's count on only when asked,
		// and the page shows from it how full the context is.
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
		s.stream(r, res, out, &reply, in.ModelName)
	}
	reply.Content, reply.Reasoning = finish(reply.Content, reply.Reasoning)
	out.send("done", reply)
}

// check says why a conversation cannot be sent, or is empty when it can.
func check(in replyIn) string {
	if in.Model == "" || in.KeyID == "" {
		return "choose a model to send to"
	}
	if len(in.Messages) == 0 || in.Messages[len(in.Messages)-1].Role != "user" {
		return "there is no message to answer"
	}
	if last := in.Messages[len(in.Messages)-1]; strings.TrimSpace(last.Content) == "" && len(last.Images) == 0 {
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
	}
	return ""
}

// finish takes inline thinking out of a finished reply.
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
	// Usage comes last in a stream, in a chunk of its own with no choices.
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type delta struct {
	Content string `json:"content"`
	// Newer vLLM servers name it "reasoning", older ones and llama.cpp
	// "reasoning_content".
	Reasoning        string `json:"reasoning"`
	ReasoningContent string `json:"reasoning_content"`
}

// stream copies the model's answer to the page as it arrives and gathers it
// into reply. When the person stops the reply, the request's context ends,
// which also ends the request to the gateway, and so the model's work.
func (s *Server) stream(r *http.Request, res *http.Response, out *events, reply *Message, model string) {
	defer func() { _ = res.Body.Close() }()
	ctx := r.Context()

	var text, thought strings.Builder
	apply := func(c chunk) {
		// The gateway passes a model's stream on as it came, so an error
		// in it is the model server's.
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
				// readErr is set before the channel closes, so reading it is
				// safe here.
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

// readEvents calls each for every JSON event of a server-sent event stream
// until the stream says [DONE], ends, or each returns false.
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
			// A chunk that is not JSON is skipped; the stream carries on.
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

// events writes server-sent events to the page, each flushed at once.
type events struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func newEvents(w http.ResponseWriter) *events {
	rc := http.NewResponseController(w)
	// The listener's write timeout is for small answers. A reply can take
	// minutes, so it is lifted for this response only.
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	// nginx buffers responses unless told not to, which would hold the
	// tokens back until the reply is complete.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	return &events{w: w, rc: rc}
}

// send writes one event. A page that has gone away is not an error here: its
// request's context ends, and with it the reply.
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

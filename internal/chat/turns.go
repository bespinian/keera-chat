package chat

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// turn is one message of a conversation, as the page sends it.
type turn struct {
	Role    string   `json:"role"` // "user" or "assistant"
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
	// Files are text files the person attached. A PDF arrives as images of
	// its pages, since no model server here reads PDFs.
	Files []textFile `json:"files,omitempty"`
	// Failed marks a reply that did not come, such as one the gateway
	// refused. It is left out of what is sent next.
	Failed bool `json:"failed,omitempty"`
}

type textFile struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Message is a reply, as the page keeps it with the conversation.
type Message struct {
	Role    string `json:"role"` // always "assistant"
	Content string `json:"content"`
	// Reasoning is what a model thought before answering, when it says.
	Reasoning string `json:"reasoning,omitempty"`
	// Model is the alias that answered.
	Model string `json:"model,omitempty"`
	// Error is why the reply did not come, worded for the person.
	Error string `json:"error,omitempty"`
	// ErrorDetail is the text Error stands in for, when it was too
	// technical to show as it was. The page shows it on request.
	ErrorDetail string `json:"error_detail,omitempty"`
	// Stopped marks a reply the person stopped. What arrived is kept.
	Stopped bool   `json:"stopped,omitempty"`
	Finish  string `json:"finish,omitempty"`
	// Tokens is how much of the model's context the conversation used once
	// this reply was written, as the model server counted it. A thinking
	// model's reasoning is counted too, though it is not sent back, so it is
	// a little more than the next message starts from.
	Tokens int `json:"tokens,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// wireMessage is one message of an OpenAI chat completions request.
type wireMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type wirePart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

// wire is what goes to the model for a conversation whose last message is
// the turn being answered.
//
// A turn that failed is left out, the person's message with it: it never
// reached the model, and some chat templates refuse two user turns in a row.
// A reply that was stopped keeps what arrived.
func wire(instructions string, msgs []turn) []wireMessage {
	out := []wireMessage{}
	if instructions != "" {
		out = append(out, wireMessage{Role: "system", Content: instructions})
	}
	for i, m := range msgs {
		switch m.Role {
		case "user":
			if i < len(msgs)-1 && !answered(msgs[i+1]) {
				continue
			}
			out = append(out, wireMessage{Role: "user", Content: content(m)})
		case "assistant":
			if answered(m) {
				out = append(out, wireMessage{Role: "assistant", Content: m.Content})
			}
		}
	}
	return out
}

// answered reports whether m is a reply the model actually gave.
func answered(m turn) bool {
	return m.Role == "assistant" && !m.Failed && strings.TrimSpace(m.Content) != ""
}

// content uses the parts form only for a turn with images, which only models
// that read images accept. Files go ahead of the message as text, so models
// that read only text read them too.
func content(m turn) any {
	text := withFiles(m.Content, m.Files)
	if len(m.Images) == 0 {
		return text
	}
	parts := make([]wirePart, 0, len(m.Images)+1)
	if text != "" {
		parts = append(parts, wirePart{Type: "text", Text: text})
	}
	for _, url := range m.Images {
		parts = append(parts, wirePart{Type: "image_url", ImageURL: &imageURL{URL: url}})
	}
	return parts
}

func withFiles(text string, files []textFile) string {
	if len(files) == 0 {
		return text
	}
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "<file name=%q>\n%s\n</file>\n\n", f.Name, strings.TrimRight(f.Text, "\n"))
	}
	b.WriteString(text)
	return strings.TrimRight(b.String(), "\n")
}

// splitThink takes the thinking out of a reply that writes it inline as
// <think>…</think>, as some open models do, rather than in a reasoning
// field. Some chat templates open the tag in the prompt, so the reply only
// closes it.
func splitThink(text string) (answer, thought string) {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	opened := strings.HasPrefix(trimmed, "<think>")
	end := strings.Index(text, "</think>")
	if !opened && end < 0 {
		return text, ""
	}
	from := 0
	if opened {
		from = len(text) - len(trimmed) + len("<think>")
	}
	if end < 0 {
		return "", strings.TrimSpace(text[from:])
	}
	return strings.TrimLeftFunc(text[end+len("</think>"):], unicode.IsSpace),
		strings.TrimSpace(text[from:end])
}

// imagePrefixes are the formats every API that reads images accepts, as the
// data: URLs the page sends. chat.js has the same list and maxImages, and
// store.js checks imported files against both.
var imagePrefixes = []string{
	"data:image/png;base64,", "data:image/jpeg;base64,",
	"data:image/gif;base64,", "data:image/webp;base64,",
}

const maxImages = 10

// maxFiles and maxFileBytes bound the text files of one message. chat.js and
// store.js have the same. A file this size is already about 130k tokens.
const (
	maxFiles     = 10
	maxFileBytes = 512 << 10
)

func validImage(url string) bool {
	for _, p := range imagePrefixes {
		if strings.HasPrefix(url, p) && len(url) > len(p) {
			return true
		}
	}
	return false
}

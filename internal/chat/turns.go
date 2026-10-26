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
	// Files are attached text files. PDFs arrive as page images instead.
	Files []textFile `json:"files,omitempty"`
	// Failed marks a reply that did not come. It is not sent again.
	Failed bool `json:"failed,omitempty"`
}

type textFile struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Message is a reply, as the page stores it.
type Message struct {
	Role      string `json:"role"` // always "assistant"
	Content   string `json:"content"`
	Reasoning string `json:"reasoning,omitempty"`
	// Model is the alias that answered.
	Model string `json:"model,omitempty"`
	Error string `json:"error,omitempty"`
	// ErrorDetail is the technical text behind Error.
	ErrorDetail string `json:"error_detail,omitempty"`
	// Stopped marks a reply the person stopped. What arrived is kept.
	Stopped bool   `json:"stopped,omitempty"`
	Finish  string `json:"finish,omitempty"`
	// Tokens is the context used after this reply. It includes reasoning,
	// which is not sent back, so it overcounts slightly.
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

// wire builds the messages sent to the model. A failed turn is left out with
// its question: some chat templates refuse two user turns in a row.
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

func answered(m turn) bool {
	return m.Role == "assistant" && !m.Failed && strings.TrimSpace(m.Content) != ""
}

// content uses the parts form only with images, since text-only models
// refuse it. Files go before the message as text.
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

// splitThink takes inline <think>…</think> out of a reply. Some chat
// templates open the tag in the prompt, so the reply only closes it.
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

// imagePrefixes are the image formats every API accepts. store.js has the
// same list and maxImages.
var imagePrefixes = []string{
	"data:image/png;base64,", "data:image/jpeg;base64,",
	"data:image/gif;base64,", "data:image/webp;base64,",
}

const maxImages = 10

// maxFiles and maxFileBytes match store.js. 512 KB is about 130k tokens.
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

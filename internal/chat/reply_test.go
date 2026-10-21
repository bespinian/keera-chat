package chat

import (
	"strings"
	"testing"
)

func TestReadEvents(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		": a comment\n\n" +
		"data: not json\n\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"r\"},\"finish_reason\":\"stop\"}]}\r\n\r\n" +
		"data: [DONE]\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"after done\"}}]}\n\n"
	var got []chunk
	if err := readEvents(strings.NewReader(stream), func(c chunk) bool {
		got = append(got, c)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Choices[0].Delta.Content != "Hi" ||
		got[1].Choices[0].Delta.ReasoningContent != "r" || got[1].Choices[0].Finish != "stop" {
		t.Fatalf("chunks = %+v", got)
	}
}

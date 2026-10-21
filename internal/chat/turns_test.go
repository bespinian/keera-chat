package chat

import (
	"strings"
	"testing"
)

func TestWire(t *testing.T) {
	user := func(s string) turn { return turn{Role: "user", Content: s} }
	ok := func(s string) turn { return turn{Role: "assistant", Content: s} }
	failed := turn{Role: "assistant", Content: "half a reply", Failed: true}
	empty := turn{Role: "assistant"}

	for name, tc := range map[string]struct {
		msgs []turn
		want string
	}{
		"first":              {[]turn{user("a")}, "user:a"},
		"answered":           {[]turn{user("a"), ok("b"), user("c")}, "user:a assistant:b user:c"},
		"refused turn":       {[]turn{user("a"), failed, user("c")}, "user:c"},
		"stopped with none":  {[]turn{user("a"), empty, user("c")}, "user:c"},
		"unanswered message": {[]turn{user("a"), user("c")}, "user:c"},
	} {
		var got []string
		for _, m := range wire("", tc.msgs) {
			got = append(got, m.Role+":"+m.Content.(string))
		}
		if strings.Join(got, " ") != tc.want {
			t.Errorf("%s: %v, want %s", name, got, tc.want)
		}
	}

	if w := wire("Be brief.", []turn{user("a")}); len(w) != 2 || w[0].Role != "system" {
		t.Errorf("instructions: %+v", w)
	}
}

func TestSplitThink(t *testing.T) {
	for in, want := range map[string][2]string{
		"plain answer":                     {"plain answer", ""},
		"<think>hmm</think>\n\nThe answer": {"The answer", "hmm"},
		"  <think>still thinking":          {"", "still thinking"},
		// The template opened the tag in the prompt.
		"hmm</think>The answer": {"The answer", "hmm"},
		"x </think> <think>":    {"<think>", "x"},
	} {
		answer, thought := splitThink(in)
		if answer != want[0] || thought != want[1] {
			t.Errorf("splitThink(%q) = %q, %q; want %q, %q", in, answer, thought, want[0], want[1])
		}
	}
}

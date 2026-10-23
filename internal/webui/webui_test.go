package webui

import "testing"

func TestAcceptsGzip(t *testing.T) {
	for name, c := range map[string]struct {
		header string
		want   bool
	}{
		"none":          {"", false},
		"gzip":          {"gzip", true},
		"among others":  {"br, gzip, deflate", true},
		"upper case":    {"GZIP", true},
		"weighted":      {"gzip;q=0.5", true},
		"refused":       {"gzip;q=0", false},
		"refused, long": {"gzip ; q=0.000", false},
		"only others":   {"br, deflate", false},
		"bad weight":    {"gzip;q=x", true},
	} {
		if got := acceptsGzip(c.header); got != c.want {
			t.Errorf("%s: acceptsGzip(%q) = %v, want %v", name, c.header, got, c.want)
		}
	}
}

package chat

import "testing"

func TestChallenge(t *testing.T) {
	// RFC 7636, appendix B.
	if got := challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge = %s", got)
	}
	if v := randomToken(); len(v) != 43 {
		t.Fatalf("a verifier is %d characters, the gateway expects 43", len(v))
	}
}

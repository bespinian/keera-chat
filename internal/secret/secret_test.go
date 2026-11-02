package secret

import (
	"bytes"
	"testing"
)

const key = "a-test-secret-key-long-enough"

func TestSealOpen(t *testing.T) {
	b := mustNew(t, key)
	sealed := b.Seal("cookie:session", []byte("token"))
	got, err := b.Open("cookie:session", sealed)
	if err != nil || string(got) != "token" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	// A random nonce makes each seal different.
	if bytes.Equal(sealed, b.Seal("cookie:session", []byte("token"))) {
		t.Error("sealing twice gave the same bytes")
	}
}

func TestOpenRefuses(t *testing.T) {
	b := mustNew(t, key)
	sealed := b.Seal("cookie:session", []byte("token"))

	// One cookie's value cannot pass as another's.
	if _, err := b.Open("cookie:login", sealed); err == nil {
		t.Error("opened under another name")
	}
	if _, err := mustNew(t, key+"-other").Open("cookie:session", sealed); err == nil {
		t.Error("opened with another key")
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	if _, err := b.Open("cookie:session", tampered); err == nil {
		t.Error("opened tampered bytes")
	}
	for _, short := range [][]byte{nil, sealed[:4]} {
		if _, err := b.Open("cookie:session", short); err == nil {
			t.Errorf("opened %d bytes", len(short))
		}
	}
}

func TestShortKeyRefused(t *testing.T) {
	if _, err := New("too-short"); err == nil {
		t.Error("accepted a key under 16 characters")
	}
}

func mustNew(t *testing.T, key string) *Box {
	t.Helper()
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Package secret encrypts chat's cookies: the session, which holds the
// gateway token, and the PKCE verifier. The key lives only in the
// environment, so stolen cookies are useless.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"errors"
)

type Box struct {
	aead cipher.AEAD
}

func New(key string) (*Box, error) {
	// config.Load checks this too. This guards other callers.
	if len(key) < 16 {
		return nil, errors.New("the secret key must be at least 16 characters; " +
			"generate one with: openssl rand -hex 32")
	}
	// Domain separation: reusing the gateway's key still gives a different key.
	sum := sha256.Sum256([]byte("keera-chat-v1|" + key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext bound to name, so it cannot be opened under
// another name.
func (b *Box) Seal(name string, plaintext []byte) []byte {
	return b.aead.Seal(nil, nil, plaintext, []byte(name))
}

// Open decrypts what Seal produced for the same name.
func (b *Box) Open(name string, sealed []byte) ([]byte, error) {
	out, err := b.aead.Open(nil, nil, sealed, []byte(name))
	if err != nil {
		return nil, errors.New("secret: cannot decrypt with the configured key")
	}
	return out, nil
}

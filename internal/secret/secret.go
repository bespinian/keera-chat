// Package secret seals the session cookie, which carries a person's gateway
// sign-in. The key lives only in the process environment, so a cookie jar
// stolen from a backup holds ciphertext.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

type Box struct {
	aead cipher.AEAD
}

func New(key string) (*Box, error) {
	// config.Load refuses a shorter key first, naming the setting. This
	// guards any other caller.
	if len(key) < 16 {
		return nil, errors.New("the secret key must be at least 16 characters; " +
			"generate one with: openssl rand -hex 32")
	}
	// Domain separation: the same value set as the gateway's key still yields
	// a different key here.
	sum := sha256.Sum256([]byte("keera-chat-v1|" + key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext for the place name names. The name is
// authenticated, so a ciphertext moved to another place fails to open.
func (b *Box) Seal(name string, plaintext []byte) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	_, _ = rand.Read(nonce) // never fails; see crypto/rand.Read
	return b.aead.Seal(nonce, nonce, plaintext, []byte(name))
}

// Open decrypts what Seal produced for the same name.
func (b *Box) Open(name string, sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("secret: ciphertext is truncated")
	}
	out, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(name))
	if err != nil {
		return nil, errors.New("secret: cannot decrypt with the configured key")
	}
	return out, nil
}

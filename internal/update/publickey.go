package update

import (
	"crypto/ed25519"
	_ "embed"
	"errors"
	"strings"
)

// ErrNoPublicKey means no signing key has been committed yet.
var ErrNoPublicKey = errors.New("update: no public key embedded")

//go:embed publickey.txt
var publicKeyText string

// PublicKey returns the embedded update-signing public key.
func PublicKey() (ed25519.PublicKey, error) {
	return ParsePublicKeyText(publicKeyText)
}

// ParsePublicKeyText extracts the key from publickey.txt-style text: blank
// lines and lines starting with # are ignored; no key yields ErrNoPublicKey.
func ParsePublicKeyText(text string) (ed25519.PublicKey, error) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return ParsePublicKey(line)
	}
	return nil, ErrNoPublicKey
}

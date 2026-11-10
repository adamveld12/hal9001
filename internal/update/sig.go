package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Verification errors; VerifyFile wraps exactly one of them.
var (
	ErrSizeMismatch      = errors.New("update: size mismatch")
	ErrSHA256Mismatch    = errors.New("update: sha256 mismatch")
	ErrSignatureMismatch = errors.New("update: signature mismatch")
)

// ParsePublicKey decodes a base64 (std, padded) 32-byte Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("update: public key: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("update: public key is %d bytes, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// ParsePrivateKey decodes a base64 (std, padded) 32-byte Ed25519 seed.
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("update: private key: %w", err)
	}
	if len(b) != ed25519.SeedSize {
		return nil, fmt.Errorf("update: private key seed is %d bytes, want %d", len(b), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(b), nil
}

// Sign returns the base64 signature of data.
func Sign(priv ed25519.PrivateKey, data []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
}

// VerifyFile streams the file at path and checks it against m: size, sha256, then signature.
func VerifyFile(path string, m Manifest, pub ed25519.PublicKey) error {
	f, err := os.Open(path) //nolint:gosec // caller-chosen download path
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, m.Size+1))
	if err != nil {
		return err
	}
	if int64(len(data)) != m.Size {
		return fmt.Errorf("%w: manifest %d, file larger or smaller (%d read)", ErrSizeMismatch, m.Size, len(data))
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), m.SHA256) {
		return ErrSHA256Mismatch
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || !ed25519.Verify(pub, data, sig) {
		return ErrSignatureMismatch
	}
	return nil
}

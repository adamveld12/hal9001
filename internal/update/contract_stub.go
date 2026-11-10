package update

// TEMPORARY STUB of the manifest/signature contract owned by branch feat/update-ci (manifest.go, sig.go,
// publickey.go, publickey.txt). Same names and signatures as that branch. Delete this file when merging feat/update-ci.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Manifest describes one published app build on a channel.
type Manifest struct {
	Schema      int       `json:"schema"`
	Channel     string    `json:"channel"`
	Version     string    `json:"version"`
	Build       int       `json:"build"`
	Commit      string    `json:"commit"`
	PublishedAt time.Time `json:"published_at"`
	MinMacOS    string    `json:"min_macos"`
	URL         string    `json:"url"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	Signature   string    `json:"signature"`
	NotesURL    string    `json:"notes_url"`
	Notes       string    `json:"notes"`
}

var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// ParseManifest decodes and validates a manifest.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("update: manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Validate checks the manifest invariants.
func (m Manifest) Validate() error {
	switch {
	case m.Schema != 1:
		return fmt.Errorf("update: manifest schema %d unsupported", m.Schema)
	case m.Build <= 0:
		return fmt.Errorf("update: manifest build %d must be positive", m.Build)
	case m.Channel != "stable" && m.Channel != "beta":
		return fmt.Errorf("update: manifest channel %q unknown", m.Channel)
	case !sha256Hex.MatchString(m.SHA256):
		return fmt.Errorf("update: manifest sha256 must be 64 hex characters")
	case m.Signature == "":
		return fmt.Errorf("update: manifest signature is empty")
	}
	u, err := url.Parse(m.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("update: manifest url %q must be https", m.URL)
	}
	return nil
}

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

// ErrNoPublicKey means no signing key has been committed yet.
var ErrNoPublicKey = errors.New("update: no public key embedded")

// PublicKey returns the embedded update-signing public key (none in this stub).
func PublicKey() (ed25519.PublicKey, error) { return nil, ErrNoPublicKey }

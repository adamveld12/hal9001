package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (string, Manifest, ed25519.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	data := []byte("zip bytes")
	p := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	m := Manifest{Schema: 1, Channel: "stable", Version: "0.1.1", Build: 1, URL: "https://x.test/a.zip",
		Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Signature: Sign(priv, data)}
	return p, m, pub
}

func TestVerifyFile(t *testing.T) {
	p, m, pub := fixture(t)
	if err := VerifyFile(p, m, pub); err != nil {
		t.Fatal(err)
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	bad := func(mut func(*Manifest), want error, key ed25519.PublicKey) {
		t.Helper()
		mm := m
		mut(&mm)
		if err := VerifyFile(p, mm, key); !errors.Is(err, want) {
			t.Fatalf("got %v want %v", err, want)
		}
	}
	bad(func(m *Manifest) { m.Size++ }, ErrSizeMismatch, pub)
	bad(func(m *Manifest) { m.SHA256 = hex.EncodeToString(make([]byte, 32)) }, ErrSHA256Mismatch, pub)
	bad(func(m *Manifest) { m.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64)) }, ErrSignatureMismatch, pub)
	bad(func(*Manifest) {}, ErrSignatureMismatch, otherPub)
}

func TestKeyRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	p2, err := ParsePrivateKey(base64.StdEncoding.EncodeToString(priv.Seed()))
	if err != nil || !p2.Equal(priv) {
		t.Fatal("private key round trip", err)
	}
	k, err := ParsePublicKey(base64.StdEncoding.EncodeToString(pub))
	if err != nil || !k.Equal(pub) {
		t.Fatal("public key round trip", err)
	}
}

func TestParseManifestValidation(t *testing.T) {
	_, m, _ := fixture(t)
	good := func() Manifest { return m }
	cases := map[string]func(*Manifest){
		"schema":    func(m *Manifest) { m.Schema = 2 },
		"build":     func(m *Manifest) { m.Build = 0 },
		"http":      func(m *Manifest) { m.URL = "http://x.test/a.zip" },
		"sha":       func(m *Manifest) { m.SHA256 = "abc" },
		"signature": func(m *Manifest) { m.Signature = "" },
		"channel":   func(m *Manifest) { m.Channel = "nightly" },
	}
	if err := good().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range cases {
		mm := good()
		mut(&mm)
		if err := mm.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := ParseManifest([]byte("{")); err == nil {
		t.Error("bad json accepted")
	}
}

func TestPublicKeyText(t *testing.T) {
	if _, err := ParsePublicKeyText("# none yet\n\n"); !errors.Is(err, ErrNoPublicKey) {
		t.Fatal(err)
	}
}

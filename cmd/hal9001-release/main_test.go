package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/update"
)

func TestKeygenRefusals(t *testing.T) {
	d := t.TempDir()
	priv, pub := filepath.Join(d, "priv.txt"), filepath.Join(d, "pub.txt")
	var out bytes.Buffer
	if err := run([]string{"keygen", "--private-out", priv, "--public-out", pub}, &out); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(priv); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if err := run([]string{"keygen", "--private-out", priv, "--public-out", pub}, &out); err == nil {
		t.Fatal("overwrote private key")
	}
	if err := run([]string{"keygen", "--private-out", priv + "2", "--public-out", pub}, &out); err == nil {
		t.Fatal("replaced public key without --force")
	}
	if err := run([]string{"keygen", "--private-out", priv + "2", "--public-out", pub, "--force"}, &out); err != nil {
		t.Fatal(err)
	}
}

func TestManifestCommand(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv("K", base64.StdEncoding.EncodeToString(priv.Seed()))
	d := t.TempDir()
	zip, outp := filepath.Join(d, "a.zip"), filepath.Join(d, "m.json")
	if err := os.WriteFile(zip, []byte("zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"manifest", "--channel", "stable", "--version", "0.1.1", "--build", "1", "--commit", "abc",
		"--zip", zip, "--url", "https://example.com/a.zip", "--key-env", "K", "--out", outp}
	if err := run(args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(outp)
	m, err := update.ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := update.VerifyFile(zip, m, priv.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if raw["schema"] != float64(1) {
		t.Fatal("schema")
	}
}

func TestManifestRejectsMismatchedKey(t *testing.T) {
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	embeddedPublicKey = func() (ed25519.PublicKey, error) { return otherPub, nil }
	t.Cleanup(func() { embeddedPublicKey = update.PublicKey })
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv("K", base64.StdEncoding.EncodeToString(priv.Seed()))
	err := run([]string{"manifest", "--zip", "x", "--out", "y", "--key-env", "K"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("got %v", err)
	}
}

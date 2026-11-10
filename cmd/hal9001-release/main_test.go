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
	embeddedPublicKey = func() (ed25519.PublicKey, error) { return priv.Public().(ed25519.PublicKey), nil }
	t.Cleanup(func() { embeddedPublicKey = update.PublicKey })
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

func TestPromote(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	embeddedPublicKey = func() (ed25519.PublicKey, error) { return pub, nil }
	t.Cleanup(func() { embeddedPublicKey = update.PublicKey })
	t.Setenv("K", base64.StdEncoding.EncodeToString(priv.Seed()))
	d := t.TempDir()
	zip, beta, notes, outp := filepath.Join(d, "a.zip"), filepath.Join(d, "beta.json"), filepath.Join(d, "n.md"), filepath.Join(d, "stable.json")
	_ = os.WriteFile(zip, []byte("zip"), 0o644)
	_ = os.WriteFile(notes, []byte("stable notes"), 0o644)
	if err := run([]string{"manifest", "--channel", "beta", "--version", "0.1.1", "--build", "1", "--commit", "abc",
		"--zip", zip, "--url", "https://example.com/a.zip", "--key-env", "K", "--out", beta}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("K", "") // promote needs no private key
	args := []string{"promote", "--manifest", beta, "--zip", zip, "--channel", "stable", "--notes-file", notes, "--out", outp}
	if err := run(args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var in, got update.Manifest
	b, _ := os.ReadFile(beta)
	in, _ = update.ParseManifest(b)
	b, _ = os.ReadFile(outp)
	got, err := update.ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Channel != "stable" || got.Notes != "stable notes" || got.Signature != in.Signature || got.SHA256 != in.SHA256 || got.Build != 1 {
		t.Fatalf("got %+v", got)
	}
	if err := update.VerifyFile(zip, got, pub); err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(zip, []byte("zap"), 0o644) // same size, different bytes
	if err := run(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("zip mismatch: %v", err)
	}
	_ = os.WriteFile(zip, []byte("zip"), 0o644)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	embeddedPublicKey = func() (ed25519.PublicKey, error) { return other, nil }
	if err := run(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("wrong key: %v", err)
	}
}

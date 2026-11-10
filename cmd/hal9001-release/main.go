// Command hal9001-release generates update-signing keys, signs release files
// and writes channel update manifests. Used by CI and by the maintainer.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"hal9001/internal/update"
)

// embeddedPublicKey is a variable so tests can substitute a key.
var embeddedPublicKey = update.PublicKey

const publicKeyPath = "internal/update/publickey.txt"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "hal9001-release:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: hal9001-release keygen|sign|manifest|promote [flags]")
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:], out)
	case "sign":
		return sign(args[1:], out)
	case "manifest":
		return manifest(args[1:])
	case "promote":
		return promote(args[1:])
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func keygen(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	privOut := fs.String("private-out", "", "file to write the private key to (mode 0600)")
	pubOut := fs.String("public-out", publicKeyPath, "file to write the public key to")
	force := fs.Bool("force", false, "overwrite a public key file that already holds a key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *privOut == "" {
		return errors.New("--private-out is required")
	}
	if _, err := os.Stat(*privOut); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite", *privOut)
	}
	if existing, err := os.ReadFile(*pubOut); err == nil && !*force {
		if _, err := update.ParsePublicKeyText(string(existing)); !errors.Is(err, update.ErrNoPublicKey) {
			return fmt.Errorf("%s already holds a key; use --force to replace it", *pubOut)
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	f, err := os.OpenFile(*privOut, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(priv.Seed()) + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(*pubOut, []byte(pubB64+"\n"), 0o600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, `Public key: %s

Next steps:
  1. gh secret set HAL9001_UPDATE_SIGNING_KEY < %s
  2. Commit %s
  3. Store %s in a password manager
  4. Delete %s
`, pubB64, *privOut, *pubOut, *privOut, *privOut)
	return nil
}

func loadKey(env string) (ed25519.PrivateKey, error) {
	v := os.Getenv(env)
	if strings.TrimSpace(v) == "" {
		return nil, fmt.Errorf("environment variable %s is empty", env)
	}
	return update.ParsePrivateKey(v)
}

func sign(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	keyEnv := fs.String("key-env", "HAL9001_UPDATE_SIGNING_KEY", "env var holding the private key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: sign [--key-env VAR] <file>")
	}
	priv, err := loadKey(*keyEnv)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, update.Sign(priv, data))
	return nil
}

func manifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	channel := fs.String("channel", "", "stable or beta")
	version := fs.String("version", "", "version, e.g. 0.1.42")
	build := fs.Int("build", 0, "build number")
	commit := fs.String("commit", "", "commit sha")
	zip := fs.String("zip", "", "path to the .app.zip")
	url := fs.String("url", "", "https download URL of the zip")
	notesURL := fs.String("notes-url", "", "release notes URL")
	notesFile := fs.String("notes-file", "", "markdown release notes file")
	minMacOS := fs.String("min-macos", "13.0", "minimum macOS version")
	keyEnv := fs.String("key-env", "HAL9001_UPDATE_SIGNING_KEY", "env var holding the private key")
	outPath := fs.String("out", "", "manifest output path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *zip == "" || *outPath == "" {
		return errors.New("--zip and --out are required")
	}
	priv, err := loadKey(*keyEnv)
	if err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	switch embedded, err := embeddedPublicKey(); {
	case errors.Is(err, update.ErrNoPublicKey):
	case err != nil:
		return err
	case !embedded.Equal(pub):
		return errors.New("signing key does not match internal/update/publickey.txt; wrong secret?")
	}
	data, err := os.ReadFile(*zip)
	if err != nil {
		return err
	}
	var notes string
	if *notesFile != "" {
		b, err := os.ReadFile(*notesFile)
		if err != nil {
			return err
		}
		notes = string(b)
	}
	sum := sha256.Sum256(data)
	m := update.Manifest{
		Schema:      1,
		Channel:     *channel,
		Version:     *version,
		Build:       *build,
		Commit:      *commit,
		PublishedAt: time.Now().UTC().Truncate(time.Second),
		MinMacOS:    *minMacOS,
		URL:         *url,
		Size:        int64(len(data)),
		SHA256:      hex.EncodeToString(sum[:]),
		Signature:   update.Sign(priv, data),
		NotesURL:    *notesURL,
		Notes:       notes,
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if err := update.VerifyFile(*zip, m, pub); err != nil {
		return fmt.Errorf("self-verification failed: %w", err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*outPath, append(b, '\n'), 0o600)
}

// promote rewrites a verified manifest for another channel without re-signing:
// the signature covers the zip bytes, which do not change. It needs no private key.
func promote(args []string) error {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	in := fs.String("manifest", "", "input manifest (the release's update.json)")
	channel := fs.String("channel", "stable", "channel to write")
	zip := fs.String("zip", "", "path to the release .app.zip the manifest describes")
	notesFile := fs.String("notes-file", "", "markdown notes that replace the manifest notes")
	outPath := fs.String("out", "", "output manifest path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *zip == "" || *outPath == "" {
		return errors.New("--manifest, --zip and --out are required")
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	m, err := update.ParseManifest(raw)
	if err != nil {
		return err
	}
	pub, err := embeddedPublicKey()
	if err != nil {
		return fmt.Errorf("cannot verify the release without a committed public key (%s): %w", publicKeyPath, err)
	}
	if err := update.VerifyFile(*zip, m, pub); err != nil {
		return fmt.Errorf("refusing to promote: %w", err)
	}
	m.Channel = *channel
	if *notesFile != "" {
		b, err := os.ReadFile(*notesFile)
		if err != nil {
			return err
		}
		m.Notes = string(b)
	}
	if err := m.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*outPath, append(b, '\n'), 0o600) //nolint:gosec // CI-chosen output path
}

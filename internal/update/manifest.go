// Package update: manifest format and signature verification for app updates.
package update

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
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

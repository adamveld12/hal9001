package update

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"hal9001/internal/service"
)

// Current is the running app build, read from the Info.plist of the bundle the binary lives in.
type Current struct {
	BundleID string `json:"bundle_id"`
	Version  string `json:"version"` // CFBundleShortVersionString
	Build    int    `json:"build"`   // CFBundleVersion; 0 for a local build
}

// ErrNotBundled means a binary does not live in an app bundle.
var ErrNotBundled = errors.New("not running from an app bundle")

// EnvCurrent overrides the detected current build for tests and the e2e harness: "<bundle id>:<version>:<build>",
// for example "dev.hal9001:0.1.5:5". The daemon then also skips the public-key requirement (it never verifies
// signatures itself; the app does).
const EnvCurrent = "HAL9001_UPDATE_CURRENT"

// ParseCurrentEnv parses the EnvCurrent format.
func ParseCurrentEnv(s string) (Current, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return Current{}, fmt.Errorf("%s must be <bundle id>:<version>:<build>, got %q", EnvCurrent, s)
	}
	build, err := strconv.Atoi(parts[2])
	if err != nil || build < 0 {
		return Current{}, fmt.Errorf("%s: build %q is not a number", EnvCurrent, parts[2])
	}
	return Current{BundleID: parts[0], Version: parts[1], Build: build}, nil
}

// BundleRoot returns the app bundle (<x>.app) that contains the executable exe (<bundle>/Contents/<dir>/<name>), or
// ErrNotBundled. Symlinks in exe are resolved first, so the CLI link ~/.local/bin/hal9001 finds the app it points into.
func BundleRoot(exe string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	contents := filepath.Dir(filepath.Dir(exe))
	if filepath.Base(contents) != "Contents" {
		return "", ErrNotBundled
	}
	root := filepath.Dir(contents)
	if !strings.HasSuffix(root, ".app") {
		return "", ErrNotBundled
	}
	return root, nil
}

// ReadBundle reads the identity of the app bundle at root from its Contents/Info.plist.
func ReadBundle(root string) (Current, error) {
	info, err := ReadInfoPlist(filepath.Join(root, "Contents", "Info.plist"))
	if err != nil {
		return Current{}, err
	}
	c := Current{BundleID: info["CFBundleIdentifier"], Version: info["CFBundleShortVersionString"]}
	if v := info["CFBundleVersion"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Current{}, fmt.Errorf("%s: CFBundleVersion %q is not a number", root, v)
		}
		c.Build = n
	}
	if c.BundleID == "" {
		return Current{}, fmt.Errorf("%s: Info.plist has no CFBundleIdentifier", root)
	}
	return c, nil
}

// ReadInfoPlist reads the top-level string, integer and boolean values of an XML Info.plist. The plists in the
// app bundle are written as XML by cmd/hal9001-app-pack; a binary plist is an error.
func ReadInfoPlist(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller-chosen bundle path
	if err != nil {
		return nil, err
	}
	return ParseInfoPlist(data)
}

// ParseInfoPlist is ReadInfoPlist for content already in memory.
func ParseInfoPlist(data []byte) (map[string]string, error) {
	if bytes.HasPrefix(data, []byte("bplist")) {
		return nil, errors.New("binary plists are not supported")
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	// Find the top-level <dict>.
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("plist: no top-level dict: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dict" {
			break
		}
	}
	out := map[string]string{}
	var key string
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("plist: %w", err)
		}
		switch t := tok.(type) {
		case xml.EndElement:
			if t.Name.Local == "dict" {
				return out, nil
			}
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				key, err = elementText(dec)
				if err != nil {
					return nil, err
				}
			case "string", "integer", "real":
				v, err := elementText(dec)
				if err != nil {
					return nil, err
				}
				if key != "" {
					out[key] = v
				}
				key = ""
			case "true", "false":
				if key != "" {
					out[key] = t.Name.Local
				}
				key = ""
				if err := dec.Skip(); err != nil {
					return nil, err
				}
			default: // nested dict, array, data, date: not needed
				key = ""
				if err := dec.Skip(); err != nil {
					return nil, err
				}
			}
		}
	}
}

func elementText(dec *xml.Decoder) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return "", fmt.Errorf("plist: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			return b.String(), nil
		}
	}
}

// CompareVersions compares dotted numeric versions ("0.1.10" > "0.1.9"). A missing component counts as 0 and a
// component that is not a number as 0.
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(strings.TrimSpace(as[i]))
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(strings.TrimSpace(bs[i]))
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// DetectOptions are the inputs of Detect.
type DetectOptions struct {
	// Exe is the running binary (os.Executable).
	Exe string
	// Env is the value of EnvCurrent.
	Env string
	// HasPublicKey reports whether a signing key is embedded; nil means PublicKey() succeeds.
	HasPublicKey func() bool
}

// Detect decides which build is running and whether the updater may offer updates. reason is empty when it may;
// otherwise it says why not (shown by GET /api/update as disabled_reason). Only the release bundle id
// (service.ReleaseBundleID) with a CI build number (> 0) and an embedded signing key is updatable. EnvCurrent
// replaces the bundle lookup and the key requirement.
func Detect(o DetectOptions) (cur Current, reason string) {
	override := strings.TrimSpace(o.Env) != ""
	if override {
		c, err := ParseCurrentEnv(o.Env)
		if err != nil {
			return Current{}, err.Error()
		}
		cur = c
	} else {
		root, err := BundleRoot(o.Exe)
		if err != nil {
			return Current{}, "not running from an app bundle"
		}
		if cur, err = ReadBundle(root); err != nil {
			return Current{}, "cannot read the app bundle: " + err.Error()
		}
	}
	switch {
	case cur.BundleID != service.ReleaseBundleID:
		return cur, "dev build (bundle " + cur.BundleID + ")"
	case cur.Build <= 0:
		return cur, "local build (build number 0); only CI releases update themselves"
	}
	if !override {
		has := o.HasPublicKey
		if has == nil {
			has = func() bool { _, err := PublicKey(); return err == nil }
		}
		if !has() {
			return cur, "this build has no update signing key"
		}
	}
	return cur, ""
}

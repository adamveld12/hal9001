package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Kind is a kind of file under the home dir.
type Kind string

const (
	KindConfig Kind = "config"
	KindEnv    Kind = "env"
	KindSensor Kind = "sensor"
	KindFlow   Kind = "flow"
	KindPrompt Kind = "prompt"
	// KindSensorEnv is a sensor's own env file, env.d/sensors/<id>.env. It
	// holds secrets, so it lives outside the git-versioned sensor YAML.
	KindSensorEnv Kind = "sensor_env"
	// KindTool is tools/<id>.yaml, a user-defined agent tool.
	KindTool Kind = "tool"
	// KindToolEnv is a tool's own env file, env.d/tools/<id>.env.
	KindToolEnv Kind = "tool_env"
	// KindSandbox is a sandbox backend's config, sandboxes/<backend>.yaml, and
	// KindSandboxEnv its own env file, env.d/sandboxes/<backend>.env.
	KindSandbox    Kind = "sandbox"
	KindSandboxEnv Kind = "sandbox_env"
)

// Directory names under the home dir (REQUIREMENTS §3).
const (
	DirSensors    = "sensors"
	DirFlows      = "flows"
	DirPrompts    = "prompts"
	DirEnvD       = "env.d"
	DirSensorEnv  = "env.d/sensors"
	DirTools      = "tools"
	DirToolEnv    = "env.d/tools"
	DirSandboxes  = "sandboxes"
	DirSandboxEnv = "env.d/sandboxes"
	DirData       = "data"
	DirBlobs      = "data/blobs"
	DirState      = "data/state"
	DirWorktrees  = "data/worktrees"
	FileConfig    = "config.yaml"
	FileEnv       = "env"
	FileDB        = "data/hal.db"
)

// Rel returns the file's path relative to the home dir. config and env ignore
// id. For the other kinds an empty id yields "".
func (k Kind) Rel(id string) string {
	switch k {
	case KindConfig:
		return FileConfig
	case KindEnv:
		return FileEnv
	case KindSensor:
		return idPath(DirSensors, id, ".yaml")
	case KindFlow:
		return idPath(DirFlows, id, ".yaml")
	case KindPrompt:
		return idPath(DirPrompts, id, ".md")
	case KindSensorEnv:
		return idPath(DirSensorEnv, id, ".env")
	case KindTool:
		return idPath(DirTools, id, ".yaml")
	case KindToolEnv:
		return idPath(DirToolEnv, id, ".env")
	case KindSandbox:
		return idPath(DirSandboxes, id, ".yaml")
	case KindSandboxEnv:
		return idPath(DirSandboxEnv, id, ".env")
	}
	return ""
}

func idPath(dir, id, ext string) string {
	if id == "" {
		return ""
	}
	return dir + "/" + id + ext
}

// Path returns the absolute path of the file under home.
func (k Kind) Path(home, id string) string { return filepath.Join(home, filepath.FromSlash(k.Rel(id))) }

func (k Kind) needsID() bool {
	return k == KindSensor || k == KindFlow || k == KindPrompt || k == KindSensorEnv || k == KindTool || k == KindToolEnv || k == KindSandbox || k == KindSandboxEnv
}

// Errors returned by the file functions.
var (
	// ErrConflict means the file on disk differs from what the caller last
	// saw (or, for create-only writes, already exists).
	ErrConflict = errors.New("file changed on disk")
	// ErrInvalidID means the id cannot be used as a file name.
	ErrInvalidID = errors.New("invalid id")
	// ErrNotFound means the file does not exist.
	ErrNotFound = errors.New("file not found")
)

// Hash returns the content hash used for conflict detection and flow
// versioning: the lowercase hex SHA-256 of the file bytes.
func Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func checkTarget(k Kind, id string) error {
	if k.needsID() && !ValidID(id) {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	if k.Rel(id) == "" {
		return fmt.Errorf("unknown file kind %q", k)
	}
	return nil
}

// ReadFile returns a file's bytes and hash.
func ReadFile(home string, k Kind, id string) ([]byte, string, error) {
	if err := checkTarget(k, id); err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(k.Path(home, id)) //nolint:gosec // path is built from a validated id under home
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("%w: %s", ErrNotFound, k.Rel(id))
	}
	if err != nil {
		return nil, "", err
	}
	return b, Hash(b), nil
}

// writeMu serializes the check-then-rename in WriteFile and DeleteFile within
// the process. It cannot stop another program (an editor) from writing
// between the check and the rename; that window is a few microseconds.
var writeMu sync.Mutex

// WriteFile atomically writes data as the file of kind k and id.
//
// expectedHash is the hash of the content the caller based its edit on. An
// empty expectedHash means "create": the file must not exist. Otherwise the
// file on disk must hash to expectedHash. In both cases a mismatch returns an
// error wrapping ErrConflict and writes nothing. The new content's hash is
// returned. Missing directories are created. The data is not validated.
func WriteFile(home string, k Kind, id string, data []byte, expectedHash string) (string, error) {
	if err := checkTarget(k, id); err != nil {
		return "", err
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	path := k.Path(home, id)
	mode, err := checkExpected(path, k.Rel(id), expectedHash)
	if err != nil {
		return "", err
	}
	if k == KindSensorEnv || k == KindToolEnv || k == KindSandboxEnv {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
	}
	if err := writeAtomic(path, data, mode); err != nil {
		return "", err
	}
	return Hash(data), nil
}

// DeleteFile removes a file if it still hashes to expectedHash.
func DeleteFile(home string, k Kind, id string, expectedHash string) error {
	if err := checkTarget(k, id); err != nil {
		return err
	}
	if expectedHash == "" {
		return fmt.Errorf("%w: delete needs the hash of the version being deleted", ErrConflict)
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	path := k.Path(home, id)
	if _, err := checkExpected(path, k.Rel(id), expectedHash); err != nil {
		return err
	}
	return os.Remove(path)
}

// checkExpected verifies the on-disk state against expectedHash and returns
// the permission bits to write with.
func checkExpected(path, rel, expectedHash string) (fs.FileMode, error) {
	cur, err := os.ReadFile(path) //nolint:gosec // path is built from a validated id under home
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if expectedHash != "" {
			return 0, fmt.Errorf("%w: %s was deleted", ErrConflict, rel)
		}
		return 0o600, nil
	case err != nil:
		return 0, err
	case expectedHash == "":
		return 0, fmt.Errorf("%w: %s already exists", ErrConflict, rel)
	case Hash(cur) != expectedHash:
		return 0, fmt.Errorf("%w: %s no longer matches the version you loaded", ErrConflict, rel)
	}
	mode := fs.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return mode, nil
}

// writeAtomic writes data to path through a temp file in the same directory
// and a rename. A symlink at path is written through, not replaced. Temp
// files start with "." so the watcher ignores them.
func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// WriteFlow is WriteFile for flows/<id>.yaml.
func WriteFlow(home, id string, data []byte, expectedHash string) (string, error) {
	return WriteFile(home, KindFlow, id, data, expectedHash)
}

// WriteSensor is WriteFile for sensors/<id>.yaml.
func WriteSensor(home, id string, data []byte, expectedHash string) (string, error) {
	return WriteFile(home, KindSensor, id, data, expectedHash)
}

// WritePrompt is WriteFile for prompts/<id>.md.
func WritePrompt(home, id string, data []byte, expectedHash string) (string, error) {
	return WriteFile(home, KindPrompt, id, data, expectedHash)
}

// WriteConfig is WriteFile for config.yaml.
func WriteConfig(home string, data []byte, expectedHash string) (string, error) {
	return WriteFile(home, KindConfig, "", data, expectedHash)
}

// WriteEnv is WriteFile for the env file (created with mode 0600).
func WriteEnv(home string, data []byte, expectedHash string) (string, error) {
	return WriteFile(home, KindEnv, "", data, expectedHash)
}

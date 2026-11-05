package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// File is one loaded file: its parsed value (nil if the file could not be
// parsed at all), its content hash, and every finding about it. A File is
// usable by the engine when Valid reports true.
type File[T any] struct {
	// Name is the file stem (the id for sensors, flows and prompts).
	Name string `json:"name"`
	// Path is relative to the home dir, e.g. "flows/triage.yaml".
	Path string `json:"path"`
	// Hash is the content hash of Raw; empty if the file does not exist.
	Hash        string       `json:"hash"`
	Value       *T           `json:"value"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	// Raw is the file content as read.
	Raw []byte `json:"-"`
}

// Valid reports whether the file parsed and has no error diagnostics.
func (f *File[T]) Valid() bool { return f.Value != nil && !HasErrors(f.Diagnostics) }

// Workspace is everything under the home dir. Load never fails as a whole: a
// bad file yields a File with diagnostics and does not affect the others.
type Workspace struct {
	Home string `json:"home"`
	// Config.Value is never nil; a missing or unparsable config.yaml yields the
	// defaults plus diagnostics.
	Config File[Config] `json:"config"`
	Env    File[Env]    `json:"env"`
	// SensorEnv are the per-sensor env files (env.d/sensors/<id>.env); Name is
	// the sensor id. A file does not need a matching sensor to be loaded.
	SensorEnv []*File[Env]    `json:"sensor_env"`
	Sensors   []*File[Sensor] `json:"sensors"`
	Flows     []*File[Flow]   `json:"flows"`
	Prompts   []*File[Prompt] `json:"prompts"`
	// Diagnostics are findings not tied to one file, such as an unreadable
	// directory.
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// MarshalJSON exposes only the variable names, never values.
func (e *Env) MarshalJSON() ([]byte, error) {
	keys := e.Keys
	if keys == nil {
		keys = []string{}
	}
	return json.Marshal(struct {
		Keys []string `json:"keys"`
	}{keys})
}

// Sensor returns the sensor file with the given id, or nil.
func (w *Workspace) Sensor(id string) *File[Sensor] { return find(w.Sensors, id) }

// Flow returns the flow file with the given id, or nil.
func (w *Workspace) Flow(id string) *File[Flow] { return find(w.Flows, id) }

// Prompt returns the prompt file with the given name, or nil.
func (w *Workspace) Prompt(name string) *File[Prompt] { return find(w.Prompts, name) }

func find[T any](files []*File[T], name string) *File[T] {
	for _, f := range files {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// Refs returns the references flows may use: every sensor and prompt file
// present (valid or not) and the configured runtimes.
func (w *Workspace) Refs() *Refs {
	r := &Refs{Sensors: map[string]bool{}, Prompts: map[string]bool{}, Runtimes: map[string]Runtime{}}
	for _, s := range w.Sensors {
		r.Sensors[s.Name] = true
	}
	for _, p := range w.Prompts {
		r.Prompts[p.Name] = true
	}
	if w.Config.Value != nil {
		for k, v := range w.Config.Value.Runtimes {
			r.Runtimes[k] = v
		}
	}
	return r
}

// AllDiagnostics returns every diagnostic in the workspace, file by file.
func (w *Workspace) AllDiagnostics() []Diagnostic {
	out := append([]Diagnostic(nil), w.Diagnostics...)
	out = append(out, w.Config.Diagnostics...)
	out = append(out, w.Env.Diagnostics...)
	for _, f := range w.SensorEnv {
		out = append(out, f.Diagnostics...)
	}
	for _, f := range w.Sensors {
		out = append(out, f.Diagnostics...)
	}
	for _, f := range w.Flows {
		out = append(out, f.Diagnostics...)
	}
	for _, f := range w.Prompts {
		out = append(out, f.Diagnostics...)
	}
	return out
}

// Load reads the whole home dir. Missing files and directories are not
// errors: they yield defaults and empty lists.
func Load(home string) *Workspace {
	w := &Workspace{Home: home}
	w.loadConfig()
	w.loadEnv()
	w.loadSensorEnv()

	sensorNames := w.listDir(DirSensors, ".yaml")
	flowNames := w.listDir(DirFlows, ".yaml")
	promptNames := w.listDir(DirPrompts, ".md")

	// References use file names, so they exist before any file is validated.
	refs := &Refs{Sensors: map[string]bool{}, Prompts: map[string]bool{}, Runtimes: map[string]Runtime{}}
	for _, n := range sensorNames {
		refs.Sensors[n] = true
	}
	for _, n := range promptNames {
		refs.Prompts[n] = true
	}
	for k, v := range w.Config.Value.Runtimes {
		refs.Runtimes[k] = v
	}

	for _, n := range sensorNames {
		w.Sensors = append(w.Sensors, loadFile(home, KindSensor, n, func(b []byte) (*Sensor, []Diagnostic) { return CheckSensor(n, b) }))
	}
	for _, n := range promptNames {
		w.Prompts = append(w.Prompts, loadFile(home, KindPrompt, n, func(b []byte) (*Prompt, []Diagnostic) { return CheckPrompt(n, b) }))
	}
	for _, n := range flowNames {
		w.Flows = append(w.Flows, loadFile(home, KindFlow, n, func(b []byte) (*Flow, []Diagnostic) { return CheckFlow(n, b, refs) }))
	}
	return w
}

// loadFile reads one file and runs check on it. Read failures become
// diagnostics.
func loadFile[T any](home string, k Kind, name string, check func([]byte) (*T, []Diagnostic)) *File[T] {
	f := &File[T]{Name: name, Path: k.Rel(name)}
	b, err := os.ReadFile(k.Path(home, name)) //nolint:gosec // name comes from a directory listing under home
	if err != nil {
		f.Diagnostics = []Diagnostic{{File: f.Path, Severity: SeverityError, Code: CodeReadError, Message: err.Error()}}
		return f
	}
	f.Raw, f.Hash = b, Hash(b)
	f.Value, f.Diagnostics = check(b)
	if !ValidID(name) {
		f.Diagnostics = append(f.Diagnostics, Diagnostic{
			File: f.Path, Severity: SeverityError, Code: CodeInvalidID,
			Message: fmt.Sprintf("file name %q is not a valid id: use letters, digits, - and _ (max 64)", name),
		})
	}
	return f
}

func (w *Workspace) loadConfig() {
	f := File[Config]{Name: "config", Path: FileConfig}
	b, err := os.ReadFile(KindConfig.Path(w.Home, "")) //nolint:gosec // fixed name under home
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f.Diagnostics = []Diagnostic{{File: FileConfig, Severity: SeverityInfo, Code: CodeMissingFile, Message: "config.yaml not found; using defaults"}}
	case err != nil:
		f.Diagnostics = []Diagnostic{{File: FileConfig, Severity: SeverityError, Code: CodeReadError, Message: err.Error()}}
	default:
		f.Raw, f.Hash = b, Hash(b)
		f.Value, f.Diagnostics = CheckConfig(b)
	}
	if f.Value == nil {
		def := Config{}
		def.ApplyDefaults()
		f.Value = &def
	}
	w.Config = f
}

func (w *Workspace) loadEnv() {
	f := File[Env]{Name: "env", Path: FileEnv}
	b, err := os.ReadFile(KindEnv.Path(w.Home, "")) //nolint:gosec // fixed name under home
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f.Value = &Env{Vars: map[string]string{}}
	case err != nil:
		f.Value = &Env{Vars: map[string]string{}}
		f.Diagnostics = []Diagnostic{{File: FileEnv, Severity: SeverityError, Code: CodeReadError, Message: err.Error()}}
	default:
		f.Raw, f.Hash = b, Hash(b)
		f.Value, f.Diagnostics = ParseEnv(b)
		f.Diagnostics = withFile(FileEnv, f.Diagnostics)
	}
	w.Env = f
}

// loadSensorEnv reads every env.d/sensors/<id>.env.
func (w *Workspace) loadSensorEnv() {
	for _, n := range w.listDir(DirSensorEnv, ".env") {
		rel := KindSensorEnv.Rel(n)
		f := &File[Env]{Name: n, Path: rel}
		b, err := os.ReadFile(KindSensorEnv.Path(w.Home, n)) //nolint:gosec // name comes from a directory listing under home
		if err != nil {
			f.Value = &Env{Vars: map[string]string{}}
			f.Diagnostics = []Diagnostic{{File: rel, Severity: SeverityError, Code: CodeReadError, Message: err.Error()}}
		} else {
			f.Raw, f.Hash = b, Hash(b)
			f.Value, f.Diagnostics = ParseEnv(b)
			f.Diagnostics = withFile(rel, f.Diagnostics)
		}
		w.SensorEnv = append(w.SensorEnv, f)
	}
}

// SensorEnvFor returns the per-sensor env file of a sensor, or nil.
func (w *Workspace) SensorEnvFor(id string) *File[Env] { return find(w.SensorEnv, id) }

// listDir returns the stems of the regular files in dir with extension ext,
// sorted. Hidden files, editor backups and directories are skipped silently;
// a ".yml" twin of a ".yaml" extension gets a warning.
func (w *Workspace) listDir(dir, ext string) []string {
	entries, err := os.ReadDir(filepath.Join(w.Home, dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		w.Diagnostics = append(w.Diagnostics, Diagnostic{File: dir, Severity: SeverityError, Code: CodeReadError, Message: err.Error()})
		return nil
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") {
			continue
		}
		if e.IsDir() {
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(w.Home, dir, name)); err != nil || st.IsDir() {
				continue
			}
		}
		switch filepath.Ext(name) {
		case ext:
			names = append(names, strings.TrimSuffix(name, ext))
		case ".yml":
			if ext == ".yaml" {
				w.Diagnostics = append(w.Diagnostics, Diagnostic{
					File: dir + "/" + name, Severity: SeverityWarning, Code: CodeIgnoredFile,
					Message: "ignored: files must use the .yaml extension",
				})
			}
		}
	}
	return names
}

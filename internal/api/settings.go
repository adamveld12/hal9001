package api

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"hal9001/internal/spec"
)

// ---- settings (config.yaml) ----

var sensitiveKeyRE = regexp.MustCompile(`(?i)hash|secret|password|token|key`)

// redactAny returns v with the values of sensitive keys replaced, at any depth.
func redactAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if sensitiveKeyRE.MatchString(k) {
				out[k] = redacted
			} else {
				out[k] = redactAny(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = redactAny(val)
		}
		return out
	}
	return v
}

// redactConfig returns a copy of c that is safe to return from the API:
// values of sensitive keys in auth module options and every runtime
// environment value (including the ask/full spawn env) are replaced by «redacted».
func redactConfig(c spec.Config) spec.Config {
	c = c.Clone()
	runtimes := make(map[string]spec.Runtime, len(c.Runtimes))
	for name, rt := range c.Runtimes {
		rt.Command = slices.Clone(rt.Command)
		rt.Env = redactEnv(rt.Env)
		rt.Ask.Args = slices.Clone(rt.Ask.Args)
		rt.Ask.Env = redactEnv(rt.Ask.Env)
		rt.Full.Args = slices.Clone(rt.Full.Args)
		rt.Full.Env = redactEnv(rt.Full.Env)
		if rt.SessionMeta != nil {
			rt.SessionMeta = redactAny(rt.SessionMeta).(map[string]any)
		}
		runtimes[name] = rt
	}
	if c.Runtimes != nil {
		c.Runtimes = runtimes
	}
	for name, mods := range c.Auth.Stacks {
		for i := range mods {
			if mods[i].Options != nil {
				mods[i].Options = redactAny(mods[i].Options).(map[string]any)
			}
		}
		c.Auth.Stacks[name] = mods
	}
	if c.AllowedHosts == nil {
		c.AllowedHosts = []string{}
	}
	if c.AllowedLogins == nil {
		c.AllowedLogins = []string{}
	}
	if c.Runtimes == nil {
		c.Runtimes = map[string]spec.Runtime{}
	}
	return c
}

func redactEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k := range env {
		out[k] = redacted
	}
	return out
}

type settingsView struct {
	Hash   string      `json:"hash"`
	Config spec.Config `json:"config"`
}

// configFile reads config.yaml. A missing file gives the defaults and the
// hash of empty content, and exists=false.
func (s *Server) configFile() (raw []byte, hash string, exists bool, err error) {
	raw, hash, err = spec.ReadFile(s.opts.Home, spec.KindConfig, "")
	if errors.Is(err, spec.ErrNotFound) {
		return nil, spec.Hash(nil), false, nil
	}
	return raw, hash, err == nil, err
}

func (s *Server) settingsView(raw []byte, hash string) settingsView {
	cfg, _ := spec.CheckConfig(raw)
	if cfg == nil {
		cfg = &spec.Config{}
		cfg.ApplyDefaults()
	}
	view := redactConfig(*cfg)
	if !conforms("Config", view) {
		// A hand-edited file with out-of-range values: show the defaults the
		// daemon falls back to; the diagnostics of the file say what is wrong.
		def := spec.Config{}
		def.ApplyDefaults()
		view = redactConfig(def)
	}
	return settingsView{Hash: hash, Config: view}
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	raw, hash, _, err := s.configFile()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsView(raw, hash))
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		ExpectedHash       *string        `json:"expected_hash"`
		MaxAgentRuns       *int           `json:"max_agent_runs"`
		MaxCommandRuns     *int           `json:"max_command_runs"`
		RetentionDays      *int           `json:"retention_days"`
		DedupRetentionDays *int           `json:"dedup_retention_days"`
		NotifyMinPriority  *string        `json:"notify_min_priority"`
		ApprovalTimeout    *spec.Duration `json:"approval_timeout"`
	}
	if !decodeJSON(w, r, &patch, false) {
		return
	}
	if patch.ExpectedHash != nil && !expectedHashOK(w, *patch.ExpectedHash) {
		return
	}
	set := map[string]any{}
	for name, v := range map[string]*int{
		"max_agent_runs": patch.MaxAgentRuns, "max_command_runs": patch.MaxCommandRuns,
		"retention_days": patch.RetentionDays, "dedup_retention_days": patch.DedupRetentionDays,
	} {
		if v == nil {
			continue
		}
		if *v < 1 {
			fieldProblem(w, name, name+" must be at least 1")
			return
		}
		set[name] = *v
	}
	if patch.NotifyMinPriority != nil {
		if !slices.Contains(spec.Priorities, *patch.NotifyMinPriority) {
			fieldProblem(w, "notify_min_priority", "notify_min_priority must be one of "+strings.Join(spec.Priorities, ", "))
			return
		}
		set["notify_min_priority"] = *patch.NotifyMinPriority
	}
	if patch.ApprovalTimeout != nil {
		if patch.ApprovalTimeout.Std() <= 0 {
			fieldProblem(w, "approval_timeout", "approval_timeout must be a positive duration")
			return
		}
		set["approval_timeout"] = patch.ApprovalTimeout.String()
	}
	if len(set) == 0 {
		fieldProblem(w, "", "set at least one setting to change")
		return
	}

	raw, hash, exists, err := s.configFile()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if patch.ExpectedHash != nil && *patch.ExpectedHash != hash {
		fileConflict(w, "config.yaml changed since you loaded it", *patch.ExpectedHash, hash)
		return
	}
	updated, err := patchYAMLTopLevel(raw, set)
	if err != nil {
		fieldProblem(w, "", err.Error())
		return
	}
	// Refuse only problems this change introduces, so a config that was
	// already broken can still be repaired through the API.
	_, before := spec.CheckConfig(raw)
	_, after := spec.CheckConfig(updated)
	if introduced := newErrors(before, after); len(introduced) > 0 {
		validationProblem(w, "the change makes config.yaml invalid", after)
		return
	}
	expected := hash
	if !exists {
		expected = ""
	}
	if _, err := spec.WriteFile(s.opts.Home, spec.KindConfig, "", updated, expected); err != nil {
		if errors.Is(err, spec.ErrConflict) {
			_, cur, _, _ := s.configFile()
			fileConflict(w, err.Error(), hash, cur)
			return
		}
		s.writeError(w, r, err)
		return
	}
	s.reload()
	s.publishFileChange(spec.KindConfig, "")
	raw, hash, _, err = s.configFile()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsView(raw, hash))
}

// newErrors returns the error diagnostics in after that are not in before.
func newErrors(before, after []spec.Diagnostic) []spec.Diagnostic {
	key := func(d spec.Diagnostic) string { return d.Code + "|" + d.Path + "|" + d.Message }
	seen := map[string]bool{}
	for _, d := range before {
		seen[key(d)] = true
	}
	var out []spec.Diagnostic
	for _, d := range after {
		if d.Severity == spec.SeverityError && !seen[key(d)] {
			out = append(out, d)
		}
	}
	return out
}

// patchYAMLTopLevel sets top-level scalar keys of a YAML mapping document and
// leaves everything else, comments included, as it was. Values are ints or
// strings.
func patchYAMLTopLevel(raw []byte, set map[string]any) ([]byte, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("config.yaml does not parse: %w", err)
		}
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	m := doc.Content[0]
	if m.Kind != yaml.MappingNode {
		return nil, errors.New("config.yaml must be a mapping at the top level")
	}
	keys := slices.Sorted(maps.Keys(set))
	for _, k := range keys {
		val := &yaml.Node{Kind: yaml.ScalarNode}
		switch v := set[k].(type) {
		case int:
			val.Tag, val.Value = "!!int", strconv.Itoa(v)
		default:
			val.Tag, val.Value = "!!str", fmt.Sprint(v)
		}
		replaced := false
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == k {
				val.LineComment = m.Content[i+1].LineComment
				m.Content[i+1] = val
				replaced = true
				break
			}
		}
		if !replaced {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, val)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- env ----

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const maxEnvValue = 64 << 10

// envFile reads an env file: the global one (spec.KindEnv) or a sensor's
// (spec.KindSensorEnv with the sensor id). A missing file is empty with an
// empty hash.
func (s *Server) envFile(k spec.Kind, id string) (raw []byte, hash string, err error) {
	raw, hash, err = spec.ReadFile(s.opts.Home, k, id)
	if errors.Is(err, spec.ErrNotFound) {
		return nil, "", nil
	}
	return raw, hash, err
}

// listEnvKeys returns variable names only. Values are never returned by any endpoint.
func (s *Server) listEnvKeys(w http.ResponseWriter, r *http.Request) {
	raw, _, err := s.envFile(spec.KindEnv, "")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	env, _ := spec.ParseEnv(raw)
	keys := slices.Clone(env.Keys)
	sort.Strings(keys)
	type item struct {
		Key string `json:"key"`
	}
	out := struct {
		Items []item `json:"items"`
	}{Items: []item{}}
	for _, k := range keys {
		out.Items = append(out.Items, item{Key: k})
	}
	writeJSON(w, http.StatusOK, out)
}

// quoteEnv renders value as a double-quoted env value that spec.ParseEnv
// reads back unchanged.
func quoteEnv(value string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(value) + `"`
}

// envSpans returns the [first, last] line ranges that define key. A quoted
// value may continue over several lines.
func envSpans(lines []string, key string) [][2]int {
	var spans [][2]int
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if rest, ok := strings.CutPrefix(t, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			t = strings.TrimSpace(rest)
		}
		rest, ok := strings.CutPrefix(t, key)
		if !ok || !strings.HasPrefix(strings.TrimLeft(rest, " \t"), "=") {
			continue
		}
		end := i
		for ; end < len(lines); end++ {
			env, ds := spec.ParseEnv([]byte(strings.Join(lines[i:end+1], "\n")))
			if !spec.HasErrors(ds) && len(env.Keys) == 1 {
				break
			}
		}
		if end >= len(lines) {
			end = i
		}
		spans = append(spans, [2]int{i, end})
		i = end
	}
	return spans
}

func splitEnvLines(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// withEnvKey returns the env file text with key set to value (set=true) or
// removed, keeping every other line, comments included.
func withEnvKey(raw []byte, key, value string, set bool) (out []byte, found bool) {
	lines := splitEnvLines(raw)
	spans := envSpans(lines, key)
	found = len(spans) > 0
	for i := len(spans) - 1; i >= 0; i-- {
		sp := spans[i]
		repl := []string(nil)
		if set && i == 0 {
			repl = []string{key + "=" + quoteEnv(value)}
		}
		lines = slices.Replace(lines, sp[0], sp[1]+1, repl...)
	}
	if set && !found {
		lines = append(lines, key+"="+quoteEnv(value))
	}
	if len(lines) == 0 {
		return nil, found
	}
	return []byte(strings.Join(lines, "\n") + "\n"), found
}

// writeEnv applies change to an env file (see envFile), retrying when another
// writer got in between.
func (s *Server) writeEnv(k spec.Kind, id string, change func(raw []byte) ([]byte, error)) error {
	var err error
	for range 3 {
		raw, hash, rerr := s.envFile(k, id)
		if rerr != nil {
			return rerr
		}
		updated, cerr := change(raw)
		if cerr != nil {
			return cerr
		}
		_, err = spec.WriteFile(s.opts.Home, k, id, updated, hash)
		if !errors.Is(err, spec.ErrConflict) {
			return err
		}
	}
	return err
}

func (s *Server) setEnvValue(w http.ResponseWriter, r *http.Request) {
	s.setEnv(w, r, spec.KindEnv, "")
}

// setEnv is PUT of one key into the env file of kind k (id is the sensor id
// for spec.KindSensorEnv).
func (s *Server) setEnv(w http.ResponseWriter, r *http.Request, k spec.Kind, id string) {
	key := r.PathValue("key")
	var req struct {
		Value *string `json:"value"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	switch {
	case len(key) > 128 || !envKeyRE.MatchString(key):
		fieldProblem(w, "key", "key must be letters, digits and _, not starting with a digit (max 128)")
		return
	case req.Value == nil:
		fieldProblem(w, "value", "value is required")
		return
	case len(*req.Value) > maxEnvValue || strings.ContainsRune(*req.Value, 0):
		fieldProblem(w, "value", "value must be text of at most 64 KiB without NUL bytes")
		return
	}
	err := s.writeEnv(k, id, func(raw []byte) ([]byte, error) {
		out, _ := withEnvKey(raw, key, *req.Value, true)
		env, ds := spec.ParseEnv(out)
		if spec.HasErrors(ds) || env.Vars[key] != *req.Value {
			return nil, fmt.Errorf("%w: the env file cannot hold this value", spec.ErrInvalidID)
		}
		return out, nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.reload()
	s.publishFileChange(k, id)
	w.WriteHeader(http.StatusNoContent)
}

var errEnvKeyMissing = errors.New("no such environment key")

func (s *Server) deleteEnvValue(w http.ResponseWriter, r *http.Request) {
	s.deleteEnv(w, r, spec.KindEnv, "")
}

// deleteEnv is DELETE of one key from the env file of kind k.
func (s *Server) deleteEnv(w http.ResponseWriter, r *http.Request, k spec.Kind, id string) {
	key := r.PathValue("key")
	err := s.writeEnv(k, id, func(raw []byte) ([]byte, error) {
		out, found := withEnvKey(raw, key, "", false)
		if !found {
			return nil, errEnvKeyMissing
		}
		return out, nil
	})
	switch {
	case errors.Is(err, errEnvKeyMissing):
		notFound(w, "no environment key %q", key)
		return
	case err != nil:
		s.writeError(w, r, err)
		return
	}
	s.reload()
	s.publishFileChange(k, id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- per-sensor env ----

// listSensorEnv returns the key names of the sensor's own env file and whether
// each one overrides a key of the global env file. Values are never returned.
func (s *Server) listSensorEnv(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.sensorExists(w, id) {
		return
	}
	raw, _, err := s.envFile(spec.KindSensorEnv, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	own, _ := spec.ParseEnv(raw)
	graw, _, err := s.envFile(spec.KindEnv, "")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	global, _ := spec.ParseEnv(graw)
	keys := slices.Clone(own.Keys)
	sort.Strings(keys)
	type item struct {
		Key             string `json:"key"`
		OverridesGlobal bool   `json:"overrides_global"`
	}
	out := struct {
		Items []item `json:"items"`
	}{Items: []item{}}
	for _, k := range keys {
		_, ok := global.Vars[k]
		out.Items = append(out.Items, item{Key: k, OverridesGlobal: ok})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) setSensorEnvValue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.sensorExists(w, id) {
		return
	}
	s.setEnv(w, r, spec.KindSensorEnv, id)
}

func (s *Server) deleteSensorEnvValue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.sensorExists(w, id) {
		return
	}
	s.deleteEnv(w, r, spec.KindSensorEnv, id)
}

// Package spec is the typed model and filesystem layer for everything under
// the hal9001 home directory (REQUIREMENTS §3).
//
// # Model
//
// The home dir holds config.yaml (Config), env (Env: KEY=VALUE secrets),
// sensors/*.yaml (Sensor), flows/*.yaml (Flow of command, agent and inbox
// Nodes joined by Edges) and prompts/*.md (Prompt: YAML frontmatter plus a
// text/template body). All types carry yaml and json tags with the same
// snake_case names. Durations are strings such as "90s", "20m" or "7d".
//
// Parsed structs hold what the file says. ApplyDefaults fills defaults in
// place; Minimal returns a copy with defaults cleared again, which is what the
// Marshal functions write, so saving through the UI does not expand a
// hand-written file.
//
// # Loading and diagnostics
//
// Load reads the whole home dir into a Workspace and never fails as a whole:
// each file becomes a File with its parsed value and its own []Diagnostic, so
// one bad file does not hide the others. Diagnostic locates a finding by file,
// YAML path (for example nodes[2].ports), node id and line, and carries a
// stable Code.
//
// # Validation
//
// ValidateFlow, ValidateSensor, ValidatePrompt and ValidateConfig are pure
// functions over values; the Check functions combine parse and validate for
// raw bytes, and ParseFlow, ParseSensor, ParseConfig, ParsePrompt and ParseEnv
// do syntax-level parsing only. Flows are checked for DAG shape (cycles are
// reported with their path), edges to unknown nodes or undeclared ports,
// unreachable nodes, duplicate ids, references to sensors, prompts and
// runtimes (through Refs), enum values, allow-rule regular expressions and
// template syntax. Sensors also validate interval and 5-field cron schedules.
//
// # Templates
//
// Render executes Go text/template with missingkey=error and the helpers json,
// default and trunc over the data from NewData ({.event, .input, .flow,
// .run}). Errors name the missing key.
//
// # Writing and watching
//
// Hash is the SHA-256 of file bytes. WriteFlow, WriteSensor, WritePrompt,
// WriteConfig and WriteEnv write atomically and return ErrConflict when the
// file on disk no longer hashes to the caller's expected hash. Watch reports
// debounced file changes from fsnotify. Init creates the directory tree and
// copies seed files that do not exist yet.
package spec

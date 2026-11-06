package assist

import (
	"fmt"
	"sort"
	"strings"
)

// Files of a session's draft directory.
const (
	// SensorDraftFile is the draft sensor, the only file the agent edits in a sensor session.
	SensorDraftFile = "sensor.yaml"
	// FlowDraftFile is the draft flow of a flow session; the agent also writes new prompts under PromptsDir.
	FlowDraftFile = "flow.yaml"
	// instructionFiles carry the same text under the names the supported agents read (Codex and omp
	// read AGENTS.md, Claude Code reads CLAUDE.md).
	instructionFile  = "AGENTS.md"
	instructionFile2 = "CLAUDE.md"
)

// skeleton is the draft of a new sensor when the request carries no YAML.
const skeleton = `# New sensor. Describe what it should watch and the assistant fills this in.
id: new-sensor
interval: 5m
lang: bash
script: |
  # Print a JSON array, or one JSON object per line, of the items to watch.
  echo '[]'
key: "{{.id}}"
first_run: baseline
timeout: 60s
`

// instructions is the text of AGENTS.md / CLAUDE.md for one session.
//
// targetID is the sensor being edited ("" for a new sensor); globalEnv and sensorEnv are env variable
// NAMES only. The values never reach the agent.
func instructions(sessionID, targetID string, globalEnv, sensorEnv []string) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# Sensor authoring: instructions for the assistant\n\n")
	w("You help the user write or change ONE hal9001 sensor. hal9001 is a local daemon that polls systems on a schedule, ")
	w("turns what they return into events, and runs flows over those events. This directory is a private draft ")
	w("directory (session %s). Your whole job is the file `%s` in it.\n\n", sessionID, SensorDraftFile)

	if targetID != "" {
		w("You are EDITING the existing sensor `%s`. Keep `id: %s` unchanged: the user saves the draft over that sensor.\n\n", targetID, targetID)
	} else {
		w("You are creating a NEW sensor. Pick a short kebab-case `id` (letters, digits, `-` and `_`, at most 64 characters).\n\n")
	}

	w("## Rules\n\n")
	w("- Edit only `%s`. Do not create or change other files. The user's own sensor files, flows and settings are not here and must not be touched.\n", SensorDraftFile)
	w("- Never print, echo, log or ask for a secret value (tokens, API keys, passwords), and do not try to read `env` files or `~/.hal9001`. ")
	w("The script reads credentials from the environment variables listed below; you only ever see their names.\n")
	w("- Use only the tools you need. Reading, searching and editing `%s` are allowed. Running anything else asks the user for approval each time.\n", SensorDraftFile)
	w("- Run the two helper commands below exactly as written, one per call: no pipes, redirects, `&&`, `;` or `cd` (you are already in the right directory).\n")
	w("- Test the draft with `hal9001 assist test` before you say it is done, and fix what it reports.\n")
	w("- A test runs the script with the user's real credentials, so the first test after you change the script (`lang`, `script`, `command`, `cwd`, or the credentials it mentions) asks the user to approve it: they see the script and a diff against the last approved version. ")
	w("Before you run it, say in a sentence what the script does and which credentials it uses. Once the user allowed a version, testing the same script again does not ask. ")
	w("Changing only `interval`, `key`, `first_run` or `timeout` does not ask. If the user denies the test, or the command answers that the script needs approval, do not look for another way to run it: explain what you wanted to check and wait for the user.\n")
	w("- End every turn with a short plain-language summary of what you changed and what the test showed. The user sees the draft update live; say what they should check or decide.\n\n")

	w("## Helper commands\n\n")
	w("- `hal9001 assist draft` prints the current draft with its validation diagnostics as JSON.\n")
	w("- `hal9001 assist test` runs the draft the way a poll would (with the real credentials, which you cannot see) and prints JSON: ")
	w("`exit_code`, `timed_out`, `parse_error`, `item_count`, `items` (each with its rendered `dedup_key` or a `key_error`), and the redacted `stdout` and `stderr`. ")
	w("Nothing is recorded: no sensor run, no events. Values of the credentials appear as `«NAME»`. It refuses (and asks the user first) when the script changed since the user last tested or approved it.\n")
	w("- `hal9001 sensors validate sensor.yaml` checks the file against the sensor format only.\n\n")

	w("## The sensor file\n\n")
	w("```yaml\nid: github-notifications\ninterval: 2m            # or: cron: \"*/5 9-18 * * 1-5\"  (exactly one of the two)\nlang: bash\nscript: |\n  gh api notifications --paginate -q '.[]'\nkey: \"{{.id}}:{{.updated_at}}\"\nfirst_run: baseline\ntimeout: 60s\n```\n\n")
	w("| Field | Meaning |\n|---|---|\n")
	w("| `id` | Names the sensor and its file. |\n")
	w("| `interval` / `cron` | Exactly one. `interval` is a Go duration (`30s`, `2m`, `1h`; at least `1s`). `cron` has 5 fields (minute hour day-of-month month day-of-week) in local time. |\n")
	w("| `lang` | `bash`, `python` (python3), `node`, `jq` (`jq -c -f`) or `exec` (`command: [argv...]`, no shell, no `script`). Inline `script` for every language except `exec`. |\n")
	w("| `cwd` | Optional working directory (a plain path). Default: the sensor's persistent state directory `$HAL_STATE_DIR`. |\n")
	w("| `key` | Required. A Go `text/template` over ONE item that yields its dedup key. |\n")
	w("| `first_run` | `baseline` (default): the first poll marks every item seen without firing. `fire`: the first poll emits every item. |\n")
	w("| `timeout` | Default `60s`. The process group is killed when it is exceeded. |\n\n")

	w("## What the script must print\n\n")
	w("- stdout is the result: a JSON array of items, or JSONL (one JSON value per line). An item is normally an object. Print nothing for \"no items\" (or `[]`).\n")
	w("- Exit 0 on success; any other exit code fails the poll. stderr is kept as the log; put diagnostics there, never on stdout.\n")
	w("- stdout and stderr are each capped at 16 MiB; exceeding the cap on stdout fails the poll.\n")
	w("- Prefer one item per thing that can happen (one notification, one issue, one message), with a stable identifier inside it.\n")
	w("- Fetch incrementally when the API allows it: `HAL_LAST_SUCCESS_AT` (RFC 3339, set from the second poll on, unset before the first success) is the time of the last successful poll.\n")
	w("- Interpreters are found on the daemon's `PATH`. Prefer tools that are already common (`curl`, `jq`, `gh`, `python3`) and say so when a script needs one that may not be installed.\n\n")

	w("## The dedup key\n\n")
	w("Each item gets a key from the `key` template; items whose key was already seen are dropped, new keys become events that flows react to. ")
	w("The key therefore decides when the sensor fires again.\n\n")
	w("- `{{.id}}` fires once per item. `{{.id}}:{{.updated_at}}` fires again whenever the item changes. Choose deliberately and say which you chose.\n")
	w("- The template sees the item as `.` (fields as `{{.field}}`, nested `{{.a.b}}`). A missing field is an error, not an empty string.\n")
	w("- Helpers: `{{.title | trunc 80}}` (first N characters), `{{default \"n/a\" (index . \"maybe\")}}` (fallback for an optional or empty field), `{{json .nested}}` (compact JSON).\n")
	w("- Numbers keep their literal text, so large ids render unchanged.\n")
	w("- `hal9001 assist test` renders the key for every item (`dedup_key`) or reports a `key_error`. Make sure keys are present and distinct across items.\n\n")

	w("## Environment\n\n")
	w("The script runs with a small base environment (`HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`, `LC_ALL`, `LC_CTYPE`, `TMPDIR`, `TZ`, `SSH_AUTH_SOCK`, `XDG_*`, `PATH`), then the variables below, then:\n\n")
	w("| Variable | Value |\n|---|---|\n")
	w("| `HAL_STATE_DIR` | Persistent scratch directory of this sensor (a temporary one during `assist test`). |\n")
	w("| `HAL_LAST_SUCCESS_AT` | See above. |\n")
	w("| `HAL_RUN_ID`, `HAL_NODE` | The run id and the sensor id. |\n\n")
	w("Credentials available to this sensor (NAMES only; reference them as `$NAME`, never paste a value):\n\n")
	w("- From the global env file: %s\n", nameList(globalEnv))
	w("- Only for this sensor: %s\n\n", nameList(sensorEnv))
	w("If the sensor needs a credential that is not listed, tell the user which variable to add (name and what it is) instead of inventing one; they add it in the sensor's Environment section. ")
	w("Write the script so that a missing variable produces a clear message on stderr and a non-zero exit.\n")
	return b.String()
}

func nameList(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	s := append([]string(nil), names...)
	sort.Strings(s)
	for i, n := range s {
		s[i] = "`" + n + "`"
	}
	return strings.Join(s, ", ")
}

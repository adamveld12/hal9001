package e2e

import (
	"hal9001/internal/agent/fakeagent"
)

// Script-building shorthands for the fake ACP agent (see cmd/fake-acp-agent/README.md).
type (
	script = fakeagent.Script
	turn   = fakeagent.Turn
	step   = fakeagent.Step
)

// inMode selects turns by the session mode: "default" is the runtimes' `ask`
// mode (readonly and approve policies), "bypass" the `full` mode (auto).
func inMode(m string) *fakeagent.When { return &fakeagent.When{Mode: m} }

func say(text string) step { return step{Op: fakeagent.OpMessage, Text: text} }

// editPermission asks permission to edit file and, when allowed, writes data to
// it in the session cwd. onReject runs when the call is rejected.
func editPermission(id, file, data string, onReject ...step) step {
	old := ""
	return step{
		Op: fakeagent.OpPermission, ID: id, Kind: "edit", Title: "Write " + file,
		RawInput: map[string]any{"path": file, "content": data},
		Content:  []fakeagent.Content{{Type: "diff", Path: file, OldText: &old, NewText: data}},
		OnAllow:  []step{{Op: fakeagent.OpWriteFile, Path: file, Data: data}},
		OnReject: onReject,
	}
}

// execPermission asks permission to run a shell command.
func execPermission(id, command string, onAllow []step, onReject ...step) step {
	return step{
		Op: fakeagent.OpPermission, ID: id, Kind: "execute", Title: command,
		RawInput: map[string]any{"command": command},
		OnAllow:  onAllow, OnReject: onReject,
	}
}

// agentFlow is a flow with one agent node `work` (extra: more node YAML lines,
// indented four spaces) whose `out` and `denied` ports feed inbox nodes
// `done` and `refused`.
func agentFlow(id, sensor, mode, extra string) string {
	return agentFlowRT(id, sensor, "fake", mode, extra)
}

// agentFlowRT is agentFlow with an explicit runtime name.
func agentFlowRT(id, sensor, runtime, mode, extra string) string {
	return `id: ` + id + `
enabled: true
subscribe: [` + sensor + `]
nodes:
  - id: work
    type: agent
    runtime: ` + runtime + `
    mode: ` + mode + `
    prompt: p
` + extra + `
  - {id: done, type: inbox, title: "done {{.input.input.id}}"}
  - {id: refused, type: inbox, title: "refused {{.input.input.id}}", summary_md: "{{.input.agent.comment}}"}
edges:
  - {from: work, to: done}
  - {from: work, port: denied, to: refused}
`
}

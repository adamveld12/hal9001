package runner

import (
	"encoding/json"
	"strings"
	"testing"
)

func payloads(outs []Output) []string {
	var s []string
	for _, o := range outs {
		s = append(s, string(o.Payload))
	}
	return s
}

func TestParseNodeOutputResultTable(t *testing.T) {
	tests := []struct {
		name         string
		stdout       string
		exit         int
		ports        []string
		wantDecision Decision
		wantPorts    []string
		wantPayloads []string
		wantErr      string // substring
	}{
		{name: "empty exit 0 drops", stdout: "", exit: 0, wantDecision: DecisionDropped},
		{name: "whitespace only drops", stdout: " \n\t\n", exit: 0, wantDecision: DecisionDropped},
		{
			name: "one value emits on out", stdout: `{"a": 1}`, exit: 0,
			wantDecision: DecisionEmitted, wantPorts: []string{"out"}, wantPayloads: []string{`{"a":1}`},
		},
		{
			name: "pretty printed single value is one output", stdout: "{\n  \"a\": 1,\n  \"b\": [1, 2]\n}\n", exit: 0,
			wantDecision: DecisionEmitted, wantPorts: []string{"out"}, wantPayloads: []string{`{"a":1,"b":[1,2]}`},
		},
		{
			name: "non-object values emit on out", stdout: "[1,2]\n", exit: 0,
			wantDecision: DecisionEmitted, wantPorts: []string{"out"}, wantPayloads: []string{`[1,2]`},
		},
		{
			name: "JSONL fans out", stdout: "{\"n\":1}\n{\"n\":2}\n{\"n\":3}\n", exit: 0,
			wantDecision: DecisionEmitted, wantPorts: []string{"out", "out", "out"},
			wantPayloads: []string{`{"n":1}`, `{"n":2}`, `{"n":3}`},
		},
		{
			name: "JSONL routes each line by its own _port", exit: 0, ports: []string{"small", "large"},
			stdout:       "{\"_port\":\"small\",\"n\":1}\n{\"_port\":\"large\",\"n\":2}\n{\"_port\":\"small\",\"n\":3}\n",
			wantDecision: DecisionEmitted, wantPorts: []string{"small", "large", "small"},
			wantPayloads: []string{`{"n":1}`, `{"n":2}`, `{"n":3}`},
		},
		{
			name: "implicit error port is routable", stdout: `{"_port":"error","why":"x"}`, exit: 0, ports: []string{"a"},
			wantDecision: DecisionEmitted, wantPorts: []string{"error"}, wantPayloads: []string{`{"why":"x"}`},
		},
		{name: "undeclared port is error", stdout: `{"_port":"nope"}`, exit: 0, ports: []string{"a"}, wantDecision: DecisionError, wantErr: `"nope" is not declared`},
		{name: "default out is undeclared when node declares other ports", stdout: `{"x":1}`, exit: 0, ports: []string{"a", "b"}, wantDecision: DecisionError, wantErr: `"out" is not declared`},
		{
			name: "one bad port fails the whole fan-out", exit: 0, ports: []string{"a"},
			stdout:       "{\"_port\":\"a\"}\n{\"_port\":\"zzz\"}\n",
			wantDecision: DecisionError, wantErr: `output 2`,
		},
		{name: "non-string _port", stdout: `{"_port":3}`, exit: 0, wantDecision: DecisionError, wantErr: "_port must be a string"},
		{name: "non-JSON stdout", stdout: "hello world\n", exit: 0, wantDecision: DecisionError, wantErr: "not valid JSON"},
		{name: "JSON then garbage", stdout: "{\"a\":1}\nhello\n", exit: 0, wantDecision: DecisionError, wantErr: "not valid JSON"},
		{name: "non-zero exit with empty stdout", stdout: "", exit: 2, wantDecision: DecisionError, wantErr: "exit status 2"},
		{name: "non-zero exit with valid JSON still errors", stdout: `{"a":1}`, exit: 1, wantDecision: DecisionError, wantErr: "exit status 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseNodeOutput([]byte(tc.stdout), tc.exit, tc.ports)
			if got.Decision != tc.wantDecision {
				t.Fatalf("decision = %q (err %q), want %q", got.Decision, got.Error, tc.wantDecision)
			}
			if tc.wantErr != "" && !strings.Contains(got.Error, tc.wantErr) {
				t.Errorf("error = %q, want substring %q", got.Error, tc.wantErr)
			}
			if tc.wantDecision != DecisionEmitted {
				if len(got.Outputs) != 0 {
					t.Errorf("non-emitted outcome carries outputs: %+v", got.Outputs)
				}
				return
			}
			var gotPorts []string
			for _, o := range got.Outputs {
				gotPorts = append(gotPorts, o.Port)
			}
			if strings.Join(gotPorts, ",") != strings.Join(tc.wantPorts, ",") {
				t.Errorf("ports = %v, want %v", gotPorts, tc.wantPorts)
			}
			if strings.Join(payloads(got.Outputs), "|") != strings.Join(tc.wantPayloads, "|") {
				t.Errorf("payloads = %v, want %v", payloads(got.Outputs), tc.wantPayloads)
			}
		})
	}
}

func TestParseNodeOutputReservedKeys(t *testing.T) {
	got := ParseNodeOutput([]byte(`{"z":1,"_port":"hi","a":{"_port":"nested"},"_reason":"because","big":12345678901234567890}`), 0, []string{"hi"})
	if got.Decision != DecisionEmitted || len(got.Outputs) != 1 {
		t.Fatalf("outcome = %+v", got)
	}
	o := got.Outputs[0]
	if o.Port != "hi" || o.Reason != "because" {
		t.Errorf("port/reason = %q/%q", o.Port, o.Reason)
	}
	// Reserved keys are stripped only at the top level; order and number precision are preserved.
	if want := `{"z":1,"a":{"_port":"nested"},"big":12345678901234567890}`; string(o.Payload) != want {
		t.Errorf("payload = %s, want %s", o.Payload, want)
	}
	if !json.Valid(o.Payload) {
		t.Errorf("payload is not valid JSON: %s", o.Payload)
	}
}

func TestParseNodeOutputReasonOnlyKeepsDefaultPort(t *testing.T) {
	got := ParseNodeOutput([]byte(`{"_reason":"review requested","id":7}`), 0, nil)
	o := got.Outputs[0]
	if o.Port != "out" || o.Reason != "review requested" || string(o.Payload) != `{"id":7}` {
		t.Errorf("output = %+v / %s", o, o.Payload)
	}
}

func TestResultOutcomeTimeoutKillAndTruncation(t *testing.T) {
	ok := Result{Stdout: []byte(`{"a":1}`)}
	if got := ok.Outcome(nil); got.Decision != DecisionEmitted {
		t.Errorf("plain result: %+v", got)
	}
	if got := (Result{TimedOut: true, Killed: true, Stdout: []byte(`{"a":1}`)}).Outcome(nil); got.Decision != DecisionError || got.Error != "timeout" {
		t.Errorf("timed out: %+v", got)
	}
	if got := (Result{Killed: true, ExitCode: -1}).Outcome(nil); got.Decision != DecisionError || got.Error != "killed" {
		t.Errorf("killed: %+v", got)
	}
	trunc := Result{Stdout: []byte(`{"a":`), StdoutTruncated: true, StdoutBytes: 100}
	if got := trunc.Outcome(nil); got.Decision != DecisionError || !strings.Contains(got.Error, "output cap") {
		t.Errorf("truncated stdout: %+v", got)
	}
}

func TestParseSensorItems(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{name: "empty", in: "", want: nil},
		{name: "array", in: `[{"id":1}, {"id": 2}]`, want: []string{`{"id":1}`, `{"id":2}`}},
		{name: "empty array", in: "[]\n", want: []string{}},
		{name: "multiline array", in: "[\n {\"id\":1},\n {\"id\":2}\n]\n", want: []string{`{"id":1}`, `{"id":2}`}},
		{name: "jsonl", in: "{\"id\":1}\n{\"id\":2}\n", want: []string{`{"id":1}`, `{"id":2}`}},
		{name: "jsonl of arrays stays items", in: "[1]\n[2]\n", want: []string{`[1]`, `[2]`}},
		{name: "reserved keys are not interpreted", in: `{"_port":"x"}`, want: []string{`{"_port":"x"}`}},
		{name: "garbage", in: "not json", wantErr: true},
		{name: "truncated array", in: `[{"id":1},`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSensorItems([]byte(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			var gs []string
			for _, g := range got {
				gs = append(gs, string(g))
			}
			if strings.Join(gs, "|") != strings.Join(tc.want, "|") || len(gs) != len(tc.want) {
				t.Errorf("items = %v, want %v", gs, tc.want)
			}
		})
	}
}

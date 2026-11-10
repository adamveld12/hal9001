package apispec

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// operations calls fn for every operation of the contract.
func operations(t *testing.T, fn func(method, path string, op *openapi3.Operation)) {
	t.Helper()
	doc, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			fn(method, path, op)
		}
	}
}

func TestContractLoadsAndValidates(t *testing.T) {
	doc, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !doc.IsOpenAPI31OrLater() {
		t.Errorf("openapi version = %q, want 3.1.x", doc.OpenAPI)
	}
}

func TestEveryOperationIsDeclaredAndDocumentsProblems(t *testing.T) {
	seen := map[string]string{}
	operations(t, func(method, path string, op *openapi3.Operation) {
		id := method + " " + path
		if op.OperationID == "" {
			t.Errorf("%s: missing operationId", id)
		} else if prev, dup := seen[op.OperationID]; dup {
			t.Errorf("%s: operationId %q already used by %s", id, op.OperationID, prev)
		} else {
			seen[op.OperationID] = id
		}
		svc, _ := op.Extensions[ExtService].(string)
		if !slices.Contains(Services, svc) {
			t.Errorf("%s: %s = %q, want one of %v", id, ExtService, svc, Services)
		}
		public, _ := op.Extensions[ExtPublic].(bool)

		has := func(code string) bool { return op.Responses.Value(code) != nil }
		require := func(code, why string) {
			if !has(code) {
				t.Errorf("%s: no %s response (%s)", id, code, why)
			}
		}
		if !public {
			require("401", "every non-public operation can be unauthenticated")
		}
		require("403", "baseline and service checks apply to every operation")
		if op.Responses.Default() == nil {
			t.Errorf("%s: no default problem response", id)
		}
		if strings.Contains(path, "{") && op.OperationID != "setEnvValue" { // PUT creates the key
			require("404", "path has a parameter")
		}
		if method != http.MethodGet && method != http.MethodHead {
			require("415", "mutating requests must be application/json")
		}
		if op.RequestBody != nil || hasQueryParams(op) {
			require("400", "operation takes input")
		}
		if usesExpectedHash(op) {
			require("409", "operation carries expected_hash")
		}
		if op.Extensions[ExtPaused] == "reject" {
			require("423", "operation is rejected while paused")
		}

		// Every 4xx, 5xx, and default response is problem+json only.
		for code, ref := range op.Responses.Map() {
			if code != "default" && !strings.HasPrefix(code, "4") && !strings.HasPrefix(code, "5") {
				continue
			}
			if ref.Value == nil || len(ref.Value.Content) != 1 || ref.Value.Content.Get("application/problem+json") == nil {
				t.Errorf("%s: %s response must be application/problem+json only", id, code)
			}
		}
	})
	// Capabilities of REQUIREMENTS §10.2 and §11 that must exist as operations.
	for _, id := range []string{
		"listSensors", "createSensor", "getSensor", "updateSensor", "deleteSensor", "validateSensor", "pollSensor", "resetSensor", "pauseSensor", "resumeSensor", "listSensorEnv", "setSensorEnv", "deleteSensorEnv",
		"listSensorRuns", "getSensorRun",
		"listEvents", "injectEvent", "getEvent", "replayEvent",
		"listFlows", "createFlow", "getFlow", "updateFlow", "deleteFlow", "validateFlow", "testFlowNode", "runFlow",
		"listFlowVersions", "getFlowVersion",
		"listFlowRuns", "getFlowRun", "getNodeRun",
		"listAgentRuns", "getAgentRun", "listAgentEvents", "listAgentPermissionRequests", "downloadAgentRunRaw",
		"listApprovals", "getApproval", "approveApproval", "denyApproval",
		"listInbox", "getInboxItem", "updateInboxItem", "bulkInbox",
		"listPrompts", "createPrompt", "getPrompt", "updatePrompt", "deletePrompt", "renderPrompt",
		"listRuntimes", "probeRuntime",
		"listAssistSessions", "createAssistSession", "getAssistSession", "deleteAssistSession", "sendAssistMessage", "updateAssistDraft", "testAssistDraft", "listAssistSamples", "cancelAssistSession",
		"getSettings", "updateSettings", "listEnvKeys", "setEnvValue", "deleteEnvValue",
		"getKillSwitch", "pauseKillSwitch", "resumeKillSwitch",
		"beginAuth", "continueAuth", "logout", "whoami", "listSessions", "revokeSession", "revokeAllSessions",
		"listPasskeys", "beginPasskeyRegistration", "finishPasskeyRegistration", "deletePasskey", "listAuthEvents",
		"getTailscaleStatus", "getHealth", "getVersion", "getStatus", "streamEvents", "getBlob", "getUIShell",
	} {
		if _, ok := seen[id]; !ok {
			t.Errorf("operation %s is missing from the contract", id)
		}
	}
}

func hasQueryParams(op *openapi3.Operation) bool {
	for _, p := range op.Parameters {
		if p.Value != nil && p.Value.In == "query" {
			return true
		}
	}
	return false
}

func usesExpectedHash(op *openapi3.Operation) bool {
	for _, p := range op.Parameters {
		if p.Value != nil && p.Value.Name == "expected_hash" {
			return true
		}
	}
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return false
	}
	mt := op.RequestBody.Value.Content.Get("application/json")
	if mt == nil || mt.Schema == nil || mt.Schema.Value == nil {
		return false
	}
	_, ok := mt.Schema.Value.Properties["expected_hash"]
	return ok
}

// Reads are guarded by api, decisions by approve, everything that changes state or runs code by admin
// (REQUIREMENTS §10.4). The POST operations below only compute and persist nothing, so they are reads. The
// transform ones run jq, but only the program internal/transform generates (user text is JSON-encoded into it,
// never spliced into jq syntax) on the posted sample, with a timeout. serveMcp is only the MCP transport: every tool it
// serves calls the REST API with the caller's credentials, so the endpoint of a tool with side effects holds it to admin.
func TestServiceMapping(t *testing.T) {
	computeOnly := []string{"validateSensor", "validateTool", "validateFlow", "renderPrompt", "previewTransform", "compileTransform", "serveMcp"}
	operations(t, func(method, path string, op *openapi3.Operation) {
		svc, _ := op.Extensions[ExtService].(string)
		public, _ := op.Extensions[ExtPublic].(bool)
		id := op.OperationID
		switch {
		case public:
			// Login and liveness endpoints are exempt from the stack; their nominal service is ui or api.
		case strings.HasPrefix(path, "/api/approvals/") && method == http.MethodPost:
			if svc != "approve" {
				t.Errorf("%s: service %q, want approve", id, svc)
			}
		case method == http.MethodGet || slices.Contains(computeOnly, id):
			if svc == "approve" || svc == "ui" && path != "/" && !strings.HasPrefix(path, "/assets/") {
				t.Errorf("%s: read operation has service %q", id, svc)
			}
		default:
			if svc != "admin" {
				t.Errorf("%s: %s %s changes state or runs code but has service %q, want admin", id, method, path, svc)
			}
		}
	})
}

func TestEveryErrorSchemaUsesDeclaredProblemType(t *testing.T) {
	doc := MustLoad()
	declared := map[string]bool{}
	for _, v := range doc.Components.Schemas["ProblemType"].Value.Enum {
		declared[v.(string)] = true
	}
	if len(declared) == 0 {
		t.Fatal("ProblemType has no values")
	}
	used := map[string]bool{}
	for name, ref := range doc.Components.Schemas {
		if !strings.HasSuffix(name, "Problem") || name == "Problem" {
			continue
		}
		var types []string
		for _, part := range ref.Value.AllOf {
			if p := part.Value.Properties["type"]; p != nil {
				for _, v := range p.Value.Enum {
					types = append(types, v.(string))
				}
			}
		}
		if len(types) == 0 {
			t.Errorf("%s does not pin its problem type", name)
		}
		for _, ty := range types {
			if !declared[ty] {
				t.Errorf("%s uses undeclared problem type %q", name, ty)
			}
			used[ty] = true
		}
	}
	for ty := range declared {
		// internal and unavailable are carried by the default response, which has the generic Problem schema.
		if !used[ty] && !strings.HasSuffix(ty, ":internal") && !strings.HasSuffix(ty, ":unavailable") {
			t.Errorf("problem type %q is declared but no error schema uses it", ty)
		}
	}
}

func TestEverySSEEventTypeHasASchema(t *testing.T) {
	doc := MustLoad()
	stream := doc.Paths.Find("/api/stream").Get
	raw, ok := stream.Extensions[ExtSSEEvents].(map[string]any)
	if !ok || len(raw) == 0 {
		t.Fatalf("/api/stream has no %s map", ExtSSEEvents)
	}
	var types []string
	for _, v := range doc.Components.Schemas["StreamEventType"].Value.Enum {
		types = append(types, v.(string))
	}
	sort.Strings(types)

	var documented []string
	for ty := range raw {
		documented = append(documented, ty)
	}
	sort.Strings(documented)
	if !slices.Equal(types, documented) {
		t.Errorf("StreamEventType enum %v differs from %s keys %v", types, ExtSSEEvents, documented)
	}

	union := doc.Components.Schemas["StreamEvent"].Value
	var unionRefs []string
	for _, o := range union.OneOf {
		unionRefs = append(unionRefs, o.Ref)
	}
	for _, ty := range types {
		ref, _ := raw[ty].(string)
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		schema := doc.Components.Schemas[name]
		if schema == nil {
			t.Errorf("%s: schema %q does not exist", ty, ref)
			continue
		}
		if got := union.Discriminator.Mapping[ty]; got.Ref != ref {
			t.Errorf("%s: StreamEvent discriminator maps to %q, want %q", ty, got.Ref, ref)
		}
		if !slices.Contains(unionRefs, ref) {
			t.Errorf("%s: %s is not in the StreamEvent oneOf", ty, name)
		}
		// The envelope must pin its type and carry a typed payload.
		var pinned []any
		var data *openapi3.SchemaRef
		for _, part := range schema.Value.AllOf {
			if p := part.Value.Properties["type"]; p != nil {
				pinned = p.Value.Enum
			}
			if d := part.Value.Properties["data"]; d != nil {
				data = d
			}
		}
		if len(pinned) != 1 || pinned[0] != ty {
			t.Errorf("%s: schema %s pins type %v", ty, name, pinned)
		}
		if data == nil || data.Ref == "" {
			t.Errorf("%s: schema %s has no referenced data schema", ty, name)
		}
	}
}

func TestServiceFor(t *testing.T) {
	tests := []struct {
		method, path, want string
	}{
		{"GET", "/api/flows", "api"},
		{"GET", "/api/flows/triage-github", "api"},
		{"PUT", "/api/flows/triage-github", "admin"},
		{"DELETE", "/api/flows/triage-github?expected_hash=abc", "admin"},
		{"POST", "/api/flows/validate", "api"},
		{"POST", "/api/flows/triage-github/test-node", "admin"},
		{"POST", "/api/prompts/render", "api"},
		{"PUT", "/api/prompts/render", "admin"},
		{"POST", "/api/events", "admin"},
		{"POST", "/api/approvals/01JC7Z3K9Q8V5T2M4N6P8R0S1T/approve", "approve"},
		{"POST", "/api/approvals/01JC7Z3K9Q8V5T2M4N6P8R0S1T/deny", "approve"},
		{"GET", "/api/stream", "api"},
		{"POST", "/api/killswitch/pause", "admin"},
		{"PUT", "/api/env/SLACK_USER_TOKEN", "admin"},
		{"DELETE", "/api/auth/sessions", "admin"},
		{"DELETE", "/api/auth/sessions/abc", "admin"},
		{"POST", "/api/auth/api/begin", "ui"},
		{"GET", "/", "ui"},
		{"GET", "/assets/index-abc.js", "ui"},
		{"GET", "/assets/chunks/editor.js", "ui"},
		{"GET", "/flows/triage-github", "ui"},
		{"POST", "/api/nope", ""},
		{"GET", "/api/nope", ""},
		{"POST", "/flows", ""},
	}
	for _, tc := range tests {
		if got := ServiceFor(tc.method, tc.path); got != tc.want {
			t.Errorf("ServiceFor(%s, %s) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestIsPublic(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/health", true},
		{"POST", "/api/auth/admin/begin", true},
		{"POST", "/api/auth/admin/continue", true},
		{"GET", "/api/auth/whoami", true},
		{"POST", "/api/auth/logout", true},
		{"GET", "/api/auth/sessions", false},
		{"GET", "/api/flows", false},
		{"GET", "/nothing", false},
	} {
		if got := IsPublic(tc.method, tc.path); got != tc.want {
			t.Errorf("IsPublic(%s, %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

// recorder collects the errors ValidateResponse reports.
type recorder struct{ errs []string }

func (r *recorder) Helper()                      {}
func (r *recorder) Errorf(f string, args ...any) { r.errs = append(r.errs, fmt.Sprintf(f, args...)) }
func (r *recorder) failed() bool                 { return len(r.errs) > 0 }
func (r *recorder) String() string               { return strings.Join(r.errs, "\n") }

// reply describes a response; response builds it so that the test controls when its body is closed.
type reply struct {
	status      int
	contentType string
	body        string
}

func respond(status int, contentType, body string) reply { return reply{status, contentType, body} }

func (r reply) response() *http.Response {
	rec := httptest.NewRecorder()
	if r.contentType != "" {
		rec.Header().Set("Content-Type", r.contentType)
	}
	rec.WriteHeader(r.status)
	_, _ = rec.WriteString(r.body)
	return rec.Result()
}

func TestValidateResponse(t *testing.T) {
	const hash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	flowJSON := `{
	  "id": "triage-github", "path": "flows/triage-github.yaml", "hash": "` + hash + `", "yaml": "id: triage-github\n",
	  "flow": {
	    "id": "triage-github", "enabled": true, "dry_run": false, "subscribe": ["github-notifications"], "max_concurrent": 2,
	    "nodes": [
	      {"id": "only-review-requests", "type": "command", "lang": "jq", "script": "select(.reason == \"review_requested\")", "ui": {"x": 100, "y": 80}},
	      {"id": "classify", "type": "command", "lang": "python", "ports": ["small", "large"], "script": "print(1)"},
	      {"id": "review-small", "type": "agent", "runtime": "claude", "mode": "approve", "approve": "proposal", "prompt": "pr-review",
	       "extra_prompt": "Focus on tests.", "model": "sonnet", "timeout": "20m",
	       "cwd": {"mode": "worktree", "repo": "~/code/{{.input.repository.name}}", "ref": "origin/main"}},
	      {"id": "note-large", "type": "inbox", "title": "Large PR needs you", "priority": "high", "summary_md": "{{.input.subject.url}}"}
	    ],
	    "edges": [
	      {"from": "only-review-requests", "to": "classify"},
	      {"from": "classify", "port": "small", "to": "review-small"},
	      {"from": "classify", "port": "large", "to": "note-large"}
	    ]
	  },
	  "diagnostics": []
	}`

	tests := []struct {
		name        string
		method, url string
		resp        reply
		wantErr     string // substring; empty means the response must conform
	}{
		{"flow from REQUIREMENTS section 6", "GET", "/api/flows/triage-github", respond(200, "application/json", flowJSON), ""},
		{"health", "GET", "/api/health", respond(200, "application/json", `{"status":"ok","version":"1.0.0"}`), ""},
		{"health with unknown status value", "GET", "/api/health", respond(200, "application/json", `{"status":"down","version":"1"}`), "does not match"},
		{"flow with an unknown node type", "GET", "/api/flows/x", respond(200, "application/json",
			`{"id":"x","path":"flows/x.yaml","hash":"`+hash+`","yaml":"","flow":{"id":"x","nodes":[{"id":"a","type":"webhook"}]},"diagnostics":[]}`), "does not match"},
		{"flow node with an unknown field", "GET", "/api/flows/x", respond(200, "application/json",
			`{"id":"x","path":"flows/x.yaml","hash":"`+hash+`","yaml":"","flow":{"id":"x","nodes":[{"id":"a","type":"inbox","title":"t","retries":3}]},"diagnostics":[]}`), "does not match"},
		{"not found problem", "GET", "/api/flows/missing", respond(404, "application/problem+json",
			`{"type":"urn:hal9001:problem:not-found","title":"Not found","status":404}`), ""},
		{"not found with another problem type", "GET", "/api/flows/missing", respond(404, "application/problem+json",
			`{"type":"urn:hal9001:problem:conflict","title":"Not found","status":404}`), "does not match"},
		{"conflict on save", "PUT", "/api/flows/x", respond(409, "application/problem+json",
			`{"type":"urn:hal9001:problem:conflict","title":"Conflict","status":409,"expected_hash":"`+hash+`","current_hash":"`+hash+`"}`), ""},
		{"validation problem needs diagnostics", "PUT", "/api/flows/x", respond(422, "application/problem+json",
			`{"type":"urn:hal9001:problem:validation-failed","title":"Invalid","status":422}`), "does not match"},
		{"validation problem with diagnostics", "PUT", "/api/flows/x", respond(422, "application/problem+json",
			`{"type":"urn:hal9001:problem:validation-failed","title":"Invalid","status":422,"diagnostics":[{"severity":"error","code":"cycle","message":"a -> b -> a","file":"flows/x.yaml","path":"edges[1]"}]}`), ""},
		{"unauthenticated lists methods", "GET", "/api/flows", respond(401, "application/problem+json",
			`{"type":"urn:hal9001:problem:unauthenticated","title":"Authentication required","status":401,"service":"api","methods":["password","webauthn"]}`), ""},
		{"error served as plain json", "GET", "/api/flows/missing", respond(404, "application/json",
			`{"type":"urn:hal9001:problem:not-found","title":"Not found","status":404}`), "Content-Type"},
		{"inject creates event", "POST", "/api/events?sensor_id=github-notifications", respond(201, "application/json",
			`{"event":{"id":"01JC7Z3K9Q8V5T2M4N6P8R0S1T","sensor_id":"github-notifications","sensor_run_id":null,"dedup_key":"1:2","payload":{"id":"1"},"created_at":"2026-11-05T10:00:00.123456789Z","injected":true},"deduplicated":false,"dedup_key":"1:2","flow_runs":[]}`), ""},
		{"event stream is checked by content type", "GET", "/api/stream", respond(200, "text/event-stream", "event: stream.ready\ndata: {}\n\n"), ""},
		{"event stream served as json", "GET", "/api/stream", respond(200, "application/json", "{}"), "Content-Type"},
		{"no content", "DELETE", "/api/flows/x?expected_hash=" + hash, respond(204, "", ""), ""},
		{"operation not in contract", "GET", "/api/unknown", respond(200, "application/json", "{}"), "not in the contract"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.url, nil)
			var rec recorder
			resp := tc.resp.response()
			defer func() { _ = resp.Body.Close() }()
			ValidateResponse(&rec, req, resp)
			switch {
			case tc.wantErr == "" && rec.failed():
				t.Errorf("response rejected: %s", rec.String())
			case tc.wantErr != "" && !rec.failed():
				t.Errorf("response accepted, want an error containing %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(rec.String(), tc.wantErr):
				t.Errorf("error %q does not contain %q", rec.String(), tc.wantErr)
			}
		})
	}
}

func TestValidateResponseLeavesBodyReadable(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/health", nil)
	resp := respond(200, "application/json", `{"status":"ok","version":"1"}`).response()
	defer func() { _ = resp.Body.Close() }()
	var rec recorder
	ValidateResponse(&rec, req, resp)
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if got := string(buf[:n]); got != `{"status":"ok","version":"1"}` {
		t.Errorf("body after validation = %q", got)
	}
}

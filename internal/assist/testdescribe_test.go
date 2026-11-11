package assist

import (
	"strings"
	"testing"

	"hal9001/internal/store"
)

func TestDraftTestApprovalIsDescribedFromTheQueuedScript(t *testing.T) {
	s := &session{row: store.AssistSession{TargetKind: store.AssistTargetSensor, TargetID: "gh-prs"}}
	if at := s.testDescribe(nil, ""); at.Description != "" {
		t.Errorf("nothing queued: %+v", at)
	}
	s.toolTest = &pendingTest{id: scriptID{Lang: "bash", Script: "gh pr list", Env: []string{"GH_TOKEN"}}}
	at := s.testDescribe(nil, "")
	if at.Title != "Test the draft sensor gh-prs" || !strings.Contains(at.Description, "bash script") || !strings.Contains(at.Description, "the credentials GH_TOKEN") || !strings.Contains(at.Command, "gh pr list") {
		t.Errorf("sensor = %+v", at)
	}

	s.row = store.AssistSession{TargetKind: store.AssistTargetFlow, TargetID: "reviews"}
	s.toolTest = &pendingTest{id: scriptID{Flow: true, Nodes: []nodeScript{{ID: "build", Lang: "bash", Script: "make"}}}}
	at = s.testDescribe(nil, "")
	if !strings.Contains(at.Description, "command nodes (build)") || !strings.Contains(at.Description, "none of the user's credentials") {
		t.Errorf("flow = %+v", at)
	}
}

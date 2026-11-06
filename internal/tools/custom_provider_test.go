package tools

import (
	"context"
	"strings"
	"testing"

	"hal9001/internal/spec"
)

func TestRunToolInSandboxWithoutSandbox(t *testing.T) {
	_, err := runToolInSandbox(context.Background(), nil, &spec.Tool{ID: "x", Lang: "bash", Script: "true"}, nil, UserOptions{})
	if err == nil || !strings.Contains(err.Error(), "set sandbox: on the agent node") {
		t.Fatalf("err = %v", err)
	}
}

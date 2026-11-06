package content

import (
	"strings"
	"testing"
)

func TestDemoFlowFiltersAndRoutes(t *testing.T) {
	drop := compiled(t, "demo", "drop-noise")
	route := compiled(t, "demo", "route")
	if out := jqNode(t, drop, `{"id":"3","severity":"noise"}`); out != "" {
		t.Errorf("noise must be dropped, got %s", out)
	}
	if out := jqNode(t, drop, `{"id":"1","severity":"urgent"}`); out != `{"id":"1","severity":"urgent"}` {
		t.Errorf("a kept item must pass through unchanged: %s", out)
	}
	if out := jqNode(t, route, `{"severity":"urgent"}`); !strings.Contains(out, `"_port":"urgent"`) {
		t.Errorf("urgent routed wrongly: %s", out)
	}
	if out := jqNode(t, route, `{"severity":"normal"}`); !strings.Contains(out, `"_port":"normal"`) {
		t.Errorf("normal routed wrongly: %s", out)
	}
}

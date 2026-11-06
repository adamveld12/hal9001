package content

import (
	"testing"

	"go.yaml.in/yaml/v3"

	"hal9001/internal/transform"
)

func TestFlowsAreConsistent(t *testing.T) {
	sensors := map[string]bool{}
	for _, n := range names(t, "sensors", ".yaml") {
		sensors[n] = true
	}
	prompts := map[string]bool{}
	for _, n := range names(t, "prompts", ".md") {
		prompts[n] = true
	}
	var cfg struct {
		Runtimes map[string]any `yaml:"runtimes"`
	}
	raw, err := FS.ReadFile("config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}

	for id, f := range loadFlows(t) {
		if f.Enabled != (id == "demo") {
			t.Errorf("%s: enabled = %v; only demo ships enabled", id, f.Enabled)
		}
		for _, s := range f.Subscribe {
			if !sensors[s] {
				t.Errorf("%s subscribes to unknown sensor %q", id, s)
			}
		}
		nodes := map[string]nodeDef{}
		hasAgent := false
		for _, n := range f.Nodes {
			nodes[n.ID] = n
			if n.Type == "agent" {
				hasAgent = true
				if _, ok := cfg.Runtimes[n.Runtime]; !ok {
					t.Errorf("%s/%s: runtime %q not in config.yaml", id, n.ID, n.Runtime)
				}
				if !prompts[n.Prompt] {
					t.Errorf("%s/%s: unknown prompt %q", id, n.ID, n.Prompt)
				}
				if n.Mode != "readonly" {
					t.Errorf("%s/%s: starter agents run readonly, got %q", id, n.ID, n.Mode)
				}
			}
		}
		if hasAgent && !f.DryRun {
			t.Errorf("%s has agent nodes and must ship with dry_run: true", id)
		}
		incoming := map[string]bool{}
		for _, e := range f.Edges {
			from, ok := nodes[e.From]
			if !ok || nodes[e.To].ID == "" {
				t.Errorf("%s: edge %+v references an unknown node", id, e)
				continue
			}
			incoming[e.To] = true
			declared := from.Ports
			if from.Type == "transform" {
				declared = transform.Ports(from.Steps)
			}
			switch {
			case e.Port == "":
				if from.Type != "transform" && len(from.Ports) > 0 {
					t.Errorf("%s: node %s declares ports, edge %+v must name one", id, from.ID, e)
				}
			case e.Port != "error" && e.Port != "denied":
				found := false
				for _, p := range declared {
					found = found || p == e.Port
				}
				if !found {
					t.Errorf("%s: edge %+v uses undeclared port", id, e)
				}
			}
		}
		for _, n := range f.Nodes {
			if len(f.Nodes) > 1 && !incoming[n.ID] && n.ID != f.Nodes[0].ID {
				t.Errorf("%s: node %s is unreachable", id, n.ID)
			}
		}
	}
}

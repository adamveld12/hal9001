package main

import (
	"encoding/json"
	"io"
)

func printJSONField(out io.Writer, label string, raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	pf(out, "%s: %s\n", label, prettyJSON(raw))
}

// printInit prints the raw initialize result so no capability field is lost to
// the SDK's typed structs.
func printInit(out io.Writer, raw json.RawMessage) {
	var r map[string]json.RawMessage
	if json.Unmarshal(raw, &r) != nil {
		pf(out, "unparseable initialize result: %s\n", string(raw))
		return
	}
	printJSONField(out, "protocolVersion", r["protocolVersion"])
	printJSONField(out, "agentInfo", r["agentInfo"])
	printJSONField(out, "agentCapabilities", r["agentCapabilities"])
	printJSONField(out, "authMethods", r["authMethods"])
	printJSONField(out, "_meta", r["_meta"])
}

// printSession prints modes, models and config options from a raw
// session/new|load|resume result.
func printSession(out io.Writer, raw json.RawMessage) {
	var r struct {
		Modes *struct {
			CurrentModeID string `json:"currentModeId"`
			Available     []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"availableModes"`
		} `json:"modes"`
		Models *struct {
			CurrentModelID string `json:"currentModelId"`
			Available      []struct {
				ID          string `json:"modelId"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"availableModels"`
		} `json:"models"`
		ConfigOptions []json.RawMessage `json:"configOptions"`
		Meta          json.RawMessage   `json:"_meta"`
	}
	if json.Unmarshal(raw, &r) != nil {
		pf(out, "unparseable session result: %s\n", string(raw))
		return
	}
	if r.Modes != nil {
		pf(out, "modes (current=%s):\n", r.Modes.CurrentModeID)
		for _, m := range r.Modes.Available {
			pf(out, "  - id=%q name=%q description=%q\n", m.ID, m.Name, trunc(m.Description, 120))
		}
	} else {
		pln(out, "modes: none reported")
	}
	if r.Models != nil {
		pf(out, "models (current=%s):\n", r.Models.CurrentModelID)
		for _, m := range r.Models.Available {
			pf(out, "  - id=%q name=%q description=%q\n", m.ID, m.Name, trunc(m.Description, 100))
		}
	} else {
		pln(out, "models: none reported")
	}
	if len(r.ConfigOptions) > 0 {
		pln(out, "configOptions:")
		for _, c := range r.ConfigOptions {
			pf(out, "  - %s\n", trunc(compactJSON(c), 1500))
		}
	} else {
		pln(out, "configOptions: none reported")
	}
	if len(r.Meta) > 0 {
		pf(out, "_meta: %s\n", trunc(compactJSON(r.Meta), 1500))
	}
}

// modelConfigID returns the id of the session config option whose category is
// "model", if the agent exposes model selection that way.
func modelConfigID(raw json.RawMessage) (string, bool) {
	var r struct {
		ConfigOptions []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
		} `json:"configOptions"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "", false
	}
	for _, c := range r.ConfigOptions {
		if c.Category == "model" {
			return c.ID, true
		}
	}
	return "", false
}

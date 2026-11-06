package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func sandboxCmds() []*command {
	return []*command{
		cmd("list", "list the sandbox backends (local, docker, depot, kubernetes, ssh)", apiGet("/api/sandboxes", "backend", "enabled", "lifecycle", "approval", "configured", "valid")),
		cmd("get", "show the config of one backend with its hash and diagnostics", &leaf{usage: "<backend>", minArgs: 1, maxArgs: 1, run: func(ctx context.Context, c *call) error {
			return c.doEmit(ctx, http.MethodGet, "/api/sandboxes/"+esc(c.args[0]), nil, nil)
		}}),
		cmd("set", "change a backend's config: merge a JSON object (file or stdin, e.g. {\"enabled\":true}) over the current config", sandboxSetLeaf()),
		cmd("check", "test the connection of a backend (docker ping, kubectl version, ssh connect + host key fingerprint, depot list, local dir writable)", &leaf{usage: "<backend>", minArgs: 1, maxArgs: 1, run: func(ctx context.Context, c *call) error {
			return c.doEmit(ctx, http.MethodPost, "/api/sandboxes/"+esc(c.args[0])+"/check", nil, nil)
		}}),
		group("env", "list, set and unset a backend's own env variables, including its credentials (DEPOT_TOKEN, SSH_PASSWORD, SSH_PRIVATE_KEY; values are secret and write-only unless set with --plain)", envCmds("sandboxes")...),
	}
}

func sandboxSetLeaf() *leaf {
	return &leaf{usage: "<backend> [file|-]", minArgs: 1, maxArgs: 2, run: func(ctx context.Context, c *call) error {
		path := "/api/sandboxes/" + esc(c.args[0])
		name := "-"
		if len(c.args) == 2 {
			name = c.args[1]
		}
		in, err := c.readInput(name)
		if err != nil {
			return err
		}
		var patch map[string]any
		if err := json.Unmarshal(in, &patch); err != nil {
			return usagef("sandboxes set: the input must be a JSON object: %v", err)
		}
		resp, err := c.client().do(ctx, http.MethodGet, path, nil, nil)
		if err != nil {
			return err
		}
		var cur struct {
			Hash   string         `json:"hash"`
			Config map[string]any `json:"config"`
		}
		if err := json.Unmarshal(resp.Body, &cur); err != nil {
			return fmt.Errorf("unexpected response: %s", strings.TrimSpace(string(resp.Body)))
		}
		for k, v := range patch {
			cur.Config[k] = v
		}
		body, _ := jsonBody(map[string]any{"expected_hash": cur.Hash, "config": cur.Config})
		return c.doEmit(ctx, http.MethodPut, path, nil, body)
	}}
}

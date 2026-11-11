package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"hal9001/internal/runtimes"
)

// installPollInterval is how often `runtimes install` asks the daemon whether the install has finished.
var installPollInterval = time.Second

// runtimeItem is the part of an item of GET /api/runtimes the status output uses.
type runtimeItem struct {
	Name      string             `json:"name"`
	Kind      *string            `json:"kind"`
	Detection runtimes.Detection `json:"detection"`
}

func runtimeStatusCmds() []*command {
	return []*command{
		cmd("status", "show what hal9001 found for each runtime: CLI, login, adapter, Node.js and warnings", runtimeReport(http.MethodGet, "/api/runtimes")),
		cmd("detect", "check the CLIs, logins, adapters and Node.js again, then show the result", runtimeReport(http.MethodPost, "/api/runtimes/detect")),
		cmd("install", "install the ACP adapter of a runtime with npm and wait for it", installAdapterLeaf()),
		cmd("uninstall", "remove the installed ACP adapter of a runtime", uninstallAdapterLeaf()),
	}
}

// runtimeReport sends the request and prints the runtimes: JSON with --json, else a readable summary.
func runtimeReport(method, path string) *leaf {
	var asJSON bool
	return &leaf{
		maxArgs: 0,
		flags:   func(fs *flag.FlagSet) { fs.BoolVar(&asJSON, "json", false, "print the API response as JSON") },
		run: func(ctx context.Context, c *call) error {
			resp, err := c.client().do(ctx, method, path, nil, nil)
			if err != nil {
				return err
			}
			if asJSON {
				return c.emit(resp)
			}
			var list struct {
				Items []runtimeItem `json:"items"`
			}
			if err := json.Unmarshal(resp.Body, &list); err != nil {
				return fmt.Errorf("decode runtimes: %w", err)
			}
			for i, it := range list.Items {
				if i > 0 {
					_, _ = fmt.Fprintln(c.stdout())
				}
				writeRuntimeStatus(c.stdout(), it)
			}
			return nil
		},
	}
}

func writeRuntimeStatus(w io.Writer, it runtimeItem) {
	d := it.Detection
	_, _ = fmt.Fprintf(w, "%s: %s\n", it.Name, strings.ToUpper(d.Status))
	cli := "not found"
	if d.CLI.Found {
		cli = deref(d.CLI.Path)
		if d.CLI.Version != nil {
			cli = d.CLI.Name + " " + *d.CLI.Version + " (" + cli + ")"
		}
	}
	_, _ = fmt.Fprintf(w, "  cli      %s\n", cli)
	_, _ = fmt.Fprintf(w, "  login    %s\n", d.Auth.Status)
	adapter := d.Adapter.Source
	if d.Adapter.Installed {
		adapter += ", installed"
		if d.Adapter.Version != nil {
			adapter += " " + *d.Adapter.Version
		}
	} else {
		adapter += ", not installed"
		if d.Adapter.PinnedVersion != nil {
			adapter += " (hal9001 installs " + *d.Adapter.PinnedVersion + ")"
		}
	}
	if d.Adapter.Installing {
		adapter += ", installing"
	}
	_, _ = fmt.Fprintf(w, "  adapter  %s\n", adapter)
	node := "not found"
	if d.Node.Found {
		node = deref(d.Node.Version)
		if !d.Node.OK {
			node += " (needs 22 or newer)"
		}
	}
	_, _ = fmt.Fprintf(w, "  node     %s\n", node)
	if len(d.Command) > 0 {
		_, _ = fmt.Fprintf(w, "  command  %s\n", strings.Join(d.Command, " "))
	}
	for _, wn := range d.Warnings {
		_, _ = fmt.Fprintf(w, "  warning  %s: %s\n", wn.Code, wn.Message)
		if wn.Command != nil {
			_, _ = fmt.Fprintf(w, "           run: %s\n", *wn.Command)
		}
		if wn.URL != nil {
			_, _ = fmt.Fprintf(w, "           see: %s\n", *wn.URL)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func installAdapterLeaf() *leaf {
	var asJSON bool
	return &leaf{
		usage: "<name>", minArgs: 1, maxArgs: 1,
		flags: func(fs *flag.FlagSet) { fs.BoolVar(&asJSON, "json", false, "print the final runtime as JSON") },
		run: func(ctx context.Context, c *call) error {
			name := c.args[0]
			if _, err := c.client().do(ctx, http.MethodPost, "/api/runtimes/"+esc(name)+"/adapter", nil, nil); err != nil {
				return err
			}
			if !asJSON {
				_, _ = fmt.Fprintf(c.stdout(), "installing the ACP adapter for %s ...\n", name)
			}
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(installPollInterval):
				}
				resp, err := c.client().do(ctx, http.MethodGet, "/api/runtimes", nil, nil)
				if err != nil {
					return err
				}
				var list struct {
					Items []runtimeItem `json:"items"`
				}
				if err := json.Unmarshal(resp.Body, &list); err != nil {
					return fmt.Errorf("decode runtimes: %w", err)
				}
				for _, it := range list.Items {
					if it.Name != name || it.Detection.Adapter.Installing {
						continue
					}
					return finishInstall(c, it, resp, asJSON)
				}
			}
		},
	}
}

// finishInstall reports the end of an install: success, or the adapter_install_failed warning as an error.
func finishInstall(c *call, it runtimeItem, resp *response, asJSON bool) error {
	for _, w := range it.Detection.Warnings {
		if w.Code == runtimes.CodeAdapterInstallFailed {
			return errors.New(w.Message)
		}
	}
	if !it.Detection.Adapter.Installed {
		return fmt.Errorf("the adapter of %s is not installed", it.Name)
	}
	if asJSON {
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(resp.Body, &list)
		for _, raw := range list.Items {
			var n struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &n) == nil && n.Name == it.Name {
				_, err := fmt.Fprintln(c.stdout(), string(raw))
				return err
			}
		}
		return nil
	}
	_, _ = fmt.Fprintf(c.stdout(), "installed the ACP adapter for %s (version %s)\n", it.Name, deref(it.Detection.Adapter.Version))
	for _, w := range it.Detection.Warnings {
		_, _ = fmt.Fprintf(c.stdout(), "warning: %s\n", w.Message)
	}
	return nil
}

func uninstallAdapterLeaf() *leaf {
	var asJSON bool
	return &leaf{
		usage: "<name>", minArgs: 1, maxArgs: 1,
		flags: func(fs *flag.FlagSet) { fs.BoolVar(&asJSON, "json", false, "print the runtime as JSON") },
		run: func(ctx context.Context, c *call) error {
			resp, err := c.client().do(ctx, http.MethodDelete, "/api/runtimes/"+esc(c.args[0])+"/adapter", nil, nil)
			if err != nil {
				return err
			}
			if asJSON {
				return c.emit(resp)
			}
			_, err = fmt.Fprintf(c.stdout(), "removed the ACP adapter for %s\n", c.args[0])
			return err
		},
	}
}

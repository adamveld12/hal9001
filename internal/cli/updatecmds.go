package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"text/tabwriter"
)

// updateStatus is the part of GET /api/update the CLI prints.
type updateStatus struct {
	Enabled        bool    `json:"enabled"`
	DisabledReason *string `json:"disabled_reason"`
	Current        struct {
		Version string `json:"version"`
		Build   int    `json:"build"`
	} `json:"current"`
	Channel     string `json:"channel"`
	AutoInstall bool   `json:"auto_install_when_idle"`
	State       string `json:"state"`
	Latest      *struct {
		Version  string `json:"version"`
		Build    int    `json:"build"`
		NotesURL string `json:"notes_url"`
	} `json:"latest"`
	Available bool    `json:"available"`
	Pending   bool    `json:"pending"`
	Phase     *string `json:"phase"`
	Busy      struct {
		RunningRuns int `json:"running_runs"`
	} `json:"busy"`
	InstallerConnected bool    `json:"installer_connected"`
	LastCheckedAt      *string `json:"last_checked_at"`
	Error              *string `json:"error"`
}

func updateGroup() *command {
	g := group("update", "show, check and install app updates (macOS app)",
		cmd("status", "show the update state", updateLeaf(http.MethodGet, "/api/update", false)),
		cmd("check", "check the update feed now", updateLeaf(http.MethodPost, "/api/update/check", false)),
		cmd("install", "install the available update when no flow is running (never forces)", updateLeaf(http.MethodPost, "/api/update/install", true)),
	)
	g.def = updateLeaf(http.MethodGet, "/api/update", false)
	return g
}

func updateLeaf(method, path string, install bool) *leaf {
	var asJSON bool
	return &leaf{
		usage: "[--json]",
		flags: func(fs *flag.FlagSet) { fs.BoolVar(&asJSON, "json", false, "print the raw JSON") },
		run: func(ctx context.Context, c *call) error {
			var body []byte
			if install {
				body = []byte(`{"force":false}`)
			}
			resp, err := c.client().do(ctx, method, path, nil, body)
			if err != nil {
				if install {
					if p, ok := asProblem(err); ok && p.Status == http.StatusConflict {
						var extra struct {
							State      string `json:"current_state"`
							ReleaseURL string `json:"release_url"`
						}
						_ = json.Unmarshal(p.Raw, &extra)
						if extra.ReleaseURL != "" {
							return errors.New(p.Detail + "\nDownload: " + extra.ReleaseURL)
						}
						return errors.New(p.Detail)
					}
				}
				return err
			}
			if asJSON {
				return writeJSON(c.stdout(), resp.Body)
			}
			var s updateStatus
			if err := json.Unmarshal(resp.Body, &s); err != nil {
				return fmt.Errorf("unexpected response: %w", err)
			}
			return printUpdate(c, s, install)
		},
	}
}

func printUpdate(c *call, s updateStatus, install bool) error {
	w := c.stdout()
	if install {
		switch {
		case s.Pending:
			_, _ = fmt.Fprintf(w, "pending until flows finish (%d running)\n", s.Busy.RunningRuns)
		case s.State == "installing":
			_, _ = fmt.Fprintln(w, "installing; HAL9001 will restart")
		default:
			_, _ = fmt.Fprintf(w, "state: %s\n", s.State)
		}
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	row := func(k, v string) { _, _ = fmt.Fprintf(tw, "%s\t%s\n", k, v) }
	row("state", s.State)
	if s.State == "installing" && s.Phase != nil {
		row("phase", *s.Phase)
	}
	if !s.Enabled && s.DisabledReason != nil {
		row("disabled", *s.DisabledReason)
	}
	row("current", fmt.Sprintf("%s (build %d)", s.Current.Version, s.Current.Build))
	row("channel", s.Channel)
	row("auto-install", fmt.Sprint(s.AutoInstall))
	if s.Latest != nil {
		row("latest", fmt.Sprintf("%s (build %d)", s.Latest.Version, s.Latest.Build))
		if s.Latest.NotesURL != "" {
			row("notes", s.Latest.NotesURL)
		}
	}
	row("update available", fmt.Sprint(s.Available))
	row("installer app connected", fmt.Sprint(s.InstallerConnected))
	if s.LastCheckedAt != nil {
		row("last checked", *s.LastCheckedAt)
	}
	if s.Error != nil {
		row("error", *s.Error)
	}
	return tw.Flush()
}

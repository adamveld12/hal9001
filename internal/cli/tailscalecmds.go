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
)

// tailscaleStatus is the part of GET /api/tailscale the summary shows.
type tailscaleStatus struct {
	CLI struct {
		Found   bool    `json:"found"`
		Path    *string `json:"path"`
		Version *string `json:"version"`
	} `json:"cli"`
	BackendState *string `json:"backend_state"`
	LoggedIn     bool    `json:"logged_in"`
	DNSName      *string `json:"dns_name"`
	Tailnet      *string `json:"tailnet"`
	HTTPSEnabled bool    `json:"https_enabled"`
	Serve        struct {
		Enabled      bool    `json:"enabled"`
		HTTPSPort    int     `json:"https_port"`
		Target       string  `json:"target"`
		Active       bool    `json:"active"`
		PointsAtHal  bool    `json:"points_at_hal"`
		Conflict     *string `json:"conflict"`
		Funnel       bool    `json:"funnel"`
		FunnelOnPort bool    `json:"funnel_on_port"`
	} `json:"serve"`
	URL         *string `json:"url"`
	AllowedHost struct {
		Entry      *string `json:"entry"`
		Present    bool    `json:"present"`
		AddedByHal bool    `json:"added_by_hal"`
	} `json:"allowed_host"`
	AllowedLogins []string `json:"allowed_logins"`
	Warnings      []struct {
		Code    string  `json:"code"`
		Message string  `json:"message"`
		Command *string `json:"command"`
		URL     *string `json:"url"`
	} `json:"warnings"`
	Error *string `json:"error"`
}

func tailscaleCmds() []*command {
	return []*command{
		cmd("status", "show what hal9001 found about Tailscale: CLI, sign-in, machine name, certificates, the serve mapping, allowed_hosts (--table: readable summary)", tailscaleStatusLeaf()),
		cmd("serve", "share hal9001 on your tailnet (tailscale serve) and add this Mac's name to allowed_hosts; tailnet only, never funnel", tailscaleServeLeaf()),
		cmd("unserve", "stop sharing: remove hal9001's tailscale serve mapping and the allowed_hosts entry hal9001 added", tailscaleUnserveLeaf()),
	}
}

func tailscaleStatusLeaf() *leaf {
	return &leaf{maxArgs: 0, run: func(ctx context.Context, c *call) error {
		return tailscaleEmit(ctx, c, http.MethodGet, "/api/tailscale", nil)
	}}
}

func tailscaleServeLeaf() *leaf {
	var port int
	var replace bool
	return &leaf{
		maxArgs: 0,
		flags: func(fs *flag.FlagSet) {
			fs.IntVar(&port, "port", 0, "tailnet HTTPS port (default: tailscale.https_port of config.yaml, 443)")
			fs.BoolVar(&replace, "replace", false, "overwrite what the HTTPS port serves now when it is not hal9001")
		},
		run: func(ctx context.Context, c *call) error {
			body := map[string]any{}
			if c.flagSet("port") {
				body["https_port"] = port
			}
			if replace {
				body["replace"] = true
			}
			b, err := jsonBody(body)
			if err != nil {
				return err
			}
			err = tailscaleEmit(ctx, c, http.MethodPost, "/api/tailscale/serve", b)
			var p *problemError
			if errors.As(err, &p) && strings.HasSuffix(p.Type, ":already-exists") {
				_, _ = fmt.Fprintln(c.stderr(), "hal9001: use --replace to overwrite it")
			}
			return err
		},
	}
}

func tailscaleUnserveLeaf() *leaf {
	return &leaf{maxArgs: 0, run: func(ctx context.Context, c *call) error {
		return tailscaleEmit(ctx, c, http.MethodDelete, "/api/tailscale/serve", nil)
	}}
}

// tailscaleEmit sends the request and prints the status it returns: JSON, or a summary with --table.
func tailscaleEmit(ctx context.Context, c *call, method, path string, body []byte) error {
	resp, err := c.client().do(ctx, method, path, nil, body)
	if err != nil {
		return err
	}
	if !c.table {
		return c.emit(resp)
	}
	var st tailscaleStatus
	if err := json.Unmarshal(resp.Body, &st); err != nil {
		return fmt.Errorf("decode tailscale status: %w", err)
	}
	writeTailscaleStatus(c.stdout(), st)
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func writeTailscaleStatus(w io.Writer, st tailscaleStatus) {
	p := func(label, value string) { _, _ = fmt.Fprintf(w, "%-14s %s\n", label, value) }
	cli := "not found"
	if st.CLI.Found {
		cli = deref(st.CLI.Path)
		if st.CLI.Version != nil {
			cli += " (" + *st.CLI.Version + ")"
		}
	}
	p("cli", cli)
	p("signed in", yesNo(st.LoggedIn)+stateSuffix(st.BackendState))
	if st.DNSName != nil {
		name := *st.DNSName
		if st.Tailnet != nil {
			name += " on " + *st.Tailnet
		}
		p("machine", name)
		p("https certs", yesNo(st.HTTPSEnabled))
	}
	serve := "off"
	switch {
	case st.Serve.PointsAtHal:
		serve = fmt.Sprintf("on, https port %d -> %s", st.Serve.HTTPSPort, st.Serve.Target)
	case st.Serve.Conflict != nil:
		serve = fmt.Sprintf("https port %d serves %s", st.Serve.HTTPSPort, *st.Serve.Conflict)
	case st.Serve.Enabled:
		serve = "set to on, but no mapping exists"
	}
	p("sharing", serve)
	if st.URL != nil && st.Serve.PointsAtHal {
		p("url", *st.URL)
	}
	if st.AllowedHost.Entry != nil {
		host := *st.AllowedHost.Entry + ": " + map[bool]string{true: "in allowed_hosts", false: "not in allowed_hosts"}[st.AllowedHost.Present]
		if st.AllowedHost.AddedByHal {
			host += " (added by hal9001)"
		}
		p("allowed host", host)
	}
	p("allowed logins", strings.Join(st.AllowedLogins, ", "))
	if st.Serve.FunnelOnPort {
		p("funnel", fmt.Sprintf("ON for https port %d: exposed to the internet; hal9001 will not share on it (run: tailscale funnel --https=%d off)", st.Serve.HTTPSPort, st.Serve.HTTPSPort))
	} else if st.Serve.Funnel {
		p("funnel", "ON: something is exposed to the internet")
	}
	if st.Error != nil {
		p("error", *st.Error)
	}
	for _, wn := range st.Warnings {
		_, _ = fmt.Fprintf(w, "warning        %s: %s\n", wn.Code, wn.Message)
		if wn.Command != nil {
			_, _ = fmt.Fprintf(w, "               run: %s\n", *wn.Command)
		}
		if wn.URL != nil {
			_, _ = fmt.Fprintf(w, "               see: %s\n", *wn.URL)
		}
	}
}

func stateSuffix(s *string) string {
	if s == nil {
		return ""
	}
	return " (" + *s + ")"
}

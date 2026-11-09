package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
)

const label = "dev.hal9001.menubar"

var plistTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.Exe}}</string>
{{- if .URL}}
		<string>--url</string>
		<string>{{.URL}}</string>
{{- end}}
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`))

func renderPlist(exe, url string) (string, error) {
	var b bytes.Buffer
	err := plistTmpl.Execute(&b, map[string]string{"Label": label, "Exe": html.EscapeString(exe), "URL": html.EscapeString(url)})
	return b.String(), err
}

// runInstall writes ~/Library/LaunchAgents/dev.hal9001.menubar.plist and loads it. --dry-run prints the plist only.
func runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "print the LaunchAgent plist instead of installing it")
	url := fs.String("url", os.Getenv("HAL9001_URL"), "daemon base URL to bake into the agent (default: built-in)")
	_ = fs.Parse(args)

	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		return 1
	}
	plist, err := renderPlist(exe, *url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		return 1
	}
	if *dry {
		fmt.Print(plist)
		return 0
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		return 1
	}
	path := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		return 1
	}
	if err := os.WriteFile(path, []byte(plist), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		return 1
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	//nolint:gosec // fixed launchctl arguments; domain is built from our own uid
	_ = exec.CommandContext(context.Background(), "launchctl", "bootout", domain+"/"+label).Run() // ignore: not loaded yet
	//nolint:gosec // see above
	if out, err := exec.CommandContext(context.Background(), "launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "wrote %s but launchctl failed: %v\n%s", path, err, out)
		return 1
	}
	fmt.Println("installed", path)
	return 0
}

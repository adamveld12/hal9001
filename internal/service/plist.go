package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
)

// Agent describes a launchd LaunchAgent running `hal9001 serve`: started at load and restarted when it exits.
type Agent struct {
	Label string
	// BundleProgram is the executable relative to the app bundle (bundled agents registered through SMAppService).
	// Empty for a plain agent, whose executable is Args[0].
	BundleProgram string
	// Args is ProgramArguments; with BundleProgram only Args[0] (argv[0]) is not the path of the executable.
	Args []string
	Env  map[string]string
	// AssociatedBundleIDs ties a bundled agent to its app in System Settings.
	AssociatedBundleIDs []string
	// LogPath, when set, receives stdout and stderr from launchd itself (absolute path; launchd does not expand ~).
	LogPath string
}

// BundleAgent is the daemon agent inside the app bundle of profile p. launchd cannot expand ~ or know the user, so the
// daemon itself opens its log file (EnvLogFile) when it sees EnvLaunchd.
func BundleAgent(p Profile) Agent {
	env := map[string]string{
		EnvLaunchd: "1",
		EnvLogFile: p.LogFile("~"), // the daemon expands the tilde
	}
	if p.Home != "" {
		env["HAL9001_HOME"] = p.Home
	}
	if p.Port != 0 {
		env[EnvDevPort] = strconv.Itoa(p.Port)
	}
	return Agent{
		Label:               p.Label(),
		BundleProgram:       BundleProgram,
		Args:                []string{"hal9001", "serve"},
		Env:                 env,
		AssociatedBundleIDs: []string{p.BundleID},
	}
}

// HeadlessAgent is the daemon-only agent for exe (the absolute path of the hal9001 binary) serving home.
func HeadlessAgent(exe, home, userHome string) Agent {
	log := LogFile(userHome)
	return Agent{
		Label:   HeadlessLabel,
		Args:    []string{exe, "serve", "--home", home},
		Env:     map[string]string{EnvLaunchd: "1", EnvLogFile: log},
		LogPath: log,
	}
}

// Plist renders the agent as an XML property list.
func (a Agent) Plist() []byte {
	var b bytes.Buffer
	str := func(indent, s string) { fmt.Fprintf(&b, "%s<string>%s</string>\n", indent, xmlEscape(s)) }
	key := func(indent, k string) { fmt.Fprintf(&b, "%s<key>%s</key>\n", indent, k) }
	strs := func(items []string) {
		b.WriteString("\t<array>\n")
		for _, s := range items {
			str("\t\t", s)
		}
		b.WriteString("\t</array>\n")
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	key("\t", "Label")
	str("\t", a.Label)
	if a.BundleProgram != "" {
		key("\t", "BundleProgram")
		str("\t", a.BundleProgram)
	}
	key("\t", "ProgramArguments")
	strs(a.Args)
	if len(a.Env) > 0 {
		key("\t", "EnvironmentVariables")
		b.WriteString("\t<dict>\n")
		names := make([]string, 0, len(a.Env))
		for k := range a.Env {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			key("\t\t", k)
			str("\t\t", a.Env[k])
		}
		b.WriteString("\t</dict>\n")
	}
	key("\t", "RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("\t", "KeepAlive")
	b.WriteString("\t<true/>\n")
	if len(a.AssociatedBundleIDs) > 0 {
		key("\t", "AssociatedBundleIdentifiers")
		strs(a.AssociatedBundleIDs)
	}
	if a.LogPath != "" {
		key("\t", "StandardOutPath")
		str("\t", a.LogPath)
		key("\t", "StandardErrorPath")
		str("\t", a.LogPath)
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

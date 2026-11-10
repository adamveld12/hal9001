package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBundleWritesInfoPlistAndMatchingAgentPlist(t *testing.T) {
	app := filepath.Join(t.TempDir(), "HAL9001.app")
	if err := runBundle([]string{"-app", app, "-version", "1.2.3", "-build", "42", "-full-version", "v1.2.3-4-gabc & co"}); err != nil {
		t.Fatal(err)
	}
	info := readFile(t, app, "Contents", "Info.plist")
	for _, want := range []string{
		"<key>CFBundleIdentifier</key><string>dev.hal9001</string>",
		"<key>CFBundleExecutable</key><string>HAL9001</string>",
		"<key>CFBundleShortVersionString</key><string>1.2.3</string>",
		"<key>HAL9001Version</key><string>v1.2.3-4-gabc &amp; co</string>",
		"<key>LSUIElement</key><true/>",
		"<key>LSMinimumSystemVersion</key><string>13.0</string>",
	} {
		if !strings.Contains(info, want) {
			t.Errorf("Info.plist lacks %q:\n%s", want, info)
		}
	}
	if strings.Contains(info, "HAL9001DevHome") || strings.Contains(info, "HAL9001DevPort") {
		t.Error("a release build must not carry dev overrides")
	}
	agent := readFile(t, app, "Contents", "Library", "LaunchAgents", "dev.hal9001.daemon.plist")
	for _, want := range []string{"<string>dev.hal9001.daemon</string>", "<key>BundleProgram</key>\n\t<string>Contents/Resources/hal9001</string>"} {
		if !strings.Contains(agent, want) {
			t.Errorf("agent plist lacks %q:\n%s", want, agent)
		}
	}
}

func TestDevBundleGetsItsOwnAgentAndOverrides(t *testing.T) {
	app := filepath.Join(t.TempDir(), "HAL9001 Dev.app")
	err := runBundle([]string{"-app", app, "-bundle-id", "dev.hal9001.dev", "-name", "HAL9001 Dev", "-dev-home", "/tmp/hal-dev", "-dev-port", "19001"})
	if err != nil {
		t.Fatal(err)
	}
	info := readFile(t, app, "Contents", "Info.plist")
	for _, want := range []string{"<key>CFBundleIdentifier</key><string>dev.hal9001.dev</string>", "<key>HAL9001DevHome</key><string>/tmp/hal-dev</string>", "<key>HAL9001DevPort</key><string>19001</string>"} {
		if !strings.Contains(info, want) {
			t.Errorf("Info.plist lacks %q:\n%s", want, info)
		}
	}
	agent := readFile(t, app, "Contents", "Library", "LaunchAgents", "dev.hal9001.dev.daemon.plist")
	for _, want := range []string{"<string>dev.hal9001.dev.daemon</string>", "<key>HAL9001_DEV_PORT</key>\n\t\t<string>19001</string>", "<key>HAL9001_HOME</key>\n\t\t<string>/tmp/hal-dev</string>"} {
		if !strings.Contains(agent, want) {
			t.Errorf("agent plist lacks %q:\n%s", want, agent)
		}
	}
}

func TestReleaseBundleRefusesDevOverrides(t *testing.T) {
	app := filepath.Join(t.TempDir(), "HAL9001.app")
	if err := runBundle([]string{"-app", app, "-dev-port", "19001"}); err == nil {
		t.Fatal("a release bundle must not accept a pinned port")
	}
	if _, err := os.Stat(app); !os.IsNotExist(err) {
		t.Errorf("nothing may be written on refusal: %v", err)
	}
}

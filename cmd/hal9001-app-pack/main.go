// Command hal9001-app-pack writes the generated parts of HAL9001.app: the icon, Info.plist and the bundled daemon
// LaunchAgent plist. scripts/build-app.sh runs it and copies the binaries in and signs the bundle.
//
//	hal9001-app-pack icon -o AppIcon.icns
//	hal9001-app-pack dmg-background -dir stage/.background   (writes background.png and background@2x.png)
//	hal9001-app-pack bundle -app HAL9001.app -bundle-id dev.hal9001 -name HAL9001 -version 0.1.0 -build 42 -full-version v0.1.0-3-gabc
package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"hal9001/internal/menubar"
	"hal9001/internal/service"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: hal9001-app-pack icon|bundle [flags]")
	}
	var err error
	switch os.Args[1] {
	case "icon":
		err = runIcon(os.Args[2:])
	case "dmg-background":
		err = runDMGBackground(os.Args[2:])
	case "bundle":
		err = runBundle(os.Args[2:])
	default:
		fail("unknown command " + os.Args[1])
	}
	if err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "hal9001-app-pack:", msg)
	os.Exit(1)
}

// iconset lists the files an .iconset directory needs and the pixel size of each.
var iconset = []struct {
	name string
	size int
}{
	{"icon_16x16.png", 16}, {"icon_16x16@2x.png", 32},
	{"icon_32x32.png", 32}, {"icon_32x32@2x.png", 64},
	{"icon_128x128.png", 128}, {"icon_128x128@2x.png", 256},
	{"icon_256x256.png", 256}, {"icon_256x256@2x.png", 512},
	{"icon_512x512.png", 512}, {"icon_512x512@2x.png", 1024},
}

// runIcon renders the iconset from the eye renderer and converts it with iconutil.
func runIcon(args []string) error {
	fs := flag.NewFlagSet("icon", flag.ExitOnError)
	out := fs.String("o", "", "output .icns file")
	_ = fs.Parse(args)
	if *out == "" {
		return fmt.Errorf("icon: -o is required")
	}
	dir, err := os.MkdirTemp("", "hal9001-iconset-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	set := filepath.Join(dir, "AppIcon.iconset")
	if err := os.Mkdir(set, 0o750); err != nil {
		return err
	}
	rendered := map[int][]byte{}
	for _, f := range iconset {
		png, ok := rendered[f.size]
		if !ok {
			png = menubar.AppIcon(f.size)
			rendered[f.size] = png
		}
		if err := os.WriteFile(filepath.Join(set, f.name), png, 0o600); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		return err
	}
	if b, err := exec.CommandContext(context.Background(), "iconutil", "-c", "icns", "-o", *out, set).CombinedOutput(); err != nil { //nolint:gosec // fixed binary, paths from our own temp dir and flag
		return fmt.Errorf("iconutil: %w: %s", err, b)
	}
	return nil
}

// bundleInfo is what Info.plist is generated from.
type bundleInfo struct {
	BundleID    string
	Name        string // CFBundleName and display name, e.g. "HAL9001 Dev"
	Executable  string
	Version     string // CFBundleShortVersionString: dotted numbers
	Build       string // CFBundleVersion
	FullVersion string // free-form `git describe`
	DevHome     string // dev builds only
	DevPort     int    // dev builds only
}

// infoPlist renders Info.plist. LSUIElement keeps the app out of the Dock and the app switcher: it lives in the menubar.
func infoPlist(b bundleInfo) []byte {
	var out bytes.Buffer
	str := func(k, v string) {
		var ev bytes.Buffer
		_ = xml.EscapeText(&ev, []byte(v))
		fmt.Fprintf(&out, "\t<key>%s</key><string>%s</string>\n", k, ev.String())
	}
	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	str("CFBundleIdentifier", b.BundleID)
	str("CFBundleName", b.Name)
	str("CFBundleDisplayName", b.Name)
	str("CFBundleExecutable", b.Executable)
	str("CFBundleIconFile", "AppIcon")
	str("CFBundlePackageType", "APPL")
	str("CFBundleShortVersionString", b.Version)
	str("CFBundleVersion", b.Build)
	str("HAL9001Version", b.FullVersion)
	str("LSMinimumSystemVersion", "13.0")
	out.WriteString("\t<key>LSUIElement</key><true/>\n\t<key>NSHighResolutionCapable</key><true/>\n")
	if b.DevHome != "" {
		str("HAL9001DevHome", b.DevHome)
	}
	if b.DevPort != 0 {
		str("HAL9001DevPort", strconv.Itoa(b.DevPort))
	}
	out.WriteString("</dict>\n</plist>\n")
	return out.Bytes()
}

// runBundle writes Contents/Info.plist and the bundled agent plist into a .app directory.
func runBundle(args []string) error {
	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	app := fs.String("app", "", "the .app directory to write into")
	id := fs.String("bundle-id", service.ReleaseBundleID, "CFBundleIdentifier; anything but dev.hal9001 makes a dev build")
	name := fs.String("name", "HAL9001", "CFBundleName")
	exe := fs.String("executable", "HAL9001", "CFBundleExecutable")
	version := fs.String("version", "0.0.0", "CFBundleShortVersionString (dotted numbers)")
	build := fs.String("build", "1", "CFBundleVersion")
	full := fs.String("full-version", "dev", "git describe output")
	devHome := fs.String("dev-home", "", "dev builds: daemon home directory the bundled service uses (default ~/.hal9001)")
	devPort := fs.Int("dev-port", 0, "dev builds: daemon port the bundled service uses (default 9001)")
	_ = fs.Parse(args)
	if *app == "" {
		return fmt.Errorf("bundle: -app is required")
	}
	p := service.Profile{BundleID: *id, Home: *devHome, Port: *devPort}
	if !p.Dev() && (p.Home != "" || p.Port != 0) {
		return fmt.Errorf("bundle: -dev-home and -dev-port are only for dev builds (bundle id other than %s)", service.ReleaseBundleID)
	}
	info := bundleInfo{BundleID: *id, Name: *name, Executable: *exe, Version: *version, Build: *build, FullVersion: *full, DevHome: p.Home, DevPort: p.Port}

	files := map[string][]byte{
		filepath.Join(*app, "Contents", "Info.plist"):                             infoPlist(info),
		filepath.Join(*app, "Contents", "Library", "LaunchAgents", p.PlistName()): service.BundleAgent(p).Plist(),
	}
	for path, data := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // plists in a bundle are world-readable
			return err
		}
	}
	return nil
}

// runDMGBackground writes the installer window background (1x and 2x) into -dir.
func runDMGBackground(args []string) error {
	fs := flag.NewFlagSet("dmg-background", flag.ExitOnError)
	dir := fs.String("dir", "", "output directory")
	_ = fs.Parse(args)
	if *dir == "" {
		return fmt.Errorf("dmg-background: -dir is required")
	}
	if err := os.MkdirAll(*dir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*dir, "background.png"), menubar.DMGBackground(1), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*dir, "background@2x.png"), menubar.DMGBackground(2), 0o600)
}

package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CLIInstall is the outcome of InstallCLI.
type CLIInstall struct {
	Link   string // the symlink
	Target string // what it points at
	// Changed is false when the symlink already pointed at Target.
	Changed bool
	// OnPath reports whether the directory of Link is on the PATH given to InstallCLI.
	OnPath bool
}

// CLIDir is the directory the command-line tool is linked into.
func CLIDir(userHome string) string { return filepath.Join(userHome, ".local", "bin") }

// InstallCLI symlinks ~/.local/bin/<name> to target (the hal9001 binary inside the app bundle). An existing symlink is
// repointed; an existing regular file is never overwritten. pathEnv is the PATH a terminal would have (the app itself
// runs with launchd's minimal one); it only decides CLIInstall.OnPath.
func InstallCLI(userHome, name, target, pathEnv string) (CLIInstall, error) {
	dir := CLIDir(userHome)
	res := CLIInstall{Link: filepath.Join(dir, name), Target: target, OnPath: onPath(pathEnv, dir)}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // an ordinary user bin directory
		return res, err
	}
	switch st, err := os.Lstat(res.Link); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return res, err
	case st.Mode()&fs.ModeSymlink == 0:
		return res, fmt.Errorf("%s exists and is not a symlink; remove or rename it, then try again", res.Link)
	default:
		cur, err := os.Readlink(res.Link)
		if err != nil {
			return res, err
		}
		if cur == target {
			return res, nil
		}
		if err := os.Remove(res.Link); err != nil {
			return res, err
		}
	}
	if err := os.Symlink(target, res.Link); err != nil {
		return res, err
	}
	res.Changed = true
	return res, nil
}

func onPath(pathEnv, dir string) bool {
	for _, p := range filepath.SplitList(pathEnv) {
		if filepath.Clean(p) == dir {
			return true
		}
	}
	return false
}

// PrintInfo is what `launchctl print <service-target>` reports about a loaded job.
type PrintInfo struct {
	State    string // "running", "waiting", ...
	PID      int    // 0 when not running
	Runs     int
	LastExit string // e.g. "0", "(never exited)"; empty when absent
}

// LaunchRejected reports that launchd refused to start the job: it exited 78 (EX_CONFIG), which is what a registered
// agent whose binary changed since launchd recorded its code requirement does.
func (p PrintInfo) LaunchRejected() bool { return strings.HasPrefix(p.LastExit, "78") }

// ParsePrint extracts the job fields of `launchctl print` output (the first `key = value` line of each wanted key, which
// is the job's own; nested coalition blocks repeat `state`).
func ParsePrint(out string) PrintInfo {
	var info PrintInfo
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "\t\t") {
			continue // only the job's own top-level fields
		}
		k, v, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok || seen[k] {
			continue
		}
		switch k {
		case "state":
			info.State = v
		case "pid":
			info.PID, _ = strconv.Atoi(v)
		case "runs":
			info.Runs, _ = strconv.Atoi(v)
		case "last exit code":
			info.LastExit = v
		default:
			continue
		}
		seen[k] = true
	}
	return info
}

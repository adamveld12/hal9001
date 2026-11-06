package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rawResult is the outcome of one script on a remote backend.
type rawResult struct {
	Stdout, Stderr []byte
	ExitCode       int
	TimedOut       bool
	Truncated      bool
}

// runFunc runs script with `sh -c` on the backend (the backend adds its transport and, where it supports
// it, the per-run env). stdin may be nil. Output streams are cut at limit bytes.
type runFunc func(ctx context.Context, script string, stdin []byte, timeout time.Duration, limit int) (rawResult, error)

// remoteSession implements Session for backends that can only run commands: docker, kubernetes, depot and
// ssh. Files are handled with small POSIX shell scripts; each one first checks that the physical location of
// the target (after resolving symlinks in its directories) is inside the workdir and that the file itself
// is not a symlink.
type remoteSession struct {
	workdir string
	run     runFunc
	// env is exported in front of every Exec command when inlineEnv is set; backends that pass the env
	// natively leave it unset.
	env       map[string]string
	inlineEnv bool
	timeout   time.Duration
	// noStdin: the transport cannot send stdin; file writes then embed base64 in the script.
	noStdin bool
	// b64Read: the transport is not binary safe; file reads go through base64.
	b64Read bool
	closeFn func(ctx context.Context, success bool) error
}

func (s *remoteSession) Workdir() string { return s.workdir }

func (s *remoteSession) Close(ctx context.Context, success bool) error {
	if s.closeFn == nil {
		return nil
	}
	return s.closeFn(ctx, success)
}

func (s *remoteSession) Exec(ctx context.Context, cmd string, stdin []byte, timeout time.Duration) (ExecResult, error) {
	if timeout <= 0 {
		timeout = s.timeout
	}
	script := "cd " + shQuote(s.workdir) + " || exit 1\n"
	if s.inlineEnv {
		script += envExports(s.env) + "\n"
	}
	res, err := s.run(ctx, script+cmd, stdin, timeout, MaxOutput)
	if err != nil {
		return ExecResult{}, err
	}
	return ExecResult{
		Stdout: string(res.Stdout), Stderr: string(res.Stderr), ExitCode: res.ExitCode,
		TimedOut: res.TimedOut, Truncated: res.Truncated,
	}, nil
}

// Exit codes of the file scripts.
const (
	exitNoWorkdir = 90
	exitEscape    = 91
	exitNotFound  = 92
	exitTooLarge  = 93
	exitIsDir     = 94
)

// guard is the common prefix of the file scripts: it sets Wp (physical workdir) and T (target) and exits
// with exitEscape unless T resolves inside Wp.
func (s *remoteSession) guard(rel string) string {
	t := `"$Wp"`
	if rel != "." {
		t = `"$Wp"/` + shQuote(rel)
	}
	return fmt.Sprintf(`Wp=$(cd %s 2>/dev/null && pwd -P) || exit %d
T=%s
d=$T
[ -d "$d" ] || d=$(dirname -- "$T")
while [ ! -d "$d" ]; do d=$(dirname -- "$d"); done
dp=$(cd "$d" && pwd -P)
case "$dp" in "$Wp"|"$Wp"/*) ;; *) exit %d;; esac
[ -L "$T" ] && exit %d
`, shQuote(s.workdir), exitNoWorkdir, t, exitEscape, exitEscape)
}

func (s *remoteSession) rel(p string) (string, error) { return relPath(s.workdir, p) }

func (s *remoteSession) script(ctx context.Context, script string, stdin []byte, limit int) (rawResult, error) {
	res, err := s.run(ctx, script, stdin, s.timeout, limit)
	if err != nil {
		return res, err
	}
	if res.TimedOut {
		return res, errors.New("timed out")
	}
	return res, nil
}

// fileErr maps a script exit code to an error.
func fileErr(res rawResult, p string) error {
	switch res.ExitCode {
	case 0:
		return nil
	case exitEscape:
		return ErrEscape
	case exitNotFound:
		return fmt.Errorf("%s: %w", p, fs.ErrNotExist)
	case exitTooLarge:
		return ErrTooLarge
	case exitIsDir:
		return fmt.Errorf("%s is a directory", p)
	case exitNoWorkdir:
		return errors.New("sandbox workdir is missing")
	}
	msg := strings.TrimSpace(string(res.Stderr))
	if msg == "" {
		msg = "exit " + strconv.Itoa(res.ExitCode)
	}
	return fmt.Errorf("%s: %s", p, msg)
}

func (s *remoteSession) ReadFile(ctx context.Context, p string) ([]byte, error) {
	rel, err := s.rel(p)
	if err != nil {
		return nil, err
	}
	read := `cat "$T"`
	limit := MaxFileSize
	if s.b64Read {
		read = `base64 < "$T"`
		limit = MaxFileSize*4/3 + 64
	}
	script := s.guard(rel) + fmt.Sprintf(`[ -e "$T" ] || exit %d
[ -d "$T" ] && exit %d
[ "$(wc -c < "$T")" -gt %d ] && exit %d
%s
`, exitNotFound, exitIsDir, MaxFileSize, exitTooLarge, read)
	res, err := s.script(ctx, script, nil, limit)
	if err != nil {
		return nil, err
	}
	if err := fileErr(res, p); err != nil {
		return nil, err
	}
	if s.b64Read {
		return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(res.Stdout)), ""))
	}
	return res.Stdout, nil
}

// maxInlineWrite bounds a write that has to travel inside the script.
const maxInlineWrite = 1 << 20

func (s *remoteSession) WriteFile(ctx context.Context, p string, data []byte) error {
	if len(data) > MaxFileSize {
		return ErrTooLarge
	}
	rel, err := s.rel(p)
	if err != nil {
		return err
	}
	if rel == "." {
		return fmt.Errorf("%s is a directory", p)
	}
	// Create the missing directories first, then re-run the guard with the real location.
	mk := s.guard(rel) + fmt.Sprintf(`[ -d "$T" ] && exit %d
mkdir -p -- "$(dirname -- "$T")"
`, exitIsDir)
	var write string
	var stdin []byte
	if s.noStdin {
		if len(data) > maxInlineWrite {
			return fmt.Errorf("this sandbox backend writes files of at most %d bytes", maxInlineWrite)
		}
		enc := base64.StdEncoding.EncodeToString(data)
		write = "base64 -d > \"$T\" <<'HAL_B64_EOF'\n" + wrap76(enc) + "HAL_B64_EOF\n"
	} else {
		write = `cat > "$T"` + "\n"
		stdin = data
		if stdin == nil {
			stdin = []byte{}
		}
	}
	// Guard twice: before mkdir (so nothing is created outside) and after (symlinks created by mkdir cannot
	// exist, but the second pass checks the final directory).
	res, err := s.script(ctx, mk+s.guard(rel)+write, stdin, 1024)
	if err != nil {
		return err
	}
	return fileErr(res, p)
}

func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76])
		b.WriteByte('\n')
		s = s[76:]
	}
	b.WriteString(s)
	b.WriteByte('\n')
	return b.String()
}

func (s *remoteSession) List(ctx context.Context, p string) ([]Entry, error) {
	rel, err := s.rel(p)
	if err != nil {
		return nil, err
	}
	script := s.guard(rel) + fmt.Sprintf(`[ -d "$T" ] || exit %d
cd "$T" || exit %d
for f in * .[!.]* ..?*; do
  [ -e "$f" ] || [ -L "$f" ] || continue
  if [ -L "$f" ]; then t=f; n=0
  elif [ -d "$f" ]; then t=d; n=0
  else t=f; n=$(wc -c < "$f" 2>/dev/null || echo 0); fi
  printf '%%s\t%%s\t%%s\n' "$t" "$n" "$f"
done
`, exitNotFound, exitNotFound)
	res, err := s.script(ctx, script, nil, MaxOutput)
	if err != nil {
		return nil, err
	}
	if err := fileErr(res, p); err != nil {
		return nil, err
	}
	var out []Entry
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64)
		out = append(out, Entry{Name: f[2], IsDir: f[0] == "d", Size: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > MaxListed {
		out = out[:MaxListed]
	}
	return out, nil
}

func (s *remoteSession) Search(ctx context.Context, p, pattern string) ([]Match, error) {
	if pattern == "" {
		return nil, errors.New("empty pattern")
	}
	rel, err := s.rel(p)
	if err != nil {
		return nil, err
	}
	script := s.guard(rel) + fmt.Sprintf(`[ -e "$T" ] || exit %d
printf '%%s\n' "$Wp"
grep -rIHn -E --exclude-dir=.git -e %s -- "$T" 2>/dev/null
[ $? -le 1 ] || exit 2
exit 0
`, exitNotFound, shQuote(pattern))
	res, err := s.script(ctx, script, nil, MaxOutput)
	if err != nil {
		return nil, err
	}
	if err := fileErr(res, p); err != nil {
		return nil, err
	}
	lines := strings.Split(string(bytes.TrimRight(res.Stdout, "\n")), "\n")
	if len(lines) == 0 {
		return nil, nil
	}
	wp := strings.TrimSpace(lines[0])
	var out []Match
	for _, line := range lines[1:] {
		rest, ok := strings.CutPrefix(line, wp+"/")
		if !ok {
			continue
		}
		f := strings.SplitN(rest, ":", 3)
		if len(f) != 3 {
			continue
		}
		n, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		out = append(out, Match{Path: path.Clean(f[0]), Line: n, Text: strings.TrimRight(f[2], "\r")})
		if len(out) >= MaxMatches {
			break
		}
	}
	return out, nil
}

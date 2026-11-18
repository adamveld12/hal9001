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
	info    SessionInfo
	// locks orders the file replacements (WriteFile and EditFile) of the session by path.
	locks pathLocks
}

func (s *remoteSession) Workdir() string { return s.workdir }

func (s *remoteSession) Describe() SessionInfo {
	info := s.info
	info.Workdir = s.workdir
	return info
}

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
	exitDenied    = 95
	exitChanged   = 96
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
	case exitDenied:
		return fmt.Errorf("%s: %w", p, fs.ErrPermission)
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

// ReadFileRaw is ReadFile for trusted code. This session has no secrets of its own to hide, so Redact keeps the text
// as it is; the redacting wrapper of every session New returns supplies the real one.
func (s *remoteSession) ReadFileRaw(ctx context.Context, p string) (RawFile, error) {
	data, err := s.ReadFile(ctx, p)
	if err != nil {
		return RawFile{}, err
	}
	return RawFile{Data: data, Exists: true, Redact: plainText}, nil
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
	unlock, err := s.locks.lock(ctx, rel)
	if err != nil {
		return err
	}
	defer unlock()
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

// editAttempts is how often EditFile reads a file again after it changed under a replacement.
const editAttempts = 3

// EditFile replaces p with what edit makes of its content. edit runs here, not in the sandbox, so this takes two scripts:
// one reads the file together with a checksum, the other replaces it through a temporary file in the same directory and
// a rename, but only while the file still has the checksum. A file that changed in between is read again and edit called
// again. Calls of this session on one path are serialized.
//
// The checksum shortens the time in which a command the agent runs with Exec can change the file unnoticed to the time
// between the check and the rename. It does not close it: this is no transaction against arbitrary processes in the
// sandbox. The scripts confine the path like every file script of this session (guard).
func (s *remoteSession) EditFile(ctx context.Context, p string, edit EditFunc) error {
	rel, err := s.rel(p)
	if err != nil {
		return err
	}
	if rel == "." {
		return fmt.Errorf("%s is a directory", p)
	}
	unlock, err := s.locks.lock(ctx, rel)
	if err != nil {
		return err
	}
	defer unlock()
	for range editAttempts {
		data, token, exists, err := s.readForEdit(ctx, rel, p)
		if err != nil {
			return err
		}
		updated, err := edit(RawFile{Data: data, Exists: exists, Redact: plainText})
		if errors.Is(err, ErrNoChange) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(updated) > MaxFileSize {
			return ErrTooLarge
		}
		changed, err := s.replaceFile(ctx, rel, p, token, updated)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
	}
	return fmt.Errorf("%s keeps changing while it is edited; try again", p)
}

// readForEdit reads the file EditFile is about to replace, with the checksum the replacement is conditional on: the
// output of cksum, "nocksum" where there is none, "missing" for a file that does not exist.
func (s *remoteSession) readForEdit(ctx context.Context, rel, p string) (data []byte, token string, exists bool, err error) {
	read, limit := `cat "$T"`, MaxFileSize+256
	if s.b64Read {
		// base64 wraps its lines.
		read, limit = `base64 < "$T"`, MaxFileSize*4/3+MaxFileSize/50+1024
	}
	script := s.guard(rel) + fmt.Sprintf(`[ -d "$T" ] && exit %d
if [ ! -e "$T" ]; then echo missing; exit 0; fi
[ "$(wc -c < "$T")" -gt %d ] && exit %d
cksum < "$T" 2>/dev/null || echo nocksum
%s
`, exitIsDir, MaxFileSize, exitTooLarge, read)
	res, err := s.script(ctx, script, nil, limit)
	if err != nil {
		return nil, "", false, err
	}
	if err := fileErr(res, p); err != nil {
		return nil, "", false, err
	}
	if res.Truncated {
		return nil, "", false, ErrTooLarge
	}
	line, body, ok := bytes.Cut(res.Stdout, []byte{'\n'})
	if !ok {
		return nil, "", false, fmt.Errorf("%s: unexpected answer from the sandbox", p)
	}
	token = string(line)
	if token == "missing" {
		return nil, token, false, nil
	}
	if s.b64Read {
		dec, derr := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(body)), ""))
		if derr != nil {
			return nil, "", false, fmt.Errorf("%s: %w", p, derr)
		}
		body = dec
	}
	return body, token, true, nil
}

// replaceFile writes data in the place of the file, through a temporary file in the same directory and a rename, if the
// file still has the checksum token. It reports true, and writes nothing, when the file has changed.
func (s *remoteSession) replaceFile(ctx context.Context, rel, p, token string, data []byte) (changed bool, err error) {
	var write string
	var stdin []byte
	if s.noStdin {
		if len(data) > maxInlineWrite {
			return false, fmt.Errorf("this sandbox backend writes files of at most %d bytes", maxInlineWrite)
		}
		write = "base64 -d > \"$tmp\" <<'HAL_B64_EOF'\n" + wrap76(base64.StdEncoding.EncodeToString(data)) + "HAL_B64_EOF\n"
	} else {
		write = "cat > \"$tmp\"\n"
		stdin = data
		if stdin == nil {
			stdin = []byte{}
		}
	}
	// As in WriteFile: guard, create the missing directories, guard again with the real location. The file keeps its
	// mode and owner (cp -p of the old file); a new one gets 640. An existing file that is not writable is refused, as
	// a write in place would be.
	script := s.guard(rel) + fmt.Sprintf(`[ -d "$T" ] && exit %d
mkdir -p -- "$(dirname -- "$T")"
`, exitIsDir) + s.guard(rel) + fmt.Sprintf(`cur=missing
if [ -e "$T" ]; then cur=$(cksum < "$T" 2>/dev/null) || cur=nocksum; fi
[ "$cur" = %s ] || exit %d
[ ! -e "$T" ] || [ -w "$T" ] || exit %d
D=$(dirname -- "$T")
tmp=$(mktemp "$D/.hal-edit-XXXXXX") || exit 1
trap 'rm -f -- "$tmp"' EXIT
trap 'exit 1' HUP INT TERM
if [ -e "$T" ]; then cp -p -- "$T" "$tmp" || exit 1; else chmod 640 "$tmp" || exit 1; fi
%smv -f -- "$tmp" "$T" || exit 1
`, shQuote(token), exitChanged, exitDenied, write)
	res, err := s.script(ctx, script, stdin, 1024)
	if err != nil {
		return false, err
	}
	if res.ExitCode == exitChanged {
		return true, nil
	}
	return false, fileErr(res, p)
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

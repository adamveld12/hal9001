package sandbox

import (
	"context"
	"errors"
	"sync"
)

// RawFile is a file as stored, for trusted code: a view of the file for line edits, or the edit itself. Data holds the
// secret values of the sandbox; what is shown to an agent, written to a log or put in an error message must first go
// through Redact.
type RawFile struct {
	// Data is the content of the file. It is nil, and Exists false, for a file EditFile is about to create. Do not
	// modify it or keep it after the call it was handed to.
	Data   []byte
	Exists bool
	// Redact replaces the secret values in text by Redacted and keeps the line structure: the result has as many line
	// breaks as text, so line n of the result is line n of text (a secret that spans lines leaves empty lines after its
	// marker). A line is hidden in part or whole exactly when Redact changes it. Redact is never nil.
	Redact func(text string) string
}

// EditFunc computes the new content of a file from its current content, for Session.EditFile.
//
// It returns the new bytes; ErrNoChange, to leave the file as it is (EditFile then returns nil); or an error, which
// refuses the edit: EditFile returns it and the file is not touched. It must depend on old alone and have no side
// effects: EditFile calls it again, with the fresh content, when the file changed after it was read.
//
// old holds the bytes as stored, secret values included, because what EditFunc returns is written back: a redacted copy
// would replace the secret values in the file by Redacted. The backend wrapper that redacts what a session returns
// leaves the data of an EditFunc alone and redacts only the error EditFile returns.
type EditFunc func(old RawFile) ([]byte, error)

// ErrNoChange is returned by an EditFunc to leave the file untouched. A preview of an edit returns it after computing
// the result, so nothing is written.
var ErrNoChange = errors.New("no change")

// plainText is RawFile.Redact of a session that knows no secrets.
func plainText(s string) string { return s }

// symlinkError is the refusal to replace a symbolic link: replacing it by a file would drop the link, and writing
// through it would be a write somewhere else. It counts as a path that escapes, as every symlink does on the backends
// that run shell scripts.
type symlinkError struct{ path string }

func (e symlinkError) Error() string {
	return e.path + " is a symbolic link; edit the file it points to"
}

func (e symlinkError) Is(target error) bool { return target == ErrEscape }

// pathLocks serializes the read-modify-write cycles of EditFile by path. It orders the writes hal makes itself. It is
// no transaction against the rest of the system: a command the agent starts with Exec can change a file between the read
// of an EditFile and its rename, and the command's change is lost (the remote backends notice it and read again, see
// remoteSession.EditFile; the local one does not).
type pathLocks struct {
	mu sync.Mutex
	m  map[string]*pathLock
}

type pathLock struct {
	// held has room for one: whoever sends holds the lock.
	held chan struct{}
	// refs counts the callers that hold or wait for the lock; the entry is dropped with the last of them.
	refs int
}

// lock waits for the lock of key, or for ctx, and returns the function that releases it.
func (l *pathLocks) lock(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*pathLock{}
	}
	e := l.m[key]
	if e == nil {
		e = &pathLock{held: make(chan struct{}, 1)}
		l.m[key] = e
	}
	e.refs++
	l.mu.Unlock()
	drop := func() {
		l.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(l.m, key)
		}
		l.mu.Unlock()
	}
	select {
	case e.held <- struct{}{}:
		return func() {
			<-e.held
			drop()
		}, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

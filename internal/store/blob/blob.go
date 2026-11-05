// Package blob is a content-addressed, zstd-compressed blob store on disk.
//
// A blob's ref is the lowercase hex SHA-256 of its uncompressed bytes. The
// compressed bytes live at <dir>/<ref[:2]>/<ref[2:]>. Because the ref is a
// content hash, Put is idempotent and concurrent Puts of the same bytes are
// safe: files are written to a temp name and renamed into place.
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// ErrNotFound is returned when a ref has no blob on disk.
var ErrNotFound = errors.New("blob: not found")

// ErrInvalidRef is returned for refs that are not 64 lowercase hex characters.
var ErrInvalidRef = errors.New("blob: invalid ref")

const (
	refLen = sha256.Size * 2
	// maxDecoded bounds decompressed size to protect against corrupt or hostile files.
	maxDecoded = 1 << 30
)

// Store is a directory of blobs. It is safe for concurrent use.
type Store struct {
	dir string
	enc *zstd.Encoder
	dec *zstd.Decoder
}

// Open creates dir if needed and returns a Store rooted there.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("blob: create dir: %w", err)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, fmt.Errorf("blob: encoder: %w", err)
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecoded))
	if err != nil {
		_ = enc.Close()
		return nil, fmt.Errorf("blob: decoder: %w", err)
	}
	return &Store{dir: dir, enc: enc, dec: dec}, nil
}

// Close releases compression resources. Blobs on disk are untouched.
func (s *Store) Close() error {
	s.dec.Close()
	return s.enc.Close()
}

// Dir returns the root directory.
func (s *Store) Dir() string { return s.dir }

// RefOf returns the ref Put would assign to data without storing it.
func RefOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Put stores data and returns its ref. Putting existing content is a no-op
// apart from refreshing the file's modification time, which GC uses as a grace
// marker so a blob Put moments ago is never collected before its row commits.
func (s *Store) Put(data []byte) (string, error) {
	ref := RefOf(data)
	path := s.path(ref)
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return ref, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("blob: create shard dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("blob: temp file: %w", err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(s.enc.EncodeAll(data, nil))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("blob: write: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("blob: rename: %w", err)
	}
	return ref, nil
}

// Get returns the uncompressed bytes for ref.
func (s *Store) Get(ref string) ([]byte, error) {
	if !validRef(ref) {
		return nil, ErrInvalidRef
	}
	raw, err := os.ReadFile(s.path(ref))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blob: read %s: %w", ref, err)
	}
	out, err := s.dec.DecodeAll(raw, nil)
	if err != nil {
		return nil, fmt.Errorf("blob: decode %s: %w", ref, err)
	}
	return out, nil
}

// Has reports whether a blob exists for ref.
func (s *Store) Has(ref string) bool {
	if !validRef(ref) {
		return false
	}
	_, err := os.Stat(s.path(ref))
	return err == nil
}

// Delete removes the blob for ref. Deleting a missing blob is not an error.
func (s *Store) Delete(ref string) error {
	if !validRef(ref) {
		return ErrInvalidRef
	}
	err := os.Remove(s.path(ref))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("blob: delete %s: %w", ref, err)
	}
	return nil
}

// Info describes one stored blob.
type Info struct {
	Ref      string
	Modified time.Time
	Size     int64 // compressed size on disk
}

// Walk calls fn for every blob on disk. Stray temp files are skipped.
func (s *Store) Walk(fn func(Info) error) error {
	return filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		rel, err := filepath.Rel(s.dir, path)
		if err != nil {
			return err
		}
		ref := strings.ReplaceAll(rel, string(filepath.Separator), "")
		if !validRef(ref) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return fn(Info{Ref: ref, Modified: fi.ModTime(), Size: fi.Size()})
	})
}

func (s *Store) path(ref string) string {
	return filepath.Join(s.dir, ref[:2], ref[2:])
}

func validRef(ref string) bool {
	if len(ref) != refLen {
		return false
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

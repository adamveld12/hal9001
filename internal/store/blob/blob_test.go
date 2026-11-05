package blob

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPutGetRoundtripAndLayout(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	data := bytes.Repeat([]byte("hello hal9001 "), 20000)
	ref, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	if ref != RefOf(data) || len(ref) != 64 {
		t.Fatalf("ref = %q", ref)
	}
	// Layout is <dir>/ab/cdef...
	fi, err := os.Stat(filepath.Join(dir, ref[:2], ref[2:]))
	if err != nil {
		t.Fatalf("blob not at sharded path: %v", err)
	}
	if fi.Size() >= int64(len(data)) {
		t.Errorf("blob not compressed: %d >= %d", fi.Size(), len(data))
	}
	got, err := s.Get(ref)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("roundtrip failed: %v", err)
	}
}

func TestEmptyAndMissingAndInvalid(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ref, err := s.Put(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ref); err != nil || len(got) != 0 {
		t.Fatalf("empty blob: %v %v", got, err)
	}
	if _, err := s.Get(RefOf([]byte("absent"))); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: got %v, want ErrNotFound", err)
	}
	for _, bad := range []string{"", "../../etc/passwd", "ABCDEF", RefOf(nil)[:63]} {
		if _, err := s.Get(bad); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("Get(%q) = %v, want ErrInvalidRef", bad, err)
		}
	}
}

func TestConcurrentPutSameContent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	data := bytes.Repeat([]byte("x"), 100000)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			if _, err := s.Put(data); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	n := 0
	if err := s.Walk(func(Info) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("walk found %d blobs, want 1 (no temp leftovers counted)", n)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ref, _ := s.Put([]byte("bye"))
	if err := s.Delete(ref); err != nil {
		t.Fatal(err)
	}
	if s.Has(ref) {
		t.Error("blob still present after delete")
	}
	if err := s.Delete(ref); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

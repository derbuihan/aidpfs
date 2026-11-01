package recovery

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDurableEntryAndStages(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte{0, 1, 255, 2}
	id, err := s.Create("platform", "workspace", "/Workspace/Shared/file", "etag-1", content)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, got, err := reopened.Load(id)
	if err != nil || r.Stage != Prepared || !bytes.Equal(got, content) {
		t.Fatalf("load %v %v", r, err)
	}
	if err = s.Advance(id, Verified, ""); err == nil {
		t.Fatal("accepted invalid stage")
	}
	for _, stage := range []Stage{PutStarted, PutConfirmed, UpdateStarted, Verified} {
		if err = s.Advance(id, stage, func() string {
			if stage == PutConfirmed {
				return "new-etag"
			}
			return ""
		}()); err != nil {
			t.Fatal(err)
		}
		r, _, err = s.Load(id)
		if err != nil || r.Stage != stage {
			t.Fatalf("stage %v %v", r.Stage, err)
		}
	}
	if err = s.Advance(id, Stage(""), ""); err == nil {
		t.Fatal("accepted empty terminal transition")
	}
	info, err := os.Stat(filepath.Join(dir, id, "content"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("content mode %v %v", info, err)
	}
}
func TestRejectInsecureAndCorruptEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, _ := Open(dir)
	id, err := s.Create("platform", "workspace", "/Workspace/file", "etag", []byte("saved"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Load("../escape"); err == nil {
		t.Fatal("path escape accepted")
	}
	file := filepath.Join(dir, id, "content")
	if err = os.WriteFile(file, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Load(id); err == nil {
		t.Fatal("corrupt content accepted")
	}
	if _, err = os.Stat(file); err != nil {
		t.Fatal("corrupt recovery data removed")
	}
	if err = os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir); err == nil {
		t.Fatal("insecure directory accepted")
	}
}
func TestRejectSymlinkContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, _ := Open(dir)
	id, err := s.Create("platform", "workspace", "/Workspace/file", "etag", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, id, "content")
	if err = os.Remove(f); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("/etc/passwd", f); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Load(id); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestConcurrentTransitionsAcrossStores(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.Create("platform", "workspace", "/Workspace/file", "etag", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, s := range []*Store{a, b} {
		go func(s *Store) { <-start; results <- s.Advance(id, PutStarted, "") }(s)
	}
	close(start)
	x, y := <-results, <-results
	if (x == nil) == (y == nil) {
		t.Fatalf("expected one successful transition: %v %v", x, y)
	}
}

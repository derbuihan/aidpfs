package mount

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"aidpfs/internal/backend"
	"aidpfs/internal/writeback"
)

type mutableReader struct {
	mu    sync.Mutex
	files map[string]bool
}

func (r *mutableReader) Stat(_ context.Context, p string) (backend.Metadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p == "" {
		return backend.Metadata{Kind: backend.Directory}, nil
	}
	if r.files[p] {
		return backend.Metadata{Name: p, Kind: backend.File, Size: 4}, nil
	}
	return backend.Metadata{}, syscall.ENOENT
}
func (r *mutableReader) ReadDir(context.Context, string) ([]backend.Metadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []backend.Metadata
	for p := range r.files {
		result = append(result, backend.Metadata{Name: p, Kind: backend.File, Size: 4})
	}
	return result, nil
}
func (r *mutableReader) ReadFile(context.Context, string) ([]byte, error) { return []byte("data"), nil }

func TestMutationGuardsAndUnknownOutcome(t *testing.T) {
	ctx := context.Background()
	calls := 0
	b := &experimentalBuffers{values: map[string]*experimentalEntry{}, mutate: func(_ context.Context, op, src, dst string) error {
		calls++
		if op != "unlink" || src != "file" || dst != "" {
			t.Errorf("wrong operation")
		}
		return errors.New("uncertain")
	}}
	n := &experimentalNode{reader: &fake{}, buffers: b}
	b.values["file"] = &experimentalEntry{buffer: &writeback.Buffer{}, refs: 1}
	if got := n.Unlink(ctx, "file"); got != syscall.EBUSY {
		t.Fatalf("open: %v", got)
	}
	delete(b.values, "file")
	if got := n.Rename(ctx, "file", n, "file", 1); got != syscall.EOPNOTSUPP {
		t.Fatalf("flags: %v", got)
	}
	if got := n.Unlink(ctx, "file"); got != syscall.EIO || calls != 1 {
		t.Fatalf("first: %v calls %d", got, calls)
	}
	if got := n.Unlink(ctx, "file"); got != syscall.EIO || calls != 1 {
		t.Fatalf("retry: %v calls %d", got, calls)
	}
}

func TestMutationMountRenameAndUnlink(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("FUSE unavailable")
	}
	dir := t.TempDir()
	r := &mutableReader{files: map[string]bool{"a": true}}
	m, err := mountWithMutations(context.Background(), dir, r, nil, nil, nil, func(_ context.Context, op, src, dst string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.files[src] {
			return syscall.ENOENT
		}
		delete(r.files, src)
		if op == "rename" {
			r.files[dst] = true
		}
		return nil
	})
	if err != nil {
		t.Skipf("FUSE mount unavailable: %v", err)
	}
	defer func() {
		if e := m.Server.Unmount(); e != nil {
			t.Errorf("unmount: %v", e)
		}
		m.Server.Wait()
	}()
	if _, err = os.Stat(filepath.Join(dir, "a")); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(dir, "a"), filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old path: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "b")); err != nil {
		t.Fatalf("new path: %v", err)
	}
	if err = os.Remove(filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted path: %v", err)
	}
}

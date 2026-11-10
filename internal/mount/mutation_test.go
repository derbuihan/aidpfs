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

	"github.com/hanwen/go-fuse/v2/fuse"
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
	for _, flags := range []uint32{2, 4, 3} { // RENAME_EXCHANGE, RENAME_WHITEOUT, combined
		if got := n.Rename(ctx, "file", n, "other", flags); got != syscall.EOPNOTSUPP {
			t.Fatalf("flags %d: %v", flags, got)
		}
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

func TestRenameDestinationRulesAndRejectedOutcome(t *testing.T) {
	ctx := context.Background()
	r := &mutableReader{files: map[string]bool{"a.txt": true, "b.txt": true}}
	var calls []string
	result := error(nil)
	b := &experimentalBuffers{values: map[string]*experimentalEntry{}, mutate: func(_ context.Context, op, src, dst string) error {
		calls = append(calls, op+":"+src+">"+dst)
		return result
	}}
	n := &experimentalNode{reader: r, buffers: b}
	if got := n.Rename(ctx, "a.txt", n, "b.txt", 0); got != syscall.EOPNOTSUPP {
		t.Fatalf("plain rename onto existing = %v, want EOPNOTSUPP", got)
	}
	if got := n.Rename(ctx, "a.txt", n, "b.txt", renameNoReplace); got != syscall.EEXIST {
		t.Fatalf("RENAME_NOREPLACE onto existing = %v, want EEXIST", got)
	}
	for _, bad := range []string{"日本.txt", "x#y", "x(y)", "a~", "q?.txt"} {
		if got := n.Rename(ctx, "a.txt", n, bad, 0); got != syscall.EINVAL {
			t.Fatalf("rename to %q = %v, want EINVAL", bad, got)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("rejected locally but sent %v", calls)
	}
	// A definite server rejection is reported and does not block later work.
	result = backend.NotApplied(syscall.EEXIST)
	if got := n.Rename(ctx, "a.txt", n, "c.txt", renameNoReplace); got != syscall.EEXIST {
		t.Fatalf("server conflict = %v, want EEXIST", got)
	}
	result = nil
	if got := n.Rename(ctx, "a.txt", n, "c.txt", renameNoReplace); got != 0 {
		t.Fatalf("rename after rejection = %v", got)
	}
	// An unknown outcome blocks every later namespace change.
	result = errors.New("uncertain")
	if got := n.Unlink(ctx, "b.txt"); got != syscall.EIO {
		t.Fatalf("unknown unlink = %v", got)
	}
	result = nil
	if got := n.Unlink(ctx, "a.txt"); got != syscall.EIO {
		t.Fatalf("unlink after unknown = %v, want EIO", got)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %v", calls)
	}
}

func TestCreateAndMkdirRejectInvalidNames(t *testing.T) {
	ctx := context.Background()
	sent := 0
	record := func(context.Context, string) error { sent++; return nil }
	b := &experimentalBuffers{values: map[string]*experimentalEntry{}, create: record, mkdir: record}
	n := &experimentalNode{reader: &fake{}, buffers: b}
	for _, bad := range []string{"日本.txt", "file~", "a#b", "x(y)"} {
		if _, _, _, got := n.Create(ctx, bad, 0, 0, &fuse.EntryOut{}); got != syscall.EINVAL {
			t.Fatalf("create %q = %v", bad, got)
		}
		if _, got := n.Mkdir(ctx, bad, 0, &fuse.EntryOut{}); got != syscall.EINVAL {
			t.Fatalf("mkdir %q = %v", bad, got)
		}
	}
	if sent != 0 {
		t.Fatalf("sent %d requests for invalid names", sent)
	}
}

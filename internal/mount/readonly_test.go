package mount

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"aidpfs/internal/backend"
)

type fake struct {
	mu       sync.Mutex
	data     []byte
	deleted  bool
	modified time.Time
}

func (f *fake) Stat(_ context.Context, p string) (backend.Metadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch p {
	case "":
		return backend.Metadata{Kind: backend.Directory}, nil
	case "file":
		if f.deleted {
			return backend.Metadata{}, syscall.ENOENT
		}
		return backend.Metadata{Name: "file", Kind: backend.File, Size: int64(len(f.data)), Modified: f.modified}, nil
	default:
		return backend.Metadata{}, syscall.ENOENT
	}
}
func (f *fake) ReadDir(_ context.Context, p string) ([]backend.Metadata, error) {
	if p != "" {
		return nil, syscall.ENOTDIR
	}
	return []backend.Metadata{{Name: "file", Kind: backend.File}}, nil
}
func (f *fake) ReadFile(_ context.Context, p string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p == "" {
		return nil, syscall.EISDIR
	}
	if p != "file" || f.deleted {
		return nil, syscall.ENOENT
	}
	return append([]byte(nil), f.data...), nil
}
func TestSnapshotAndReadonlyMount(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("FUSE device unavailable")
	}
	d := t.TempDir()
	modified := time.Unix(1760000000, 0)
	f := &fake{data: []byte("abcdef"), modified: modified}
	srv, err := Mount(context.Background(), d, f)
	if err != nil {
		t.Skipf("FUSE mount unavailable: %v", err)
	}
	defer func() {
		if err := srv.Unmount(); err != nil {
			t.Errorf("unmount: %v", err)
		}
		srv.Wait()
	}()
	var fsStat syscall.Statfs_t
	if err := syscall.Statfs(d, &fsStat); !errors.Is(err, syscall.EOPNOTSUPP) {
		t.Fatalf("statfs error = %v, want EOPNOTSUPP", err)
	}
	file := filepath.Join(d, "file")
	rootInfo, err := os.Stat(d)
	if err != nil || rootInfo.Mode().Perm() != 0500 {
		t.Fatalf("directory mode = %v, error = %v", rootInfo, err)
	}
	h, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	buf := make([]byte, 3)
	if n, err := h.ReadAt(buf, 2); err != nil || n != 3 || string(buf) != "cde" {
		t.Fatalf("partial read %q %d %v", buf, n, err)
	}
	f.mu.Lock()
	f.data = []byte("new data")
	f.mu.Unlock()
	old, err := io.ReadAll(h)
	if err != nil || string(old) != "abcdef" {
		t.Fatalf("open snapshot %q %v", old, err)
	}
	fresh, err := os.ReadFile(file)
	if err != nil || string(fresh) != "new data" {
		t.Fatalf("reopen %q %v", fresh, err)
	}
	st, err := os.Stat(file)
	if err != nil || st.Size() != int64(len(fresh)) {
		t.Fatalf("stat %v %v", st, err)
	}
	if st.Mode().Perm() != 0400 || !st.ModTime().Equal(modified) {
		t.Fatalf("file mode/time = %v/%v", st.Mode().Perm(), st.ModTime())
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Gid != uint32(os.Getgid()) {
		t.Fatalf("file owner = %#v", st.Sys())
	}
	if _, err = os.OpenFile(file, os.O_WRONLY, 0); !errors.Is(err, syscall.EROFS) {
		t.Errorf("existing write: %v", err)
	}
	if _, err = os.Create(filepath.Join(d, "new")); !errors.Is(err, syscall.EROFS) {
		t.Errorf("create: %v", err)
	}
	if err = os.Chmod(file, 0600); !errors.Is(err, syscall.EROFS) {
		t.Errorf("chmod: %v", err)
	}
	if err = os.Chown(file, os.Getuid(), os.Getgid()); !errors.Is(err, syscall.EROFS) {
		t.Errorf("chown: %v", err)
	}
	if err = os.Chtimes(file, modified, modified); !errors.Is(err, syscall.EROFS) {
		t.Errorf("utimens: %v", err)
	}
	f.mu.Lock()
	f.deleted = true
	f.mu.Unlock()
	handleStat, err := h.Stat()
	if err != nil || handleStat.Size() != 6 {
		t.Errorf("open file stat after deletion: %v %v", handleStat, err)
	}
	if _, err := os.Stat(file); !errors.Is(err, syscall.ENOENT) {
		t.Errorf("deleted path stat: %v", err)
	}
}
func TestSnapshotBounds(t *testing.T) {
	s := &snapshot{data: []byte("abc")}
	if _, e := s.Read(context.Background(), make([]byte, 1), -1); e != syscall.EINVAL {
		t.Fatal(e)
	}
	r, e := s.Read(context.Background(), make([]byte, 2), 2)
	if e != 0 {
		t.Fatal(e)
	}
	b, status := r.Bytes(nil)
	if status != 0 || string(b) != "c" {
		t.Fatalf("%q %v", b, status)
	}
}

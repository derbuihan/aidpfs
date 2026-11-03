package mount

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"aidpfs/internal/backend"
)

type fake struct {
	mu        sync.Mutex
	data      []byte
	readCount int
	deleted   bool
	modified  time.Time
}

type directoryListingFake struct {
	*fake
	entries []backend.Metadata
	listErr error
}

func (f *directoryListingFake) ReadDir(context.Context, string) ([]backend.Metadata, error) {
	return append([]backend.Metadata(nil), f.entries...), f.listErr
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
	f.readCount++
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
	for _, flags := range []int{os.O_WRONLY, os.O_RDONLY | os.O_TRUNC, os.O_RDONLY | os.O_APPEND} {
		if _, err = os.OpenFile(file, flags, 0); !errors.Is(err, syscall.EROFS) {
			t.Errorf("write/truncate/append flags %#x: %v", flags, err)
		}
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

func TestReadonlyMountDeniesOtherUID(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("FUSE device unavailable")
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		t.Skip("sudo unavailable for cross-user permission check")
	}
	check := exec.Command(sudo, "-n", "-u", "#65534", "--", "true")
	if err = check.Run(); err != nil {
		t.Skipf("noninteractive user switch unavailable: %v", err)
	}
	d := t.TempDir()
	if err = os.Chmod(d, 0755); err != nil {
		t.Fatal(err)
	}
	srv, err := Mount(context.Background(), d, &fake{data: []byte("private")})
	if err != nil {
		t.Skipf("FUSE mount unavailable: %v", err)
	}
	defer func() {
		if err := srv.Unmount(); err != nil {
			t.Errorf("unmount: %v", err)
		}
		srv.Wait()
	}()

	check = exec.Command(sudo, "-n", "-u", "#65534", "--", "test", "-r", filepath.Join(d, "file"))
	if err = check.Run(); err == nil {
		t.Fatal("another UID can read the owner-only FUSE file")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("cross-user permission check failed unexpectedly: %v", err)
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

func TestReadonlyReaddirSortsEntriesAndMapsErrors(t *testing.T) {
	reader := &directoryListingFake{
		fake: &fake{},
		entries: []backend.Metadata{
			{Name: "z-dir", Kind: backend.Directory},
			{Name: "a-file", Kind: backend.File},
		},
	}
	stream, errno := (&node{reader: reader}).Readdir(context.Background())
	if errno != 0 || stream == nil {
		t.Fatalf("Readdir stream=%v errno=%v", stream, errno)
	}
	defer stream.Close()
	var names []string
	var modes []uint32
	for stream.HasNext() {
		entry, entryErrno := stream.Next()
		if entryErrno != 0 {
			t.Fatalf("next entry errno=%v", entryErrno)
		}
		names = append(names, entry.Name)
		modes = append(modes, entry.Mode&syscall.S_IFMT)
	}
	if len(names) != 2 || names[0] != "a-file" || names[1] != "z-dir" {
		t.Fatalf("directory order = %v", names)
	}
	if modes[0] != syscall.S_IFREG || modes[1] != syscall.S_IFDIR {
		t.Fatalf("directory entry modes = %#o", modes)
	}

	reader.listErr = context.Canceled
	if stream, errno = (&node{reader: reader}).Readdir(context.Background()); stream != nil || errno != syscall.EINTR {
		t.Fatalf("cancelled Readdir stream=%v errno=%v, want EINTR", stream, errno)
	}
}

func TestExperimentalReaddirSortsEntriesAndMapsErrors(t *testing.T) {
	reader := &directoryListingFake{
		fake: &fake{},
		entries: []backend.Metadata{
			{Name: "z-dir", Kind: backend.Directory},
			{Name: "a-file", Kind: backend.File},
		},
	}
	stream, errno := (&experimentalNode{reader: reader}).Readdir(context.Background())
	if errno != 0 || stream == nil {
		t.Fatalf("Readdir stream=%v errno=%v", stream, errno)
	}
	defer stream.Close()
	var names []string
	var modes []uint32
	for stream.HasNext() {
		entry, entryErrno := stream.Next()
		if entryErrno != 0 {
			t.Fatalf("next entry errno=%v", entryErrno)
		}
		names = append(names, entry.Name)
		modes = append(modes, entry.Mode&syscall.S_IFMT)
	}
	if len(names) != 2 || names[0] != "a-file" || names[1] != "z-dir" {
		t.Fatalf("directory order = %v", names)
	}
	if modes[0] != syscall.S_IFREG || modes[1] != syscall.S_IFDIR {
		t.Fatalf("directory entry modes = %#o", modes)
	}

	reader.listErr = context.Canceled
	if stream, errno = (&experimentalNode{reader: reader}).Readdir(context.Background()); stream != nil || errno != syscall.EINTR {
		t.Fatalf("cancelled Readdir stream=%v errno=%v, want EINTR", stream, errno)
	}
}

func TestConcurrentOpensUseIndependentSnapshots(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("FUSE device unavailable")
	}
	d := t.TempDir()
	want := []byte("one snapshot per open")
	f := &fake{data: append([]byte(nil), want...)}
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

	const readers = 16
	start := make(chan struct{})
	errs := make(chan error, readers)
	for i := 0; i < readers; i++ {
		go func() {
			<-start
			got, err := os.ReadFile(filepath.Join(d, "file"))
			if err != nil {
				errs <- err
			} else if string(got) != string(want) {
				errs <- errors.New("concurrent open returned corrupted or shared content")
			} else {
				errs <- nil
			}
		}()
	}
	close(start)
	for i := 0; i < readers; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	f.mu.Lock()
	gotReads := f.readCount
	f.mu.Unlock()
	if gotReads != readers {
		t.Fatalf("backend reads = %d, want one independent read per open (%d)", gotReads, readers)
	}
}

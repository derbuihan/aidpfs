package mount

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"aidpfs/internal/recovery"
	"aidpfs/internal/writeback"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type mountRemote struct {
	mu   sync.Mutex
	tag  string
	data []byte
	fail bool
	puts int
}

func (r *mountRemote) Refresh(context.Context, writeback.Target) (writeback.Upload, error) {
	return r, nil
}
func (r *mountRemote) Put(_ context.Context, tag string, data []byte) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.puts++
	if r.fail {
		return "", syscall.EIO
	}
	if tag != r.tag {
		return "", syscall.ESTALE
	}
	r.data = append([]byte(nil), data...)
	r.tag = "next"
	return r.tag, nil
}
func (r *mountRemote) Complete(context.Context, writeback.Target, string, int64) error { return nil }
func (r *mountRemote) Verify(_ context.Context, _ writeback.Target, tag string, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tag != r.tag || !bytes.Equal(data, r.data) {
		return syscall.ESTALE
	}
	return nil
}
func testExperimentalMount(t *testing.T, remote *mountRemote) (string, *recovery.Store) {
	return testExperimentalMountReader(t, remote, &fake{data: []byte("abcd")})
}
func testExperimentalMountReader(t *testing.T, remote *mountRemote, reader *fake) (string, *recovery.Store) {
	t.Helper()
	if _, e := os.Stat("/dev/fuse"); e != nil {
		t.Skip("FUSE unavailable")
	}
	d := t.TempDir()
	cache := t.TempDir()
	if e := os.Chmod(cache, 0700); e != nil {
		t.Fatal(e)
	}
	store, e := recovery.Open(cache)
	if e != nil {
		t.Fatal(e)
	}
	server, e := mountExperimental(context.Background(), d, reader, func(_ context.Context, rel string) (*writeback.Buffer, error) {
		if rel != "file" {
			return nil, syscall.ENOENT
		}
		return writeback.NewBuffer(writeback.Target{PlatformID: "p", WorkspaceKey: "w", RemotePath: "/Workspace/file"}, "old", []byte("abcd"), store, remote)
	})
	if e != nil {
		t.Skipf("FUSE mount unavailable: %v", e)
	}
	t.Cleanup(func() {
		if e := server.Unmount(); e != nil {
			t.Errorf("unmount: %v", e)
		}
		server.Wait()
	})
	return d, store
}
func TestExperimentalMountWriteSyncAndTruncate(t *testing.T) {
	remote := &mountRemote{tag: "old"}
	d, store := testExperimentalMount(t, remote)
	name := filepath.Join(d, "file")
	f, e := os.OpenFile(name, os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	if n, e := f.WriteAt([]byte("XY"), 2); e != nil || n != 2 {
		t.Fatalf("write %d %v", n, e)
	}
	if e = f.Truncate(7); e != nil {
		t.Fatal(e)
	}
	st, e := f.Stat()
	if e != nil || st.Size() != 7 {
		t.Fatalf("stat %v %v", st, e)
	}
	if e = f.Sync(); e != nil {
		t.Fatal(e)
	}
	remote.mu.Lock()
	got := append([]byte(nil), remote.data...)
	count := remote.puts
	remote.mu.Unlock()
	if !bytes.Equal(got, []byte{'a', 'b', 'X', 'Y', 0, 0, 0}) || count != 1 {
		t.Fatal("sync bytes or count incorrect")
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	remote.mu.Lock()
	count = remote.puts
	remote.mu.Unlock()
	if count != 1 {
		t.Fatal("close re-sent clean state")
	}
	entries, e := store.List()
	if e != nil || len(entries) != 2 {
		t.Fatalf("journal entries %d %v", len(entries), e)
	}
}
func TestExperimentalMountFsyncFailureRetainsJournal(t *testing.T) {
	remote := &mountRemote{tag: "old", fail: true}
	d, store := testExperimentalMount(t, remote)
	f, e := os.OpenFile(filepath.Join(d, "file"), os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.WriteAt([]byte("data"), 0); e != nil {
		t.Fatal(e)
	}
	if e = f.Sync(); !errors.Is(e, syscall.EIO) {
		t.Fatalf("fsync error %v", e)
	}
	remote.mu.Lock()
	count := remote.puts
	remote.mu.Unlock()
	if e = f.Sync(); !errors.Is(e, syscall.EIO) {
		t.Fatalf("second fsync %v", e)
	}
	remote.mu.Lock()
	if remote.puts != count {
		t.Error("automatically retried")
	}
	remote.mu.Unlock()
	if e = f.Close(); !errors.Is(e, syscall.EIO) {
		t.Errorf("close did not report failure: %v", e)
	}
	entries, e := store.List()
	if e != nil || len(entries) != 1 || entries[0].Invalid || entries[0].Stage != recovery.PutStarted {
		t.Fatalf("recovery entries %v %v", entries, e)
	}
	_, data, e := store.Load(entries[0].ID)
	if e != nil || !bytes.Equal(data, []byte("data")) {
		t.Fatal("recovery content missing")
	}
}

func TestExperimentalMountTruncateOnOpenAndSharedHandles(t *testing.T) {
	remote := &mountRemote{tag: "old"}
	d, _ := testExperimentalMount(t, remote)
	name := filepath.Join(d, "file")
	first, e := os.OpenFile(name, os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	second, e := os.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = second.Write([]byte("fresh")); e != nil {
		t.Fatal(e)
	}
	buf := make([]byte, 5)
	if n, e := first.ReadAt(buf, 0); e != nil || n != 5 || string(buf) != "fresh" {
		t.Fatalf("shared read n=%d err=%v", n, e)
	}
	if e = second.Close(); e != nil {
		t.Fatal(e)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if !bytes.Equal(remote.data, []byte("fresh")) {
		t.Fatal("truncate-on-open save mismatch")
	}
}

func TestExperimentalBufferReferencesRetainDirtyAndEvictClean(t *testing.T) {
	b, _ := newMountBuffer(t, &mountRemote{tag: "old"})
	created := 0
	m := &experimentalBuffers{values: map[string]*experimentalEntry{}, open: func(context.Context, string) (*writeback.Buffer, error) { created++; return b, nil }}
	first, e := m.get(context.Background(), "file")
	if e != nil {
		t.Fatal(e)
	}
	second, e := m.get(context.Background(), "file")
	if e != nil || first != second || created != 1 {
		t.Fatal("handles not shared")
	}
	m.release("file", first)
	if m.find("file") == nil {
		t.Fatal("evicted while handle still open")
	}
	m.release("file", second)
	if m.find("file") != nil {
		t.Fatal("clean buffer not evicted")
	}
	current, e := m.get(context.Background(), "file")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = current.WriteAt([]byte("x"), 0); e != nil {
		t.Fatal(e)
	}
	m.release("file", current)
	if m.find("file") != current {
		t.Fatal("dirty buffer was discarded")
	}
	again, e := m.get(context.Background(), "file")
	if e != nil || again != current {
		t.Fatal("dirty buffer not reused")
	}
	m.release("file", again)
}
func newMountBuffer(t *testing.T, remote *mountRemote) (*writeback.Buffer, *recovery.Store) {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := recovery.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	b, err := writeback.NewBuffer(writeback.Target{PlatformID: "p", WorkspaceKey: "w", RemotePath: "/Workspace/file"}, "old", []byte("abcd"), s, remote)
	if err != nil {
		t.Fatal(err)
	}
	return b, s
}

func TestExperimentalReadOnlyCloseDoesNotFlushOtherHandle(t *testing.T) {
	remote := &mountRemote{tag: "old"}
	d, store := testExperimentalMount(t, remote)
	name := filepath.Join(d, "file")
	writer, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.WriteAt([]byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	puts := remote.puts
	remote.mu.Unlock()
	if puts != 0 {
		t.Fatal("read-only close flushed another handle's dirty data")
	}
	entries, err := store.List()
	if err != nil || len(entries) != 1 || entries[0].Stage != recovery.Prepared {
		t.Fatalf("dirty data not retained: %v %v", entries, err)
	}
	if err = writer.Sync(); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	puts = remote.puts
	got := string(remote.data)
	remote.mu.Unlock()
	if puts != 1 || got != "newd" {
		t.Fatalf("writer sync: puts=%d bytes=%q", puts, got)
	}
}

func TestExperimentalOpenHandleSurvivesRemoteStatFailure(t *testing.T) {
	remote := &mountRemote{tag: "old", fail: true}
	reader := &fake{data: []byte("abcd")}
	d, store := testExperimentalMountReader(t, remote, reader)
	name := filepath.Join(d, "file")
	f, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader.mu.Lock()
	reader.deleted = true
	reader.mu.Unlock()
	if err = f.Truncate(2); err != nil {
		t.Fatalf("handle truncate: %v", err)
	}
	st, err := f.Stat()
	if err != nil || st.Size() != 2 {
		t.Fatalf("handle stat: %v %v", st, err)
	}
	if _, err = os.Stat(name); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("path stat: %v", err)
	}
	if err = f.Sync(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("sync: %v", err)
	}
	if err = f.Close(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("close: %v", err)
	}
	entries, err := store.List()
	if err != nil || len(entries) != 1 || entries[0].Stage != recovery.PutStarted {
		t.Fatalf("journal: %v %v", entries, err)
	}
	_, data, err := store.Load(entries[0].ID)
	if err != nil || string(data) != "ab" {
		t.Fatalf("journal content error: %v", err)
	}
}

func TestExperimentalSynchronousWrites(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags int
		fail  bool
	}{{"sync", syscall.O_SYNC, false}, {"dsync", syscall.O_DSYNC, false}, {"sync_failure", syscall.O_SYNC, true}} {
		t.Run(tc.name, func(t *testing.T) {
			remote := &mountRemote{tag: "old", fail: tc.fail}
			d, store := testExperimentalMount(t, remote)
			f, err := os.OpenFile(filepath.Join(d, "file"), os.O_WRONLY|tc.flags, 0)
			if err != nil {
				t.Fatal(err)
			}
			n, err := f.WriteAt([]byte("new"), 0)
			remote.mu.Lock()
			puts := remote.puts
			got := string(remote.data)
			remote.mu.Unlock()
			if puts != 1 {
				t.Fatalf("write returned before remote PUT: %d", puts)
			}
			if tc.fail {
				if !errors.Is(err, syscall.EIO) || n != 0 {
					t.Fatalf("write failure n=%d err=%v", n, err)
				}
				if err = f.Close(); !errors.Is(err, syscall.EIO) {
					t.Fatalf("close failure: %v", err)
				}
				entries, err := store.List()
				if err != nil || len(entries) != 1 || entries[0].Stage != recovery.PutStarted {
					t.Fatalf("journal %v %v", entries, err)
				}
				_, data, err := store.Load(entries[0].ID)
				if err != nil || string(data) != "newd" {
					t.Fatal("dirty content missing")
				}
			} else {
				if err != nil || n != 3 || got != "newd" {
					t.Fatalf("write n=%d err=%v", n, err)
				}
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
				remote.mu.Lock()
				puts = remote.puts
				remote.mu.Unlock()
				if puts != 1 {
					t.Fatal("close repeated synchronous write")
				}
			}
		})
	}
}

func TestExperimentalCreateFailureDoesNotRetry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		createErr error
		want      syscall.Errno
	}{{"exists", syscall.EEXIST, syscall.EEXIST}, {"ambiguous", errors.New("unknown outcome"), syscall.EIO}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			n := &experimentalNode{buffers: &experimentalBuffers{create: func(_ context.Context, name string) error {
				calls++
				if name != "new" {
					t.Fatal("unexpected path")
				}
				return tc.createErr
			}}}
			_, _, _, got := n.Create(context.Background(), "new", syscall.O_WRONLY, 0600, &fuse.EntryOut{})
			if got != tc.want || calls != 1 {
				t.Fatalf("errno=%v calls=%d", got, calls)
			}
		})
	}
}

func TestExperimentalMkdirFailureDoesNotRetry(t *testing.T) {
	calls := 0
	n := &experimentalNode{buffers: &experimentalBuffers{mkdir: func(_ context.Context, rel string) error { calls++; return syscall.EEXIST }}}
	_, err := n.Mkdir(context.Background(), "dir", 0700, &fuse.EntryOut{})
	if err != syscall.EEXIST || calls != 1 {
		t.Fatalf("errno %v calls %d", err, calls)
	}
}

func TestExperimentalAppendSharedHandles(t *testing.T) {
	remote := &mountRemote{tag: "old"}
	d, _ := testExperimentalMount(t, remote)
	name := filepath.Join(d, "file")
	first, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i, h := range []*os.File{first, second} {
		wg.Add(1)
		go func(h *os.File, b byte) {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				if n, e := h.Write([]byte{b}); e != nil || n != 1 {
					t.Errorf("append n=%d err=%v", n, e)
					return
				}
			}
		}(h, byte('x'+i))
	}
	wg.Wait()
	if err = first.Sync(); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	result := append([]byte(nil), remote.data...)
	remote.mu.Unlock()
	if len(result) != 12 || string(result[:4]) != "abcd" || bytes.Count(result[4:], []byte("x")) != 4 || bytes.Count(result[4:], []byte("y")) != 4 {
		t.Fatalf("appends not preserved: %q", result)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExperimentalSynchronousAppendFailureRetainsData(t *testing.T) {
	remote := &mountRemote{tag: "old", fail: true}
	d, store := testExperimentalMount(t, remote)
	f, err := os.OpenFile(filepath.Join(d, "file"), os.O_WRONLY|os.O_APPEND|os.O_SYNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := f.Write([]byte("x"))
	if n != 0 || !errors.Is(err, syscall.EIO) {
		t.Fatalf("append: %d %v", n, err)
	}
	if err = f.Close(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("close: %v", err)
	}
	entries, err := store.List()
	if err != nil || len(entries) != 1 || entries[0].Stage != recovery.PutStarted {
		t.Fatalf("entries: %v %v", entries, err)
	}
	_, data, err := store.Load(entries[0].ID)
	if err != nil || string(data) != "abcdx" {
		t.Fatalf("append recovery error: %v", err)
	}
}

func TestExperimentalDeleteOperationsAreUnsupported(t *testing.T) {
	n := &experimentalNode{}
	if got := n.Unlink(context.Background(), "file"); got != syscall.EOPNOTSUPP {
		t.Fatalf("unlink: %v", got)
	}
	if got := n.Rmdir(context.Background(), "dir"); got != syscall.EOPNOTSUPP {
		t.Fatalf("rmdir: %v", got)
	}
}

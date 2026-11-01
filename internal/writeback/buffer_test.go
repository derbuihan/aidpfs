package writeback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"testing"

	"aidpfs/internal/recovery"
)

func newTestBuffer(t *testing.T, remote *fakeRemote) (*Buffer, *recovery.Store) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := recovery.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBuffer(Target{"p", "w", "/Workspace/file"}, "old", []byte("abcd"), s, remote)
	if err != nil {
		t.Fatal(err)
	}
	return b, s
}
func TestBufferPartialTruncateAndSync(t *testing.T) {
	remote := &fakeRemote{tag: "old"}
	b, s := newTestBuffer(t, remote)
	if n, err := b.WriteAt([]byte("XY"), 2); err != nil || n != 2 {
		t.Fatalf("write %d %v", n, err)
	}
	if err := b.Truncate(7); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 8)
	n, err := b.ReadAt(got, 0)
	if n != 7 || !errors.Is(err, io.EOF) || !bytes.Equal(got[:n], []byte{'a', 'b', 'X', 'Y', 0, 0, 0}) {
		t.Fatal("read mismatch")
	}
	if b.Size() != 7 || !b.Dirty() {
		t.Fatal("incorrect dirty size")
	}
	id := b.RecoveryIDs()[1]
	r, data, err := s.Load(id)
	if err != nil || r.Stage != recovery.Prepared || !bytes.Equal(data, got[:n]) {
		t.Fatalf("snapshot not persisted: %v", err)
	}
	if err = b.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := len(remote.calls)
	if b.Dirty() {
		t.Fatal("still dirty")
	}
	if err = b.Sync(context.Background()); err != nil || len(remote.calls) != calls {
		t.Fatal("clean repeated sync made remote call")
	}
	if err = b.Truncate(0); err != nil || b.Size() != 0 {
		t.Fatal("truncate zero")
	}
	if err = b.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestBufferFailureFreezesAndRetainsData(t *testing.T) {
	remote := &fakeRemote{tag: "old", fail: "complete"}
	b, s := newTestBuffer(t, remote)
	_, _ = b.WriteAt([]byte("data"), 0)
	if err := b.Sync(context.Background()); !errors.Is(err, errUnknown) {
		t.Fatalf("sync %v", err)
	}
	calls := len(remote.calls)
	if err := b.Sync(context.Background()); !errors.Is(err, ErrRecoveryRequired) || len(remote.calls) != calls {
		t.Fatal("failure retried")
	}
	if _, err := b.WriteAt([]byte("x"), 0); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("write accepted after ambiguous failure")
	}
	ids := b.RecoveryIDs()
	record, data, err := s.Load(ids[len(ids)-1])
	if err != nil || record.Stage != recovery.UpdateStarted || !bytes.Equal(data, []byte("data")) {
		t.Fatalf("retained stage=%s error=%v", record.Stage, err)
	}
}
func TestBufferBoundsAndConcurrentWrites(t *testing.T) {
	b, _ := newTestBuffer(t, &fakeRemote{tag: "old"})
	if _, err := b.WriteAt([]byte("x"), -1); !errors.Is(err, syscall.EINVAL) {
		t.Fatal(err)
	}
	if err := b.Truncate(maxBuffer + 1); !errors.Is(err, syscall.EFBIG) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := b.WriteAt([]byte{byte('0' + i)}, int64(i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got := make([]byte, 4)
	if _, err := b.ReadAt(got, 0); err != nil || string(got) != "0123" {
		t.Fatal("concurrent writes not serialized")
	}
}

func TestBufferRecoveryChainAndPersistedSave(t *testing.T) {
	remote := &fakeRemote{tag: "old"}
	b, s := newTestBuffer(t, remote)
	if _, err := b.WriteAt([]byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteAt([]byte("y"), 1); err != nil {
		t.Fatal(err)
	}
	ids := b.RecoveryIDs()
	first, _, err := s.Load(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Load(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation != 0 || second.Generation != 1 || second.PreviousID != ids[0] {
		t.Fatal("recovery snapshot chain lost")
	}
	if err = b.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := b.RecoveryIDs()
	if len(after) != 2 {
		t.Fatal("save created an unlinked recovery entry")
	}
	second, _, err = s.Load(ids[1])
	if err != nil || second.Stage != recovery.Verified {
		t.Fatal("latest snapshot not marked verified")
	}
	if _, err = b.WriteAt([]byte("z"), 0); err != nil {
		t.Fatal(err)
	}
	newest, _, err := s.Load(b.RecoveryIDs()[2])
	if err != nil || newest.Generation != 0 || newest.BaseETag != "new" {
		t.Fatal("new base version not recorded")
	}
}

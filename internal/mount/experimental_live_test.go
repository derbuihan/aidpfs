package mount

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"aidpfs/internal/aidp"
	"aidpfs/internal/recovery"
	"aidpfs/internal/writeback"
)

func TestLiveFUSEIsolatedFixture(t *testing.T) {
	if os.Getenv("AIDPFS_LIVE_PAR_PROBE") != "isolated" {
		t.Skip("isolated live probe not requested")
	}
	root := os.Getenv("AIDPFS_PROBE_ROOT")
	if !regexp.MustCompile(`^/Workspace/Shared/aidpfs-probe-[0-9a-f]{32}$`).MatchString(root) {
		t.Fatal("invalid isolated probe root")
	}
	profile, region := os.Getenv("AIDPFS_PROBE_PROFILE"), os.Getenv("AIDPFS_PROBE_REGION")
	r, e := aidp.New(os.Getenv("AIDPFS_PROBE_ENDPOINT"), os.Getenv("AIDPFS_PROBE_PLATFORM"), os.Getenv("AIDPFS_PROBE_WORKSPACE"), root, aidp.APIKeySigner(os.Getenv("OCI_CONFIG_FILE"), profile))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	data := []byte{23, 0, 255, 42, 128, 91, 13}
	got, e := r.ReadFile(ctx, "upload-par.bin")
	if e != nil || !bytes.Equal(got, data) {
		t.Fatalf("fixture mismatch: %v", e)
	}
	journalDir := t.TempDir()
	if e = os.Chmod(journalDir, 0700); e != nil {
		t.Fatal(e)
	}
	journal, e := recovery.Open(journalDir)
	if e != nil {
		t.Fatal(e)
	}
	mountpoint := t.TempDir()
	server, e := mountExperimental(ctx, mountpoint, r, func(ctx context.Context, rel string) (*writeback.Buffer, error) {
		return r.OpenExperimentalBuffer(ctx, rel, region, journal)
	})
	if e != nil {
		t.Fatalf("live FUSE mount: %v", e)
	}
	defer func() {
		if err := server.Unmount(); err != nil {
			t.Errorf("unmount: %v", err)
		}
		server.Wait()
	}()
	// A clean last-close must eventually permit a fresh open after an external update.
	mountedFile := filepath.Join(mountpoint, "upload-par.bin")
	oldHandle, e := os.Open(mountedFile)
	if e != nil {
		t.Fatal(e)
	}
	if e = oldHandle.Close(); e != nil {
		t.Fatal(e)
	}
	externalBuffer, e := r.OpenExperimentalBuffer(ctx, "upload-par.bin", region, journal)
	if e != nil {
		t.Fatal(e)
	}
	if e = externalBuffer.Truncate(0); e != nil {
		t.Fatal(e)
	}
	if _, e = externalBuffer.WriteAt([]byte("external-update"), 0); e != nil {
		t.Fatal(e)
	}
	if e = externalBuffer.Sync(ctx); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(10 * time.Second)
	refreshed := false
	for time.Now().Before(deadline) {
		observed, err := os.ReadFile(mountedFile)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(observed, []byte("external-update")) {
			refreshed = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !refreshed {
		t.Fatal("reopen did not observe external change")
	}
	if e = externalBuffer.Truncate(0); e != nil {
		t.Fatal(e)
	}
	if _, e = externalBuffer.WriteAt(data, 0); e != nil {
		t.Fatal(e)
	}
	if e = externalBuffer.Sync(ctx); e != nil {
		t.Fatal(e)
	}
	// Wait for the previous read handle's release before opening for writes.
	deadline = time.Now().Add(10 * time.Second)
	refreshed = false
	for time.Now().Before(deadline) {
		observed, err := os.ReadFile(mountedFile)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(observed, data) {
			refreshed = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !refreshed {
		t.Fatal("reopen did not observe restored bytes")
	}
	f, e := os.OpenFile(mountedFile, os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	changed := []byte("fuse-test-data")
	if e = f.Truncate(0); e != nil {
		t.Fatal(e)
	}
	if _, e = f.WriteAt(changed, 0); e != nil {
		t.Fatal(e)
	}
	if e = f.Sync(); e != nil {
		t.Fatalf("FUSE fsync: %v", e)
	}
	got, e = r.ReadFile(ctx, "upload-par.bin")
	if e != nil || !bytes.Equal(got, changed) {
		t.Fatalf("remote verification: %v", e)
	}
	if e = f.Truncate(0); e != nil {
		t.Fatal(e)
	}
	if _, e = f.WriteAt(data, 0); e != nil {
		t.Fatal(e)
	}
	if e = f.Sync(); e != nil {
		t.Fatalf("FUSE restore: %v", e)
	}
	got, e = r.ReadFile(ctx, "upload-par.bin")
	if e != nil || !bytes.Equal(got, data) {
		t.Fatalf("restored verification: %v", e)
	}
	entries, e := journal.List()
	if e != nil || len(entries) < 2 {
		t.Fatalf("journal entries: %v", e)
	}
	// A second client wins after FUSE has buffered a different change.
	if e = f.Truncate(0); e != nil {
		t.Fatal(e)
	}
	local := []byte("local-unsaved-change")
	if _, e = f.WriteAt(local, 0); e != nil {
		t.Fatal(e)
	}
	other, e := r.OpenExperimentalBuffer(ctx, "upload-par.bin", region, journal)
	if e != nil {
		t.Fatal(e)
	}
	if e = other.Truncate(0); e != nil {
		t.Fatal(e)
	}
	if _, e = other.WriteAt([]byte("other-client"), 0); e != nil {
		t.Fatal(e)
	}
	if e = other.Sync(ctx); e != nil {
		t.Fatalf("external update: %v", e)
	}
	if e = f.Sync(); !errors.Is(e, syscall.ESTALE) {
		t.Fatalf("expected FUSE conflict: %v", e)
	}
	entries, e = journal.List()
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, entry := range entries {
		if entry.Invalid {
			continue
		}
		record, content, err := journal.Load(entry.ID)
		if err == nil && record.Stage == recovery.PutStarted && bytes.Equal(content, local) {
			found = true
		}
	}
	if !found {
		t.Fatal("conflicted local change missing from recovery")
	}
	if e = other.Truncate(0); e != nil {
		t.Fatal(e)
	}
	if _, e = other.WriteAt(data, 0); e != nil {
		t.Fatal(e)
	}
	if e = other.Sync(ctx); e != nil {
		t.Fatalf("external restore: %v", e)
	}
	got, e = r.ReadFile(ctx, "upload-par.bin")
	if e != nil || !bytes.Equal(got, data) {
		t.Fatalf("conflict restore verification: %v", e)
	}
	t.Log("live FUSE write, fsync, conflict recovery retention and restore succeeded")
}

package mount

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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
	// O_SYNC must complete its remote save before WriteAt returns.
	syncHandle, err := os.OpenFile(mountedFile, os.O_WRONLY|syscall.O_SYNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syncHandle.Close()
	if _, err = syncHandle.WriteAt([]byte{55}, 0); err != nil {
		t.Fatal(err)
	}
	expected := append([]byte(nil), data...)
	expected[0] = 55
	got, err = r.ReadFile(ctx, "upload-par.bin")
	if err != nil || !bytes.Equal(got, expected) {
		t.Fatalf("synchronous write not visible remotely: %v", err)
	}
	if _, err = syncHandle.WriteAt(data[:1], 0); err != nil {
		t.Fatal(err)
	}
	if err = syncHandle.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("live O_SYNC write visible before close")
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
	var conflictID string
	for _, entry := range entries {
		if entry.Invalid {
			continue
		}
		record, content, err := journal.Load(entry.ID)
		if err == nil && record.Stage == recovery.PutStarted && bytes.Equal(content, local) {
			found = true
			conflictID = entry.ID
		}
	}
	if !found {
		t.Fatal("conflicted local change missing from recovery")
	}
	// The CLI diagnosis generates a read PAR but must not replay the change.
	command := exec.CommandContext(ctx, "go", "run", "../../cmd/aidpfs", "recovery", "inspect", "--dir", journalDir, "--id", conflictID, "--region", region, "--profile", profile, "--endpoint", os.Getenv("AIDPFS_PROBE_ENDPOINT"))
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "observation=remote_changed") {
		t.Fatalf("recovery inspect failed: %v", err)
	}
	if strings.Contains(string(output), string(local)) {
		t.Fatal("recovery inspect leaked content")
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
	// A separate synthetic fixture is deleted while an open handle is dirty.
	deletedFile := filepath.Join(mountpoint, "live-delete.bin")
	dirty, err := os.OpenFile(deletedFile, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer dirty.Close()
	localDeleted := []byte("local-after-delete")
	if err = dirty.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err = dirty.WriteAt(localDeleted, 0); err != nil {
		t.Fatal(err)
	}
	endpoint := os.Getenv("AIDPFS_PROBE_ENDPOINT") + "/20260430/aiDataPlatforms/" + url.PathEscape(os.Getenv("AIDPFS_PROBE_PLATFORM")) + "/workspaces/" + url.PathEscape(os.Getenv("AIDPFS_PROBE_WORKSPACE")) + "/notebook/api/contents/" + url.PathEscape(strings.TrimPrefix(root+"/live-delete.bin", "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	if err = aidp.APIKeySigner(os.Getenv("OCI_CONFIG_FILE"), profile).Sign(req); err != nil {
		t.Fatal("sign delete failed")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("delete request failed")
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	st, err := dirty.Stat()
	if err != nil || st.Size() != int64(len(localDeleted)) {
		t.Fatalf("open handle stat after delete: %v", err)
	}
	if _, err = os.Stat(deletedFile); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("path stat after delete: %v", err)
	}
	if err = dirty.Sync(); err == nil {
		t.Fatal("sync unexpectedly succeeded after deletion")
	}
	if err = dirty.Close(); err == nil {
		t.Fatal("close unexpectedly succeeded after failed sync")
	}
	entries, err = journal.List()
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, entry := range entries {
		_, content, loadErr := journal.Load(entry.ID)
		if loadErr == nil && bytes.Equal(content, localDeleted) {
			found = true
		}
	}
	if !found {
		t.Fatal("deleted file dirty content missing from journal")
	}
	if _, err = r.Stat(ctx, "live-delete.bin"); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("deleted file unexpectedly present: %v", err)
	}
	t.Log("live deletion retained dirty journal; sync reported failure")
	t.Log("live FUSE write, fsync, conflict recovery retention and restore succeeded")
}

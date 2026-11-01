package aidp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"testing"
	"time"

	"aidpfs/internal/recovery"
	"aidpfs/internal/writeback"
)

// This integration test only runs inside the isolated probe's UUID directory.
// The caller owns and cleans the synthetic file; it must never target user data.
func TestLivePARIsolatedFixture(t *testing.T) {
	if os.Getenv("AIDPFS_LIVE_PAR_PROBE") != "isolated" {
		t.Skip("isolated live probe not requested")
	}
	root := os.Getenv("AIDPFS_PROBE_ROOT")
	if !regexp.MustCompile(`^/Workspace/Shared/aidpfs-probe-[0-9a-f]{32}$`).MatchString(root) {
		t.Fatal("invalid isolated probe root")
	}
	platform, key := os.Getenv("AIDPFS_PROBE_PLATFORM"), os.Getenv("AIDPFS_PROBE_WORKSPACE")
	profile, region := os.Getenv("AIDPFS_PROBE_PROFILE"), os.Getenv("AIDPFS_PROBE_REGION")
	endpoint := os.Getenv("AIDPFS_PROBE_ENDPOINT")
	r, err := New(endpoint, platform, key, root, APIKeySigner(os.Getenv("OCI_CONFIG_FILE"), profile))
	if err != nil {
		t.Fatal(err)
	}
	par, err := newOCIParTransport(region)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	original := []byte{23, 0, 255, 42, 128, 91, 13}
	changed := []byte("aidpfs-go-par-probe")
	meta, err := r.parMetadataRequest(ctx, par, "upload-par.bin", "DOWNLOAD", "", 0)
	if err != nil {
		t.Fatalf("download metadata: %v", err)
	}
	data, err := par.downloadVersioned(ctx, meta.URL, meta.ETag)
	if err != nil || !bytes.Equal(data, original) || meta.ETag == "" {
		t.Fatalf("download mismatch: %v", err)
	}
	refresh, err := r.parMetadataRequest(ctx, par, "upload-par.bin", "REFRESH", "", 0)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	old := meta.ETag
	next, err := par.putExisting(ctx, refresh.URL, old, changed)
	if err != nil {
		t.Fatalf("conditional PUT: %v", err)
	}
	if _, err = par.putExisting(ctx, refresh.URL, old, []byte("should-not-write")); !errors.Is(err, syscall.ESTALE) {
		t.Fatalf("stale ETag not rejected: %v", err)
	}
	if _, err = r.parMetadataRequest(ctx, par, "upload-par.bin", "UPDATE", next, int64(len(changed))); err != nil {
		t.Fatalf("close: %v", err)
	}
	data, err = r.ReadFile(ctx, "upload-par.bin")
	if err != nil || !bytes.Equal(data, changed) {
		t.Fatalf("updated read mismatch: %v", err)
	}
	// Restore the synthetic file so the enclosing probe can continue.
	refresh, err = r.parMetadataRequest(ctx, par, "upload-par.bin", "REFRESH", "", 0)
	if err != nil {
		t.Fatalf("restore refresh: %v", err)
	}
	restored, err := par.putExisting(ctx, refresh.URL, next, original)
	if err != nil {
		t.Fatalf("restore put: %v", err)
	}
	if _, err = r.parMetadataRequest(ctx, par, "upload-par.bin", "UPDATE", restored, int64(len(original))); err != nil {
		t.Fatalf("restore close: %v", err)
	}
	data, err = r.ReadFile(ctx, "upload-par.bin")
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("restore read mismatch: %v", err)
	}
	// Exercise durable writeback orchestration on this synthetic file.
	recoveryDir := t.TempDir()
	if err = os.Chmod(recoveryDir, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := recovery.Open(recoveryDir)
	if err != nil {
		t.Fatal(err)
	}
	saver := writeback.Saver{Journal: journal, Remote: &parWriter{reader: r, transport: par}}
	target := writeback.Target{PlatformID: platform, WorkspaceKey: key, RemotePath: root + "/upload-par.bin"}
	journalID, err := saver.SaveExisting(ctx, target, restored, []byte("journalled update"))
	if err != nil {
		t.Fatalf("journalled save failed (id retained=%v): %v", journalID != "", err)
	}
	record, contents, err := journal.Load(journalID)
	if err != nil || record.Stage != recovery.Verified || !bytes.Equal(contents, []byte("journalled update")) {
		t.Fatalf("journal verification failed: %v", err)
	}
	restoreID, err := saver.SaveExisting(ctx, target, record.PutETag, original)
	if err != nil {
		t.Fatalf("journalled restore failed (id retained=%v): %v", restoreID != "", err)
	}
	record, _, err = journal.Load(restoreID)
	if err != nil || record.Stage != recovery.Verified {
		t.Fatalf("restore journal failed: %v", err)
	}
	t.Log("journalled conditional save, verify and restore succeeded")
	// Two independent clients start with the same storage version.
	meta, err = r.parMetadataRequest(ctx, par, "upload-par.bin", "DOWNLOAD", "", 0)
	if err != nil {
		t.Fatalf("race metadata: %v", err)
	}
	first, err := r.parMetadataRequest(ctx, par, "upload-par.bin", "REFRESH", "", 0)
	if err != nil {
		t.Fatalf("race refresh 1: %v", err)
	}
	second, err := r.parMetadataRequest(ctx, par, "upload-par.bin", "REFRESH", "", 0)
	if err != nil {
		t.Fatalf("race refresh 2: %v", err)
	}
	type result struct {
		tag     string
		err     error
		content []byte
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for i, u := range []parURL{first.URL, second.URL} {
		payload := []byte{byte('A' + i), 1, 2, 3}
		go func(u parURL, payload []byte) {
			<-start
			tag, e := par.putExisting(ctx, u, meta.ETag, payload)
			results <- result{tag, e, payload}
		}(u, payload)
	}
	close(start)
	x, y := <-results, <-results
	successes, conflicts := 0, 0
	var winner result
	for _, result := range []result{x, y} {
		if result.err == nil {
			successes++
			winner = result
		} else if errors.Is(result.err, syscall.ESTALE) {
			conflicts++
		} else {
			t.Fatalf("concurrent transfer: %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent PUT: successes=%d conflicts=%d", successes, conflicts)
	}
	t.Log("concurrent conditional PUT: one success, one conflict")
	if _, err = r.parMetadataRequest(ctx, par, "upload-par.bin", "UPDATE", winner.tag, int64(len(winner.content))); err != nil {
		t.Fatalf("race close: %v", err)
	}
	data, err = r.ReadFile(ctx, "upload-par.bin")
	if err != nil || !bytes.Equal(data, winner.content) {
		t.Fatalf("winner read: %v", err)
	}
	refresh, err = r.parMetadataRequest(ctx, par, "upload-par.bin", "REFRESH", "", 0)
	if err != nil {
		t.Fatalf("race restore refresh: %v", err)
	}
	restored, err = par.putExisting(ctx, refresh.URL, winner.tag, original)
	if err != nil {
		t.Fatalf("race restore: %v", err)
	}
	if _, err = r.parMetadataRequest(ctx, par, "upload-par.bin", "UPDATE", restored, int64(len(original))); err != nil {
		t.Fatalf("race restore close: %v", err)
	}
	data, err = r.ReadFile(ctx, "upload-par.bin")
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("race restore read: %v", err)
	}
	exerciseLiveCrash(t, ctx, r, par, root, region, original)
}

// Child mutates only the UUID-scoped synthetic file, then waits before writing
// the PUT result to disk. The parent kills it and reconciles the result.
func TestLivePARCrashChild(t *testing.T) {
	if os.Getenv("AIDPFS_LIVE_CRASH") != "1" {
		t.Skip("live crash helper")
	}
	root := os.Getenv("AIDPFS_PROBE_ROOT")
	if !regexp.MustCompile(`^/Workspace/Shared/aidpfs-probe-[0-9a-f]{32}$`).MatchString(root) {
		t.Fatal("invalid fixture root")
	}
	r, e := New(os.Getenv("AIDPFS_PROBE_ENDPOINT"), os.Getenv("AIDPFS_PROBE_PLATFORM"), os.Getenv("AIDPFS_PROBE_WORKSPACE"), root, APIKeySigner(os.Getenv("OCI_CONFIG_FILE"), os.Getenv("AIDPFS_PROBE_PROFILE")))
	if e != nil {
		t.Fatal(e)
	}
	p, e := newOCIParTransport(os.Getenv("AIDPFS_PROBE_REGION"))
	if e != nil {
		t.Fatal(e)
	}
	journal, e := recovery.Open(os.Getenv("AIDPFS_LIVE_CRASH_DIR"))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	meta, e := r.parMetadataRequest(ctx, p, "upload-par.bin", "DOWNLOAD", "", 0)
	if e != nil {
		t.Fatal(e)
	}
	id, e := journal.Create(r.platformID, r.workspaceKey, root+"/upload-par.bin", meta.ETag, []byte("crash-synthetic"))
	if e != nil {
		t.Fatal(e)
	}
	upload, e := r.parMetadataRequest(ctx, p, "upload-par.bin", "REFRESH", "", 0)
	if e != nil {
		t.Fatal(e)
	}
	if e = journal.Advance(id, recovery.PutStarted, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = p.putExisting(ctx, upload.URL, meta.ETag, []byte("crash-synthetic")); e != nil {
		t.Fatal(e)
	}
	fmt.Println("PUT_ACCEPTED")
	time.Sleep(time.Hour)
}

func exerciseLiveCrash(t *testing.T, ctx context.Context, r *Reader, p *parTransport, root, region string, original []byte) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLivePARCrashChild$")
	child.Env = append(os.Environ(), "AIDPFS_LIVE_CRASH=1", "AIDPFS_LIVE_CRASH_DIR="+dir)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	signal := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			signal <- scanner.Text()
		} else {
			signal <- ""
		}
	}()
	select {
	case msg := <-signal:
		if msg != "PUT_ACCEPTED" {
			t.Fatal("child did not confirm PUT")
		}
	case <-ctx.Done():
		t.Fatal("child timed out")
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	journal, err := recovery.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := journal.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("crash journal list: %v", err)
	}
	record, data, err := journal.Load(entries[0].ID)
	if err != nil || record.Stage != recovery.PutStarted || !bytes.Equal(data, []byte("crash-synthetic")) {
		t.Fatalf("crash journal: %v", err)
	}
	observation, err := r.InspectRecovery(ctx, region, record, data)
	if err != nil || observation != writeback.ContentMatchUnknownVersion {
		t.Fatalf("crash observation %s: %v", observation, err)
	}
	meta, err := r.parMetadataRequest(ctx, p, "upload-par.bin", "DOWNLOAD", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := r.parMetadataRequest(ctx, p, "upload-par.bin", "REFRESH", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := p.putExisting(ctx, refresh.URL, meta.ETag, original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.parMetadataRequest(ctx, p, "upload-par.bin", "UPDATE", tag, int64(len(original))); err != nil {
		t.Fatal(err)
	}
	restored, err := r.ReadFile(ctx, "upload-par.bin")
	if err != nil || !bytes.Equal(restored, original) {
		t.Fatalf("crash fixture restore: %v", err)
	}
	t.Log("live PUT accepted then process killed: retained journal and ambiguous observation confirmed")
}

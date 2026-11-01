package aidp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"syscall"
	"testing"
	"time"
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
}

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
	data, err := par.download(ctx, meta.URL)
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
}

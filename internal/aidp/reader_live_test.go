package aidp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"

	"aidpfs/internal/backend"
)

// This opt-in probe reads one existing FILE and NOTEBOOK without printing or
// persisting their names or contents. It is skipped in ordinary offline tests.
func TestLiveReadOnlyWorkspace(t *testing.T) {
	if os.Getenv("AIDPFS_LIVE_READ_PROBE") != "enabled" {
		t.Skip("read-only AIDP probe not requested")
	}
	platform, workspace := os.Getenv("AIDPFS_PROBE_PLATFORM"), os.Getenv("AIDPFS_PROBE_WORKSPACE")
	profile, region, endpoint := os.Getenv("AIDPFS_PROBE_PROFILE"), os.Getenv("AIDPFS_PROBE_REGION"), os.Getenv("AIDPFS_PROBE_ENDPOINT")
	if platform == "" || workspace == "" || profile == "" || region == "" || endpoint == "" {
		t.Fatal("live probe configuration is incomplete")
	}
	r, err := New(endpoint, platform, workspace, "/Workspace", APIKeySigner(os.Getenv("OCI_CONFIG_FILE"), profile))
	if err != nil {
		t.Fatal("invalid live probe configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	type candidate struct {
		rel  string
		meta backend.Metadata
	}
	found := map[backend.Kind]candidate{}
	queue := []string{""}
	visited := map[string]bool{}
	for len(queue) > 0 && len(visited) < 30 && (found[backend.File].rel == "" || found[backend.Notebook].rel == "") {
		dir := queue[0]
		queue = queue[1:]
		if visited[dir] {
			continue
		}
		visited[dir] = true
		entries, err := r.ReadDir(ctx, dir)
		if err != nil {
			t.Fatalf("list workspace subtree: %v", err)
		}
		for _, entry := range entries {
			rel := entry.Name
			if dir != "" {
				rel = path.Join(dir, entry.Name)
			}
			switch entry.Kind {
			case backend.Directory:
				queue = append(queue, rel)
			case backend.File, backend.Notebook:
				if _, ok := found[entry.Kind]; !ok {
					found[entry.Kind] = candidate{rel: rel, meta: entry}
				}
			}
		}
	}
	if found[backend.File].rel == "" || found[backend.Notebook].rel == "" {
		t.Fatalf("workspace sample types unavailable (visited %d directories)", len(visited))
	}
	for _, kind := range []backend.Kind{backend.File, backend.Notebook} {
		sample := found[kind]
		content, data, err := r.fetch(ctx, sample.rel, true)
		if err != nil {
			t.Fatalf("read existing %s: %v", kindName(kind), err)
		}
		stat, err := r.Stat(ctx, sample.rel)
		if err != nil {
			t.Fatalf("stat existing %s: %v", kindName(kind), err)
		}
		wantType := "file"
		if kind == backend.Notebook {
			wantType = "notebook"
		}
		if content.Type != wantType || stat.Kind != kind || stat.Size != int64(len(data)) || !content.Modified.Equal(sample.meta.Modified) {
			t.Fatalf("existing %s metadata/content mismatch", kindName(kind))
		}
		again, err := r.ReadFile(ctx, sample.rel)
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("existing %s repeated read mismatch: %v", kindName(kind), err)
		}
		t.Logf("%s: format=%s bytes=%d size_matches=true modified_matches_object=true composite_etag_present=%t", kindName(kind), content.Format, len(data), sample.meta.Version != "")
	}
}

// TestLiveNotebookAutosaveMarker verifies a Workbench auto-save from a
// dedicated synthetic notebook using the signed contents API. The marker is
// supplied by the caller and is never included in test output.
func TestLiveNotebookAutosaveMarker(t *testing.T) {
	if os.Getenv("AIDPFS_LIVE_NOTEBOOK_AUTOSAVE") != "enabled" {
		t.Skip("Workbench auto-save probe not requested")
	}
	root := os.Getenv("AIDPFS_PROBE_ROOT")
	if !regexp.MustCompile(`^/Workspace/Shared/aidpfs-ui-probe-[0-9a-f]{32}$`).MatchString(root) {
		t.Fatal("auto-save probe requires the dedicated aidpfs-ui-probe subtree")
	}
	name := os.Getenv("AIDPFS_LIVE_NOTEBOOK_NAME")
	marker := os.Getenv("AIDPFS_LIVE_NOTEBOOK_MARKER")
	if name == "" || strings.ContainsAny(name, "/\\") || marker == "" {
		t.Fatal("auto-save probe requires a notebook name and marker")
	}
	platform, workspace := os.Getenv("AIDPFS_PROBE_PLATFORM"), os.Getenv("AIDPFS_PROBE_WORKSPACE")
	profile, region, endpoint := os.Getenv("AIDPFS_PROBE_PROFILE"), os.Getenv("AIDPFS_PROBE_REGION"), os.Getenv("AIDPFS_PROBE_ENDPOINT")
	if platform == "" || workspace == "" || profile == "" || region == "" || endpoint == "" {
		t.Fatal("live probe configuration is incomplete")
	}
	r, err := New(endpoint, platform, workspace, root, APIKeySigner(os.Getenv("OCI_CONFIG_FILE"), profile))
	if err != nil {
		t.Fatal("invalid live probe configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	data, err := r.ReadFile(ctx, name)
	if err != nil {
		t.Fatalf("read synthetic notebook after Workbench auto-save: %v", err)
	}
	if !bytes.Contains(data, []byte(marker)) {
		t.Fatal("signed contents API does not contain the auto-save marker")
	}
	t.Log("signed contents API contains the synthetic auto-save marker")
}

// TestLivePARExpiry observes a short-lived download PAR until Object Storage
// rejects it. It is opt-in and accepts only a FILE under the dedicated
// synthetic UI probe root. The bearer URL and response body are never logged.
func TestLivePARExpiry(t *testing.T) {
	if os.Getenv("AIDPFS_LIVE_PAR_EXPIRY") != "enabled" {
		t.Skip("live PAR expiry probe not requested")
	}
	root := os.Getenv("AIDPFS_PROBE_ROOT")
	if !regexp.MustCompile(`^/Workspace/Shared/aidpfs-ui-probe-[0-9a-f]{32}$`).MatchString(root) {
		t.Fatal("PAR expiry probe requires the dedicated aidpfs-ui-probe subtree")
	}
	name := os.Getenv("AIDPFS_LIVE_PAR_EXPIRY_FILE")
	if name == "" || strings.ContainsAny(name, "/\\") {
		t.Fatal("PAR expiry probe requires a FILE basename")
	}
	platform, workspace := os.Getenv("AIDPFS_PROBE_PLATFORM"), os.Getenv("AIDPFS_PROBE_WORKSPACE")
	profile, region, endpoint := os.Getenv("AIDPFS_PROBE_PROFILE"), os.Getenv("AIDPFS_PROBE_REGION"), os.Getenv("AIDPFS_PROBE_ENDPOINT")
	if platform == "" || workspace == "" || profile == "" || region == "" || endpoint == "" {
		t.Fatal("live probe configuration is incomplete")
	}
	r, err := New(endpoint, platform, workspace, root, APIKeySigner(os.Getenv("OCI_CONFIG_FILE"), profile))
	if err != nil {
		t.Fatal("invalid live probe configuration")
	}
	transport, err := newOCIParTransport(region)
	if err != nil {
		t.Fatal("invalid live PAR transport configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	issuedAt := time.Now()
	meta, err := r.parMetadataRequest(ctx, transport, name, "DOWNLOAD", "", 0)
	if err != nil {
		t.Fatalf("generate synthetic FILE download PAR: %v", err)
	}
	status, err := parExpiryStatus(ctx, transport, meta.URL)
	if err != nil || (status != http.StatusOK && status != http.StatusPartialContent) {
		t.Fatalf("new PAR initial status = %d, request failed=%t", status, err != nil)
	}
	t.Log("new PAR is usable; waiting for expiration without logging its URL or contents")
	for {
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			t.Fatalf("PAR was still usable after %s", time.Since(issuedAt).Round(time.Second))
		case <-timer.C:
		}
		status, err = parExpiryStatus(ctx, transport, meta.URL)
		if err != nil {
			continue
		}
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			t.Logf("PAR rejected after %s with HTTP %d", time.Since(issuedAt).Round(time.Second), status)
			return
		case http.StatusOK, http.StatusPartialContent:
			continue
		default:
			if status == http.StatusTooManyRequests || status >= http.StatusInternalServerError {
				continue
			}
			t.Fatalf("unexpected PAR probe status HTTP %d", status)
		}
	}
}

func parExpiryStatus(ctx context.Context, transport *parTransport, capability parURL) (int, error) {
	if _, err := transport.validate(capability.raw); err != nil {
		return 0, fmt.Errorf("invalid PAR capability")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, capability.raw, nil)
	if err != nil {
		return 0, fmt.Errorf("invalid PAR request")
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := transport.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("PAR status request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
	return resp.StatusCode, nil
}

func kindName(kind backend.Kind) string {
	switch kind {
	case backend.File:
		return "FILE"
	case backend.Notebook:
		return "NOTEBOOK"
	default:
		return fmt.Sprintf("kind-%d", kind)
	}
}

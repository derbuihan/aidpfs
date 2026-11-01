package aidp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
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

package aidp

import (
	"aidpfs/internal/recovery"
	"aidpfs/internal/writeback"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func TestPARWriterRejectsDifferentTarget(t *testing.T) {
	r := serverReader(t, nil)
	w := parWriter{reader: r}
	for _, target := range []writeback.Target{
		{PlatformID: "other", WorkspaceKey: "workspace", RemotePath: "/Workspace/a"},
		{PlatformID: "platform", WorkspaceKey: "other", RemotePath: "/Workspace/a"},
		{PlatformID: "platform", WorkspaceKey: "workspace", RemotePath: "/Workspace2/a"},
		{PlatformID: "platform", WorkspaceKey: "workspace", RemotePath: "/Workspace/../outside"},
	} {
		if _, err := w.relative(target); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("unexpected target: %v", err)
		}
	}
}

func TestExperimentalBufferRejectsNotebook(t *testing.T) {
	r := serverReader(t, func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"items":[{"displayName":"test.ipynb","path":"/Workspace/test.ipynb","type":"NOTEBOOK"}]}`)
	})
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := recovery.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.OpenExperimentalBuffer(context.Background(), "test.ipynb", "ap-osaka-1", journal); !errors.Is(err, syscall.EOPNOTSUPP) {
		t.Fatalf("notebook accepted: %v", err)
	}
}

func TestPARWriterSaveVerifyAndSnapshot(t *testing.T) {
	var mu sync.Mutex
	remoteData := []byte("old")
	remoteETag := "old-tag"
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if req.URL.Path != "/p/capability" {
			t.Errorf("unexpected object path %q", req.URL.Path)
		}
		switch req.Method {
		case http.MethodPut:
			if got := req.Header.Get("If-Match"); got != remoteETag {
				t.Errorf("If-Match = %q, want %q", got, remoteETag)
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Errorf("read upload: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			remoteData = body
			remoteETag = "new-tag"
			w.Header().Set("ETag", `"new-tag"`)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			w.Header().Set("ETag", `"`+remoteETag+`"`)
			_, _ = w.Write(remoteData)
		default:
			t.Errorf("unexpected object method %s", req.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer storage.Close()
	transport, err := newPARTransport(storage.URL, storage.Client())
	if err != nil {
		t.Fatal(err)
	}
	var metadataETag = "old-tag"
	var metadataSize int64 = 3
	var listedKind = "FILE"
	r := serverReader(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/downloadFileMeta"):
			fmt.Fprintf(w, `{"parUrl":%q,"eTag":%q,"size":%d}`, storage.URL+"/p/capability", metadataETag, metadataSize)
		case strings.HasSuffix(req.URL.Path, "/uploadFileMeta"):
			var payload struct {
				Action string `json:"action"`
				ETag   string `json:"eTag"`
				Size   int64  `json:"size"`
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Errorf("decode upload metadata: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch payload.Action {
			case "REFRESH":
				fmt.Fprintf(w, `{"parUrl":%q,"eTag":"old-tag"}`, storage.URL+"/p/capability")
			case "UPDATE":
				if payload.ETag != `"new-tag"` || payload.Size != 4 {
					t.Errorf("unexpected update metadata: %#v", payload)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				fmt.Fprint(w, `{"eTag":"updated"}`)
			default:
				t.Errorf("unexpected metadata action %q", payload.Action)
				w.WriteHeader(http.StatusBadRequest)
			}
		case strings.HasSuffix(req.URL.Path, "/objects"):
			fmt.Fprintf(w, `{"items":[{"displayName":"file","path":"/Workspace/file","type":%q}]}`, listedKind)
		default:
			t.Errorf("unexpected metadata endpoint %s", req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	w := &parWriter{reader: r, transport: transport}
	target := writeback.Target{PlatformID: "platform", WorkspaceKey: "workspace", RemotePath: "/Workspace/file"}
	upload, err := w.Refresh(context.Background(), target)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	newTag, err := upload.Put(context.Background(), "old-tag", []byte("next"))
	if err != nil || newTag != `"new-tag"` {
		t.Fatalf("conditional upload tag=%q error=%v", newTag, err)
	}
	if err = w.Complete(context.Background(), target, newTag, 4); err != nil {
		t.Fatalf("complete update: %v", err)
	}
	metadataETag, metadataSize = "new-tag", 4
	if err = w.Verify(context.Background(), target, newTag, []byte("next")); err != nil {
		t.Fatalf("verify saved bytes: %v", err)
	}
	snapshot, err := w.Snapshot(context.Background(), target)
	if err != nil || snapshot.ETag != "new-tag" || !bytes.Equal(snapshot.Data, []byte("next")) {
		t.Fatalf("snapshot=%+v error=%v", snapshot, err)
	}

	metadataETag = "different"
	if err = w.Verify(context.Background(), target, newTag, []byte("next")); !errors.Is(err, syscall.ESTALE) {
		t.Fatalf("verify mismatched version = %v, want ESTALE", err)
	}
	metadataETag = "new-tag"
	metadataSize = 99
	if err = w.Verify(context.Background(), target, newTag, []byte("next")); !errors.Is(err, syscall.ESTALE) {
		t.Fatalf("verify mismatched size = %v, want ESTALE", err)
	}
	metadataSize = 4
	mu.Lock()
	remoteData = []byte("lost")
	mu.Unlock()
	if err = w.Verify(context.Background(), target, newTag, []byte("next")); !errors.Is(err, syscall.ESTALE) {
		t.Fatalf("verify mismatched bytes = %v, want ESTALE", err)
	}
	listedKind = "NOTEBOOK"
	if _, err = w.Snapshot(context.Background(), target); !errors.Is(err, syscall.EOPNOTSUPP) {
		t.Fatalf("snapshot Notebook = %v, want EOPNOTSUPP", err)
	}
}

func TestOpenExperimentalBufferRejectsInvalidOrUnversionedTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := recovery.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := serverReader(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/objects") {
			fmt.Fprint(w, `{"items":[{"displayName":"file","path":"/Workspace/file","type":"FILE"}]}`)
			return
		}
		if strings.HasSuffix(req.URL.Path, "/downloadFileMeta") {
			fmt.Fprint(w, `{"parUrl":"https://objectstorage.ap-osaka-1.oraclecloud.com/p/not-used"}`)
			return
		}
		t.Errorf("unexpected request %s", req.URL.Path)
	})
	for _, tc := range []struct {
		name    string
		rel     string
		journal *recovery.Store
	}{
		{name: "nil journal", rel: "file"},
		{name: "empty path", journal: journal},
		{name: "path escape", rel: "../outside", journal: journal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.OpenExperimentalBuffer(context.Background(), tc.rel, "ap-osaka-1", tc.journal); !errors.Is(err, syscall.EINVAL) {
				t.Fatalf("OpenExperimentalBuffer error = %v, want EINVAL", err)
			}
		})
	}
	if _, err = r.OpenExperimentalBuffer(context.Background(), "file", "ap-osaka-1", journal); !errors.Is(err, syscall.ESTALE) {
		t.Fatalf("unversioned FILE error = %v, want ESTALE", err)
	}
	for _, region := range []string{"", "ap_osaka_1", "https://example.invalid"} {
		if _, err = newOCIParTransport(region); err == nil {
			t.Errorf("invalid region %q accepted", region)
		}
	}
	if _, err = newOCIParTransport("ap-osaka-1"); err != nil {
		t.Fatalf("valid OCI region rejected: %v", err)
	}
}

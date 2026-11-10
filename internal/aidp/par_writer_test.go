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

	"aidpfs/internal/backend"
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

// Notebooks are opened through the PAR protocol with type NOTEBOOK; folders
// are still rejected before any PAR request.
func TestExperimentalBufferNotebookUsesNotebookType(t *testing.T) {
	var types []string
	listed := "NOTEBOOK"
	r := serverReader(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/objects") {
			fmt.Fprintf(w, `{"items":[{"displayName":"test.ipynb","path":"/Workspace/test.ipynb","type":%q}]}`, listed)
			return
		}
		types = append(types, req.Header.Get("type"))
		fmt.Fprint(w, `{"parUrl":"https://elsewhere.example/p","eTag":"e","size":2}`)
	})
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := recovery.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The mock's PAR origin is rejected, after the NOTEBOOK metadata request.
	if _, err = r.OpenExperimentalBuffer(context.Background(), "test.ipynb", "ap-osaka-1", journal); err == nil || errors.Is(err, syscall.EOPNOTSUPP) {
		t.Fatalf("notebook open: %v", err)
	}
	if len(types) != 1 || types[0] != "NOTEBOOK" {
		t.Fatalf("metadata types = %v", types)
	}
	listed = "FOLDER"
	if _, err = r.OpenExperimentalBuffer(context.Background(), "test.ipynb", "ap-osaka-1", journal); !errors.Is(err, syscall.EOPNOTSUPP) || len(types) != 1 {
		t.Fatalf("folder open: %v types %v", err, types)
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
	listedKind = "FOLDER"
	if _, err = w.Snapshot(context.Background(), target); !errors.Is(err, syscall.EOPNOTSUPP) {
		t.Fatalf("snapshot FOLDER = %v, want EOPNOTSUPP", err)
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

func TestValidNotebook(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		ok   bool
	}{
		{"empty", `{"cells":[],"metadata":{},"nbformat":4,"nbformat_minor":5}`, true},
		{"cells", `{"cells":[{"cell_type":"code","source":"x","metadata":{},"outputs":[],"execution_count":null},{"cell_type":"markdown","source":[],"metadata":{}},{"cell_type":"raw","source":"","metadata":{}}],"metadata":{"k":1},"nbformat":4,"nbformat_minor":4,"extra":true}`, true},
		{"truncated", `{"cells":[`, false},
		{"empty bytes", ``, false},
		{"trailing data", `{"cells":[],"metadata":{},"nbformat":4,"nbformat_minor":5} {}`, false},
		{"no cells", `{"metadata":{},"nbformat":4,"nbformat_minor":5}`, false},
		{"nbformat 3", `{"cells":[],"metadata":{},"nbformat":3,"nbformat_minor":0}`, false},
		{"bad cell type", `{"cells":[{"cell_type":"heading"}],"metadata":{},"nbformat":4,"nbformat_minor":5}`, false},
		{"array", `[]`, false},
	} {
		if got := validNotebook([]byte(tc.data)) == nil; got != tc.ok {
			t.Errorf("%s: valid=%t want %t", tc.name, got, tc.ok)
		}
	}
	w := &parWriter{}
	if w.Validate([]byte("anything")) != nil {
		t.Error("FILE writer validated content")
	}
	w.kind = backend.Notebook
	if w.Validate([]byte("anything")) == nil {
		t.Error("Notebook writer accepted invalid content")
	}
}

package aidp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"aidpfs/internal/backend"
)

func TestPARMetadataRefreshAndUpdate(t *testing.T) {
	storage := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer storage.Close()
	p, e := newPARTransport(storage.URL, storage.Client())
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	r := serverReader(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Header.Get("Content-Type") != "application/json" {
			t.Error("missing signed content type")
		}
		if strings.HasSuffix(req.URL.Path, "/downloadFileMeta") {
			fmt.Fprintf(w, `{"parUrl":%q,"eTag":"etag"}`, storage.URL+"/p/secret")
			return
		}
		if req.Method != "POST" || req.Header.Get("path") != "/Workspace/a %#" {
			t.Error("invalid metadata request")
		}
		var payload map[string]any
		_ = json.NewDecoder(req.Body).Decode(&payload)
		if payload["action"] == "REFRESH" {
			fmt.Fprintf(w, `{"parUrl":%q,"eTag":"etag"}`, storage.URL+"/p/secret?token=private")
			return
		}
		if payload["action"] != "UPDATE" || payload["size"] != float64(7) || payload["eTag"] != "new" {
			t.Error("invalid update payload")
		}
		fmt.Fprint(w, `{"eTag":"new"}`)
	})
	_, e = r.parMetadataRequest(context.Background(), p, "a %#", "DOWNLOAD", "", 0)
	if e != nil {
		t.Fatalf("download metadata: %v", e)
	}
	got, e := r.parMetadataRequest(context.Background(), p, "a %#", "REFRESH", "", 0)
	if e != nil || got.ETag != "etag" {
		t.Fatalf("refresh %v", e)
	}
	if strings.Contains(fmt.Sprintf("%v", got.URL), "secret") {
		t.Fatal("secret leaked")
	}
	_, e = r.parMetadataRequest(context.Background(), p, "a %#", "UPDATE", "new", 7)
	if e != nil || calls.Load() != 3 {
		t.Fatalf("update %v", e)
	}
	if _, e = r.parMetadataRequest(context.Background(), p, "../escape", "REFRESH", "", 0); !errors.Is(e, syscall.EINVAL) {
		t.Fatalf("escape %v", e)
	}
}
func TestPARMetadataDoesNotRetryAmbiguousPost(t *testing.T) {
	storage := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer storage.Close()
	p, _ := newPARTransport(storage.URL, storage.Client())
	var calls atomic.Int32
	r := serverReader(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(503) })
	_, e := r.parMetadataRequest(context.Background(), p, "file", "REFRESH", "", 0)
	if !errors.Is(e, ErrOutcomeUnknown) || calls.Load() != 1 {
		t.Fatalf("calls %d err %v", calls.Load(), e)
	}
}

func TestPARMetadataNotebookType(t *testing.T) {
	storage := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer storage.Close()
	p, _ := newPARTransport(storage.URL, storage.Client())
	var types []string
	r := serverReader(t, func(w http.ResponseWriter, req *http.Request) {
		types = append(types, req.Header.Get("type"))
		fmt.Fprintf(w, `{"parUrl":%q,"eTag":"etag","size":2}`, storage.URL+"/p/x")
	})
	if _, e := r.parMetadataRequestKind(context.Background(), p, "n.ipynb", "DOWNLOAD", "", 0, backend.Notebook); e != nil {
		t.Fatal(e)
	}
	if _, e := r.parMetadataRequest(context.Background(), p, "f.txt", "DOWNLOAD", "", 0); e != nil {
		t.Fatal(e)
	}
	if _, e := r.parMetadataRequestKind(context.Background(), p, "d", "DOWNLOAD", "", 0, backend.Directory); !errors.Is(e, syscall.EINVAL) {
		t.Fatalf("directory kind: %v", e)
	}
	if strings.Join(types, ",") != "NOTEBOOK,FILE" {
		t.Fatalf("types = %v", types)
	}
}

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
	got, e := r.parMetadataRequest(context.Background(), p, "a %#", "REFRESH", "", 0)
	if e != nil || got.ETag != "etag" {
		t.Fatalf("refresh %v", e)
	}
	if strings.Contains(fmt.Sprintf("%v", got.URL), "secret") {
		t.Fatal("secret leaked")
	}
	_, e = r.parMetadataRequest(context.Background(), p, "a %#", "UPDATE", "new", 7)
	if e != nil || calls.Load() != 2 {
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

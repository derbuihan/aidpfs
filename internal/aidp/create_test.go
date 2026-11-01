package aidp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestCreateEmptyExperimentalSingleAttempt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   error
	}{{"created", 200, nil}, {"exists", 409, syscall.EEXIST}, {"unknown", 500, ErrOutcomeUnknown}, {"timeout", 408, ErrOutcomeUnknown}, {"forbidden", 403, syscall.EACCES}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.Header.Get("path") != "/Workspace/f" || r.Header.Get("type") != "FILE" || r.Header.Get("is-overwrite") != "false" || r.Header.Get("Content-Type") != "application/octet-stream" {
					t.Error("incorrect create request")
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			reader, err := NewWithClient(server.URL, "platform", "workspace", "/Workspace", noSigner{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			err = reader.CreateEmptyExperimental(context.Background(), "f")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("attempts: %d", calls.Load())
			}
			if err = reader.CreateEmptyExperimental(context.Background(), "../f"); !errors.Is(err, syscall.EINVAL) {
				t.Fatalf("path accepted: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatal("invalid path sent")
			}
		})
	}
}

func TestCreateEmptyExperimentalDisconnectIsUnknown(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer server.Close()
	reader, err := NewWithClient(server.URL, "platform", "workspace", "/Workspace", noSigner{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err = reader.CreateEmptyExperimental(context.Background(), "f"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("error %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("attempts: %d", calls.Load())
	}
}

func TestCreateDirectoryExperimental(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("type") != "FOLDER" || r.Header.Get("is-overwrite") != "false" {
			t.Error("incorrect directory create request")
		}
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	reader, err := NewWithClient(server.URL, "platform", "workspace", "/Workspace", noSigner{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err = reader.CreateDirectoryExperimental(context.Background(), "dir"); !errors.Is(err, syscall.EEXIST) || calls.Load() != 1 {
		t.Fatalf("error %v calls %d", err, calls.Load())
	}
}

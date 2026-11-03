package aidp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestFileMutationSingleAttempt(t *testing.T) {
	for _, tc := range []struct {
		op     string
		status int
		want   error
	}{
		{"rename", 200, nil}, {"rename", 409, syscall.EEXIST}, {"unlink", 204, nil}, {"unlink", 202, ErrOutcomeUnknown}, {"unlink", 500, ErrOutcomeUnknown},
	} {
		t.Run(tc.op+http.StatusText(tc.status), func(t *testing.T) {
			var mutations atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"items":[{"displayName":"a","path":"/Workspace/a","type":"FILE"}]}`))
					return
				}
				mutations.Add(1)
				if tc.op == "rename" {
					if r.Method != http.MethodPatch {
						t.Errorf("method %s", r.Method)
					}
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["path"] != "Workspace/b" {
						t.Errorf("bad path")
					}
				} else if r.Method != http.MethodDelete {
					t.Errorf("method %s", r.Method)
				}
				w.WriteHeader(tc.status)
			}))
			defer s.Close()
			r, err := NewWithClient(s.URL, "p", "w", "/Workspace", noSigner{}, s.Client())
			if err != nil {
				t.Fatal(err)
			}
			dest := ""
			if tc.op == "rename" {
				dest = "b"
			}
			if err = r.MutateFile(context.Background(), tc.op, "a", dest); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if mutations.Load() != 1 {
				t.Fatalf("attempts %d", mutations.Load())
			}
		})
	}
}

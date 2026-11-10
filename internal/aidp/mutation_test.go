package aidp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"

	"aidpfs/internal/backend"
)

func TestFileMutationSingleAttempt(t *testing.T) {
	for _, tc := range []struct {
		op         string
		status     int
		body       string
		want       error
		notApplied bool
	}{
		{"rename", 200, "", nil, false},
		{"rename", 409, "", syscall.EEXIST, true},
		{"rename", 400, `{"code":"InvalidParameter","message":"Invalid resource name."}`, syscall.EINVAL, true},
		{"rename", 403, `{"code":"NotAllowed"}`, syscall.EACCES, true},
		{"rename", 404, `{"code":"NotAuthorizedOrNotFound"}`, syscall.ENOENT, true},
		{"rename", 500, `{"code":"InternalError","message":"(409, Lock already held, false)"}`, ErrOutcomeUnknown, false},
		{"unlink", 204, "", nil, false},
		{"unlink", 202, "", ErrOutcomeUnknown, false},
		{"unlink", 500, "", ErrOutcomeUnknown, false},
	} {
		t.Run(tc.op+fmt.Sprint(tc.status), func(t *testing.T) {
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
				_, _ = w.Write([]byte(tc.body))
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
			var rejected *backend.NotAppliedError
			if errors.As(err, &rejected) != tc.notApplied {
				t.Fatalf("not applied = %t, want %t (%v)", !tc.notApplied, tc.notApplied, err)
			}
			if mutations.Load() != 1 {
				t.Fatalf("attempts %d", mutations.Load())
			}
		})
	}
}

func TestRenameToInvalidNameSendsNoRequest(t *testing.T) {
	var mutations atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"items":[{"displayName":"a","path":"/Workspace/a","type":"FILE"}]}`))
			return
		}
		mutations.Add(1)
	}))
	defer s.Close()
	r, err := NewWithClient(s.URL, "p", "w", "/Workspace", noSigner{}, s.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = r.MutateFile(context.Background(), "rename", "a", "日本.txt")
	var rejected *backend.NotAppliedError
	if !errors.Is(err, syscall.EINVAL) || !errors.As(err, &rejected) || mutations.Load() != 0 {
		t.Fatalf("err=%v attempts=%d", err, mutations.Load())
	}
}

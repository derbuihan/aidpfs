package aidp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestPARConditionalTransfer(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials forwarded")
		}
		if r.Method == http.MethodGet {
			fmt.Fprint(w, "abc")
			return
		}
		if r.Header.Get("If-Match") != "old" {
			w.WriteHeader(412)
			return
		}
		w.Header().Set("ETag", "new")
		w.WriteHeader(200)
	}))
	defer s.Close()
	p, e := newPARTransport(s.URL, s.Client())
	if e != nil {
		t.Fatal(e)
	}
	u, e := p.validate(s.URL + "/p/bearer-secret?token=sensitive")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", u, u), "bearer-secret") {
		t.Fatal("PAR leaked")
	}
	b, e := p.download(context.Background(), u)
	if e != nil || string(b) != "abc" {
		t.Fatalf("download: %v", e)
	}
	tag, e := p.putExisting(context.Background(), u, "old", []byte("new bytes"))
	if e != nil || tag != "new" {
		t.Fatalf("put: %v", e)
	}
	_, e = p.putExisting(context.Background(), u, "wrong", nil)
	if !errors.Is(e, syscall.ESTALE) {
		t.Fatalf("conflict %v", e)
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected attempts %d", calls.Load())
	}
}
func TestPARRejectsOriginRedirectAndSecrets(t *testing.T) {
	var visited atomic.Bool
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { visited.Store(true) }))
	defer other.Close()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/p/secret", http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	p, e := newPARTransport(s.URL, s.Client())
	if e != nil {
		t.Fatal(e)
	}
	for _, candidate := range []string{"https://evil.example/p/token", s.URL + "/elsewhere/token", strings.Replace(s.URL, "https://", "http://", 1) + "/p/token", s.URL + "/p/token#fragment"} {
		if _, e := p.validate(candidate); e == nil || strings.Contains(e.Error(), "token") {
			t.Fatalf("accepted or leaked URL: %v", e)
		}
	}
	u, _ := p.validate(s.URL + "/p/secret")
	_, e = p.putExisting(context.Background(), u, "old", []byte("x"))
	if e == nil || visited.Load() {
		t.Fatal("redirect was followed")
	}
	if strings.Contains(e.Error(), "secret") {
		t.Fatal("error leaked secret")
	}
}
func TestPARUnknownOutcomeNeverRetried(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer s.Close()
	p, _ := newPARTransport(s.URL, s.Client())
	u, _ := p.validate(s.URL + "/p/secret")
	_, e := p.putExisting(context.Background(), u, "etag", nil)
	if !errors.Is(e, ErrOutcomeUnknown) || calls.Load() != 1 {
		t.Fatalf("calls %d error %v", calls.Load(), e)
	}
}

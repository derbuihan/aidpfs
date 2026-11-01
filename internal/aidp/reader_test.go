package aidp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"aidpfs/internal/backend"
)

type noSigner struct{}

func (noSigner) Sign(*http.Request) error { return nil }
func serverReader(t *testing.T, h http.HandlerFunc) *Reader {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	r, e := NewWithClient(s.URL, "platform", "workspace", "/Workspace", noSigner{}, s.Client())
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestPagingAndPaths(t *testing.T) {
	r := serverReader(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") != "/Workspace/日本 語%#" {
			t.Error("incorrect query path")
		}
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("opc-next-page", "next%token")
			fmt.Fprint(w, `{"items":[{"displayName":"one","path":"/Workspace/日本 語%#/one","type":"FILE"}]}`)
			return
		}
		if r.URL.Query().Get("page") != "next%token" {
			t.Error("incorrect token")
		}
		fmt.Fprint(w, `{"items":[]}`)
	})
	entries, e := r.ReadDir(context.Background(), "日本 語%#")
	if e != nil || len(entries) != 1 || entries[0].Name != "one" {
		t.Fatalf("entries=%v error=%v", entries, e)
	}
	if _, e = r.ReadDir(context.Background(), "../escape"); !errors.Is(e, syscall.EINVAL) {
		t.Fatalf("unexpected error %v", e)
	}
}
func TestContentAndSize(t *testing.T) {
	for _, tt := range []struct {
		format, content, want, typ string
		kind                       backend.Kind
	}{
		{"text", `"hello 日本"`, "hello 日本", "file", backend.File},
		{"base64", `"AAH/"`, string([]byte{0, 1, 255}), "file", backend.File},
		{"json", `{"cells":[],"metadata":{}}`, `{"cells":[],"metadata":{}}`, "notebook", backend.Notebook},
	} {
		t.Run(tt.format, func(t *testing.T) {
			r := serverReader(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/objects") {
					kind := "FILE"
					if tt.typ == "notebook" {
						kind = "NOTEBOOK"
					}
					fmt.Fprintf(w, `{"items":[{"displayName":"日本 %%#","path":"/Workspace/日本 %%#","type":%q}]}`, kind)
					return
				}
				if !strings.Contains(r.URL.EscapedPath(), "Workspace%2F%E6%97%A5%E6%9C%AC%20%25%23") {
					t.Errorf("unescaped content path: %s", r.URL.EscapedPath())
				}
				fmt.Fprintf(w, `{"name":"日本 %%#","path":"Workspace/日本 %%#","type":%q,"format":%q,"content":%s}`, tt.typ, tt.format, tt.content)
			})
			got, e := r.ReadFile(context.Background(), "日本 %#")
			if e != nil || string(got) != tt.want {
				t.Fatalf("read mismatch: %v", e)
			}
			st, e := r.Stat(context.Background(), "日本 %#")
			if e != nil || st.Kind != tt.kind || st.Size != int64(len(got)) {
				t.Fatalf("stat=%v error=%v", st, e)
			}
		})
	}
}
func TestErrorsAndTruncation(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   error
	}{{401, syscall.EACCES}, {403, syscall.EACCES}, {404, syscall.ENOENT}, {409, syscall.ESTALE}, {412, syscall.ESTALE}, {429, syscall.EAGAIN}, {500, syscall.EIO}} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			r := serverReader(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, `{"code":"secret"}`)
			})
			_, e := r.ReadDir(context.Background(), "")
			if !errors.Is(e, tt.want) || strings.Contains(e.Error(), "secret") {
				t.Fatalf("error %v", e)
			}
		})
	}
	r := serverReader(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"items":`) })
	if _, e := r.ReadDir(context.Background(), ""); !errors.Is(e, syscall.EIO) {
		t.Fatal(e)
	}
}
func TestDirectoryStatDoesNotRequestContent(t *testing.T) {
	r := serverReader(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/objects") {
			fmt.Fprint(w, `{"items":[]}`)
			return
		}
		if r.URL.Query().Get("content") != "0" {
			t.Error("directory content requested")
		}
		fmt.Fprint(w, `{"name":"Workspace","path":"Workspace","type":"directory"}`)
	})
	st, e := r.Stat(context.Background(), "")
	if e != nil || st.Kind != backend.Directory {
		t.Fatalf("%v %v", st, e)
	}
}

func TestRepeatedPageAndInvalidEntries(t *testing.T) {
	for _, body := range []string{`{"items":[]}`, `{"items":[{"displayName":"escape","path":"/elsewhere/escape","type":"FILE"}]}`} {
		r := serverReader(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("opc-next-page", "same")
			fmt.Fprint(w, body)
		})
		if _, e := r.ReadDir(context.Background(), ""); !errors.Is(e, syscall.EIO) {
			t.Fatalf("unexpected error: %v", e)
		}
	}
}
func TestCancellationAndDirectoryRead(t *testing.T) {
	r := serverReader(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/objects") {
			fmt.Fprint(w, `{"items":[]}`)
			return
		}
		fmt.Fprint(w, `{"name":"Workspace","path":"Workspace","type":"directory"}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := r.ReadDir(ctx, ""); !errors.Is(e, context.Canceled) {
		t.Fatalf("unexpected error: %v", e)
	}
	if _, e := r.ReadFile(context.Background(), ""); !errors.Is(e, syscall.EISDIR) {
		t.Fatalf("unexpected error: %v", e)
	}
}

func TestSyntheticDirectoryIsNotFound(t *testing.T) {
	r := serverReader(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/objects") {
			fmt.Fprint(w, `{"items":[]}`)
			return
		}
		t.Error("must not trust synthetic contents directory")
		fmt.Fprint(w, `{"name":"missing","path":"Workspace/missing","type":"directory"}`)
	})
	if _, err := r.Stat(context.Background(), "missing"); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("stat: %v", err)
	}
	if _, err := r.ReadFile(context.Background(), "missing"); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("read: %v", err)
	}
}

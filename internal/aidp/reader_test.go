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
	"time"

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
		switch r.URL.Query().Get("path") {
		case "/Workspace":
			fmt.Fprint(w, `{"items":[{"displayName":"日本 語%#","path":"/Workspace/日本 語%#","type":"FOLDER"}]}`)
		case "/Workspace/日本 語%#":
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("opc-next-page", "next%token")
				fmt.Fprint(w, `{"items":[{"displayName":"one","path":"/Workspace/日本 語%#/one","type":"FILE"}]}`)
				return
			}
			if r.URL.Query().Get("page") != "next%token" {
				t.Error("incorrect token")
			}
			fmt.Fprint(w, `{"items":[]}`)
		default:
			t.Errorf("unexpected query path %q", r.URL.Query().Get("path"))
			fmt.Fprint(w, `{"items":[]}`)
		}
	})
	entries, e := r.ReadDir(context.Background(), "日本 語%#")
	if e != nil || len(entries) != 1 || entries[0].Name != "one" {
		t.Fatalf("entries=%v error=%v", entries, e)
	}
	if _, e = r.ReadDir(context.Background(), "../escape"); !errors.Is(e, syscall.EINVAL) {
		t.Fatalf("unexpected error %v", e)
	}
}

func TestReadDirRejectsMissingSyntheticDirectoryAndFiles(t *testing.T) {
	t.Run("missing synthetic directory", func(t *testing.T) {
		r := serverReader(t, func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Query().Get("path") != "/Workspace" {
				t.Errorf("unexpected child listing for %q", req.URL.Query().Get("path"))
			}
			fmt.Fprint(w, `{"items":[]}`)
		})
		if _, err := r.ReadDir(context.Background(), "missing"); !errors.Is(err, syscall.ENOENT) {
			t.Fatalf("ReadDir missing error = %v, want ENOENT", err)
		}
	})
	t.Run("file path", func(t *testing.T) {
		r := serverReader(t, func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Query().Get("path") != "/Workspace" {
				t.Errorf("unexpected child listing for %q", req.URL.Query().Get("path"))
			}
			fmt.Fprint(w, `{"items":[{"displayName":"file","path":"/Workspace/file","type":"FILE"}]}`)
		})
		if _, err := r.ReadDir(context.Background(), "file"); !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("ReadDir file error = %v, want ENOTDIR", err)
		}
	})
}

func TestConfiguredSubtreeRootValidation(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprintf("exists=%t", exists), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/objects") {
					switch req.URL.Query().Get("path") {
					case "/Workspace":
						if exists {
							fmt.Fprint(w, `{"items":[{"displayName":"selected","path":"/Workspace/selected","type":"FOLDER"}]}`)
						} else {
							fmt.Fprint(w, `{"items":[]}`)
						}
					case "/Workspace/selected":
						if !exists {
							t.Error("listed nonexistent configured root")
						}
						fmt.Fprint(w, `{"items":[]}`)
					default:
						t.Errorf("unexpected objects path %q", req.URL.Query().Get("path"))
					}
					return
				}
				if !exists || req.URL.Query().Get("content") != "0" {
					t.Error("unexpected contents request")
					return
				}
				fmt.Fprint(w, `{"name":"selected","path":"Workspace/selected","type":"directory"}`)
			}))
			t.Cleanup(server.Close)
			r, err := NewWithClient(server.URL, "platform", "workspace", "/Workspace/selected", noSigner{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.Stat(context.Background(), ""); exists {
				if err != nil {
					t.Fatalf("stat valid configured root: %v", err)
				}
				entries, err := r.ReadDir(context.Background(), "")
				if err != nil || len(entries) != 0 {
					t.Fatalf("read valid empty configured root: %v %v", entries, err)
				}
			} else if !errors.Is(err, syscall.ENOENT) {
				t.Fatalf("stat missing configured root = %v, want ENOENT", err)
			}
		})
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
		code   string
		want   error
	}{{401, "NotAuthenticated", syscall.EACCES}, {403, "NotAuthorized", syscall.EACCES}, {404, "NotAuthorizedOrNotFound", syscall.ENOENT}, {404, "InvalidParameter", syscall.EINVAL}, {409, "ResourceLocked", syscall.EBUSY}, {409, "NotAuthorizedOrResourceAlreadyExists", syscall.ESTALE}, {412, "NoEtagMatch", syscall.ESTALE}, {413, "PayloadTooLarge", syscall.EFBIG}, {429, "TooManyRequests", syscall.EAGAIN}, {500, "InternalError", syscall.EIO}} {
		t.Run(fmt.Sprintf("%d_%s", tt.status, tt.code), func(t *testing.T) {
			r := serverReader(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprintf(w, `{"code":%q}`, tt.code)
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

func TestDuplicateDisplayNameRejected(t *testing.T) {
	r := serverReader(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"items":[{"displayName":"same","path":"/Workspace/same","type":"FILE"},{"displayName":"same","path":"/Workspace/same","type":"NOTEBOOK"}]}`)
	})
	if _, err := r.ReadDir(context.Background(), ""); !errors.Is(err, syscall.EIO) {
		t.Fatalf("duplicate display name error = %v, want EIO", err)
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

func TestRetryOnlyTransientStatuses(t *testing.T) {
	for _, tt := range []struct {
		status, attempts int
		success          bool
	}{{429, 3, true}, {500, 3, true}, {503, 3, true}, {403, 1, false}, {409, 1, false}} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			attempts := 0
			r := serverReader(t, func(w http.ResponseWriter, req *http.Request) {
				attempts++
				if req.Method != http.MethodGet {
					t.Errorf("unexpected method %s", req.Method)
				}
				if attempts < 3 || !tt.success {
					w.WriteHeader(tt.status)
					return
				}
				fmt.Fprint(w, `{"items":[]}`)
			})
			_, err := r.listRemote(context.Background(), "/Workspace")
			if (err == nil) != tt.success || attempts != tt.attempts {
				t.Fatalf("attempts=%d err=%v", attempts, err)
			}
		})
	}
}
func TestRetryCancellationAndTimeout(t *testing.T) {
	r := serverReader(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := r.ReadDir(ctx, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v", err)
	}
	r = serverReader(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(25 * time.Millisecond)
		fmt.Fprint(w, `{"items":[]}`)
	})
	r.client.Timeout = 5 * time.Millisecond
	if _, err := r.ReadDir(context.Background(), ""); !errors.Is(err, syscall.ETIMEDOUT) {
		t.Fatalf("error %v", err)
	}
}
func TestRedirectDoesNotForwardSignedHeaders(t *testing.T) {
	visited := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { visited = true }))
	defer other.Close()
	r := serverReader(t, func(w http.ResponseWriter, req *http.Request) { http.Redirect(w, req, other.URL, http.StatusFound) })
	_, err := r.ReadDir(context.Background(), "")
	if err == nil || visited {
		t.Fatalf("redirect followed or not rejected: %v", err)
	}
}

func TestRetryAfterLongDelayDoesNotRetryEarly(t *testing.T) {
	calls := 0
	r := serverReader(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err := r.ReadDir(context.Background(), ""); !errors.Is(err, syscall.EAGAIN) || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
func TestTruncatedResponseIsRetried(t *testing.T) {
	calls := 0
	r := serverReader(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Length", "100")
			fmt.Fprint(w, `{"items":`)
			return
		}
		fmt.Fprint(w, `{"items":[]}`)
	})
	if _, err := r.listRemote(context.Background(), "/Workspace"); err != nil || calls != 2 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
func TestSignerErrorIsRedacted(t *testing.T) {
	r := serverReader(t, func(http.ResponseWriter, *http.Request) { t.Error("must not send") })
	r.signer = failingSigner{}
	_, err := r.ReadDir(context.Background(), "")
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("error %v", err)
	}
}

type failingSigner struct{}

func (failingSigner) Sign(*http.Request) error { return errors.New("sensitive signing material") }

// The tested tenant reports an extension-less FILE as a directory for
// content=0 and returns HTTP 400 for content=1 unless type=file is requested;
// notebooks must not be sent that type.
func TestListedFileContentRequestsFileType(t *testing.T) {
	r := serverReader(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/objects") {
			fmt.Fprint(w, `{"items":[{"displayName":"Makefile","path":"/Workspace/Makefile","type":"FILE"},{"displayName":"n.ipynb","path":"/Workspace/n.ipynb","type":"NOTEBOOK"}]}`)
			return
		}
		q := req.URL.Query()
		switch {
		case strings.HasSuffix(req.URL.EscapedPath(), "Makefile"):
			switch {
			case q.Get("type") == "file" && q.Get("content") == "1":
				fmt.Fprint(w, `{"name":"Makefile","path":"Workspace/Makefile","type":"file","format":"text","content":"all:\n"}`)
			case q.Get("type") == "file":
				fmt.Fprint(w, `{"name":"Makefile","path":"Workspace/Makefile","type":"file"}`)
			case q.Get("content") == "0":
				// Observed: the type is inferred from the missing extension.
				fmt.Fprint(w, `{"name":"Makefile","path":"Workspace/Makefile","type":"directory"}`)
			default:
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"code":"InvalidParameter","message":"Bad Request"}`)
			}
		default:
			if q.Has("type") {
				t.Errorf("notebook request has type %q", q.Get("type"))
			}
			fmt.Fprint(w, `{"name":"n.ipynb","path":"Workspace/n.ipynb","type":"notebook","format":"json","content":{"cells":[]}}`)
		}
	})
	data, err := r.ReadFile(context.Background(), "Makefile")
	if err != nil || string(data) != "all:\n" {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}
	st, err := r.Stat(context.Background(), "Makefile")
	if err != nil || st.Kind != backend.File || st.Size != 5 {
		t.Fatalf("Stat = %+v, %v", st, err)
	}
	if _, err = r.ReadFile(context.Background(), "n.ipynb"); err != nil {
		t.Fatalf("notebook ReadFile: %v", err)
	}
}

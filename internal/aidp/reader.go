// Package aidp implements the read-only Workbench backend.
package aidp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aidpfs/internal/backend"
	"aidpfs/internal/config"
)

// Signer signs a request immediately before it is sent.
type Signer interface{ Sign(*http.Request) error }

type Reader struct {
	base, root string
	client     *http.Client
	signer     Signer
}

// New requires an HTTPS origin; tests can use NewWithClient with an injected server.
func New(endpoint, platform, workspace, root string, signer Signer) (*Reader, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("endpoint must use HTTPS")
	}
	return NewWithClient(endpoint, platform, workspace, root, signer, &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
}

func NewWithClient(endpoint, platform, workspace, root string, signer Signer, client *http.Client) (*Reader, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || platform == "" || workspace == "" || client == nil || signer == nil {
		return nil, errors.New("invalid backend configuration")
	}
	if _, err := config.Resolve(root, ""); err != nil {
		return nil, err
	}
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Reader{base: strings.TrimRight(endpoint, "/") + "/20260430/aiDataPlatforms/" + url.PathEscape(platform) + "/workspaces/" + url.PathEscape(workspace), root: root, signer: signer, client: &safeClient}, nil
}

type APIError struct {
	Status    int
	Code      string
	RequestID string
}

func (e *APIError) Error() string { return fmt.Sprintf("AIDP request failed (HTTP %d)", e.Status) }
func (e *APIError) Unwrap() error {
	switch e.Status {
	case 400:
		return syscall.EINVAL
	case 401, 403:
		return syscall.EACCES
	case 404:
		return syscall.ENOENT
	case 409, 412:
		return syscall.ESTALE
	case 429:
		return syscall.EAGAIN
	default:
		return syscall.EIO
	}
}

// A response bound limits resource use until a streaming transfer is implemented.
const maxResponse = 64 << 20

// Only idempotent GET requests use this retry loop. Each attempt is signed
// separately and redirects are refused to avoid forwarding credentials.
func (r *Reader) get(ctx context.Context, endpoint string, query url.Values, out any) (http.Header, error) {
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		header, retry, errResult := r.getOnce(ctx, endpoint, query, out)
		if !retry || attempt == maxAttempts-1 {
			return header, errResult
		}
		delay := time.Duration(100<<attempt) * time.Millisecond
		if h := header.Get("Retry-After"); h != "" {
			if seconds, err := strconv.Atoi(h); err == nil && seconds >= 0 {
				if seconds > 2 {
					return nil, errResult
				}
				requested := time.Duration(seconds) * time.Second
				if requested > delay {
					delay = requested
				}
			} else if until, err := http.ParseTime(h); err == nil {
				requested := time.Until(until)
				if requested > 2*time.Second {
					return nil, errResult
				}
				if requested > delay {
					delay = requested
				}
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	panic("unreachable")
}

func (r *Reader) getOnce(ctx context.Context, endpoint string, query url.Values, out any) (http.Header, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, false, syscall.EINVAL
	}
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("should-update-recent", "false")
	if err = r.signer.Sign(req); err != nil {
		return nil, false, errors.New("unable to sign AIDP request")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, true, syscall.ETIMEDOUT
		}
		return nil, true, syscall.EIO
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
		retry := resp.StatusCode == 429 || resp.StatusCode == 500 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504
		return resp.Header, retry, &APIError{Status: resp.StatusCode, Code: body.Code, RequestID: resp.Header.Get("opc-request-id")}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, true, syscall.ETIMEDOUT
		}
		return nil, true, syscall.EIO
	}
	if len(data) > maxResponse {
		return nil, false, syscall.EFBIG
	}
	if err = json.Unmarshal(data, out); err != nil {
		return nil, false, syscall.EIO
	}
	return resp.Header, false, nil
}

type object struct {
	Name    string    `json:"displayName"`
	Path    string    `json:"path"`
	Type    string    `json:"type"`
	Updated time.Time `json:"timeUpdated"`
	ETag    string    `json:"compositeEtag"`
}
type objectList struct {
	Items []object `json:"items"`
}

func (r *Reader) ReadDir(ctx context.Context, rel string) ([]backend.Metadata, error) {
	remote, err := config.Resolve(r.root, rel)
	if err != nil {
		return nil, syscall.EINVAL
	}
	return r.listRemote(ctx, remote)
}

func (r *Reader) listRemote(ctx context.Context, remote string) ([]backend.Metadata, error) {
	var result []backend.Metadata
	page := ""
	seen := map[string]bool{}
	names := map[string]bool{}
	for {
		q := url.Values{"path": {remote}, "limit": {"1000"}}
		if page != "" {
			q.Set("page", page)
		}
		var batch objectList
		h, e := r.get(ctx, r.base+"/objects", q, &batch)
		if e != nil {
			return nil, e
		}
		if batch.Items == nil {
			return nil, syscall.EIO
		}
		for _, item := range batch.Items {
			if item.Name == "" || item.Name == "." || item.Name == ".." || strings.ContainsAny(item.Name, "/\x00") || path.Join(remote, item.Name) != item.Path || names[item.Name] {
				return nil, syscall.EIO
			}
			names[item.Name] = true
			var kind backend.Kind
			switch item.Type {
			case "FOLDER":
				kind = backend.Directory
			case "FILE":
				kind = backend.File
			case "NOTEBOOK":
				kind = backend.Notebook
			default:
				return nil, syscall.ENOTSUP
			}
			result = append(result, backend.Metadata{Name: item.Name, Kind: kind, Modified: item.Updated, Version: item.ETag, Size: -1})
		}
		page = h.Get("opc-next-page")
		if page == "" {
			return result, nil
		}
		if seen[page] {
			return nil, syscall.EIO
		}
		seen[page] = true
	}
}

type content struct {
	Name     string          `json:"name"`
	Path     string          `json:"path"`
	Type     string          `json:"type"`
	Format   string          `json:"format"`
	Modified time.Time       `json:"last_modified"`
	Content  json.RawMessage `json:"content"`
}

func (r *Reader) fetch(ctx context.Context, rel string, include bool) (content, []byte, error) {
	remote, err := config.Resolve(r.root, rel)
	if err != nil {
		return content{}, nil, syscall.EINVAL
	}
	// contentPath is one path parameter, including encoded slash separators.
	endpoint := r.base + "/notebook/api/contents/" + url.PathEscape(strings.TrimPrefix(remote, "/"))
	q := url.Values{"content": {"1"}}
	if !include {
		q.Set("content", "0")
	}
	var c content
	_, err = r.get(ctx, endpoint, q, &c)
	if err != nil {
		return c, nil, err
	}
	if strings.TrimPrefix(c.Path, "/") != strings.TrimPrefix(remote, "/") || (remote != "/" && c.Name != path.Base(remote)) {
		return c, nil, syscall.EIO
	}
	if !include || c.Type == "directory" {
		return c, nil, nil
	}
	var data []byte
	switch c.Format {
	case "text":
		if c.Type != "file" || len(c.Content) == 0 || string(c.Content) == "null" {
			return c, nil, syscall.EIO
		}
		var s string
		if json.Unmarshal(c.Content, &s) != nil {
			return c, nil, syscall.EIO
		}
		data = []byte(s)
	case "base64":
		if c.Type != "file" || len(c.Content) == 0 || string(c.Content) == "null" {
			return c, nil, syscall.EIO
		}
		var s string
		if json.Unmarshal(c.Content, &s) != nil {
			return c, nil, syscall.EIO
		}
		data, err = base64.StdEncoding.DecodeString(s)
		if err != nil {
			return c, nil, syscall.EIO
		}
	case "json":
		if c.Type != "notebook" || len(c.Content) == 0 || string(c.Content) == "null" || !json.Valid(c.Content) || c.Content[0] != '{' {
			return c, nil, syscall.EIO
		}
		data = append([]byte(nil), c.Content...)
	default:
		return c, nil, syscall.ENOTSUP
	}
	return c, data, nil
}

// Contents may report a synthetic directory for a nonexistent path. Confirm
// membership in the parent's complete object listing before trusting it.
func (r *Reader) listed(ctx context.Context, rel string) (backend.Kind, error) {
	remote, err := config.Resolve(r.root, rel)
	if err != nil {
		return 0, syscall.EINVAL
	}
	if remote == "/" {
		return backend.Directory, nil
	}
	// Workspace's virtual root is not necessarily listed by its parent. Verify
	// that objects accepts the explicitly configured root instead.
	if rel == "" {
		_, err := r.listRemote(ctx, remote)
		if err != nil {
			return 0, err
		}
		return backend.Directory, nil
	}
	entries, err := r.listRemote(ctx, path.Dir(remote))
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		if entry.Name == path.Base(remote) {
			return entry.Kind, nil
		}
	}
	return 0, syscall.ENOENT
}

func (r *Reader) Stat(ctx context.Context, rel string) (backend.Metadata, error) {
	listedKind, err := r.listed(ctx, rel)
	if err != nil {
		return backend.Metadata{}, err
	}
	c, _, err := r.fetch(ctx, rel, false)
	if err != nil {
		return backend.Metadata{}, err
	}
	var data []byte
	if c.Type != "directory" {
		c, data, err = r.fetch(ctx, rel, true)
		if err != nil {
			return backend.Metadata{}, err
		}
	}
	var kind backend.Kind
	switch c.Type {
	case "directory":
		kind = backend.Directory
	case "file":
		kind = backend.File
	case "notebook":
		kind = backend.Notebook
	default:
		return backend.Metadata{}, syscall.ENOTSUP
	}
	if kind != listedKind {
		return backend.Metadata{}, syscall.ESTALE
	}
	return backend.Metadata{Name: c.Name, Kind: kind, Size: int64(len(data)), Modified: c.Modified}, nil
}
func (r *Reader) ReadFile(ctx context.Context, rel string) ([]byte, error) {
	listedKind, err := r.listed(ctx, rel)
	if err != nil {
		return nil, err
	}
	if listedKind == backend.Directory {
		return nil, syscall.EISDIR
	}
	c, _, err := r.fetch(ctx, rel, false)
	if err != nil {
		return nil, err
	}
	if c.Type == "directory" {
		return nil, syscall.EISDIR
	}
	c, data, err := r.fetch(ctx, rel, true)
	if err != nil {
		return nil, err
	}
	if c.Type == "directory" {
		return nil, syscall.EISDIR
	}
	if (c.Type == "file" && listedKind != backend.File) || (c.Type == "notebook" && listedKind != backend.Notebook) {
		return nil, syscall.ESTALE
	}
	if c.Type != "file" && c.Type != "notebook" {
		return nil, syscall.ENOTSUP
	}
	return data, nil
}

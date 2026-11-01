package aidp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrOutcomeUnknown means a PUT may have reached storage. The caller must
// reconcile the remote state before any retry; blindly repeating it is unsafe.
var ErrOutcomeUnknown = errors.New("PAR transfer outcome unknown")

// parURL deliberately does not expose its bearer secret via String or GoString.
type parURL struct{ raw string }

func (parURL) String() string   { return "[redacted PAR]" }
func (parURL) GoString() string { return "[redacted PAR]" }

// parTransport is separate from the signed AIDP client. It never follows
// redirects or receives a signer. Origin is an exact HTTPS authority.
type parTransport struct {
	origin *url.URL
	client *http.Client
}

func newPARTransport(origin string, client *http.Client) (*parTransport, error) {
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || client == nil {
		return nil, errors.New("invalid PAR origin")
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// Cookies can carry bearer credentials; PAR requests must not use a cookie jar.
	c.Jar = nil
	return &parTransport{origin: u, client: &c}, nil
}
func newOCIParTransport(region string) (*parTransport, error) {
	// OCI region identifiers are DNS labels, not arbitrary hostnames or URLs.
	if region == "" || strings.Trim(region, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return nil, errors.New("invalid OCI region")
	}
	return newPARTransport("https://objectstorage."+region+".oraclecloud.com", &http.Client{Timeout: 30 * time.Second})
}
func (p *parTransport) validate(raw string) (parURL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host != p.origin.Host || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/p/") {
		return parURL{}, errors.New("invalid PAR URL")
	}
	return parURL{raw: raw}, nil
}

type parHTTPError struct{ status int }

func (e *parHTTPError) Error() string { return fmt.Sprintf("PAR request failed (HTTP %d)", e.status) }
func (e *parHTTPError) Unwrap() error {
	switch e.status {
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

// downloadVersioned requires the object response ETag to match the metadata
// version, avoiding pairing bytes from a newer object with an older version.
func (p *parTransport) downloadVersioned(ctx context.Context, u parURL, expectedETag string) ([]byte, error) {
	if expectedETag == "" {
		return nil, syscall.ESTALE
	}
	data, actual, err := p.downloadWithETag(ctx, u)
	if err != nil {
		return nil, err
	}
	// OCI may quote the HTTP ETag while returning its metadata value unquoted.
	if actual == "" || normalizeETag(actual) != normalizeETag(expectedETag) {
		return nil, syscall.ESTALE
	}
	return data, nil
}

func normalizeETag(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
func (p *parTransport) download(ctx context.Context, u parURL) ([]byte, error) {
	data, _, err := p.downloadWithETag(ctx, u)
	return data, err
}
func (p *parTransport) downloadWithETag(ctx context.Context, u parURL) ([]byte, string, error) {
	if _, e := p.validate(u.raw); e != nil {
		return nil, "", e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.raw, nil)
	if e != nil {
		return nil, "", errors.New("invalid PAR request")
	}
	resp, e := p.client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", syscall.EIO
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", &parHTTPError{status: resp.StatusCode}
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if e != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", syscall.EIO
	}
	if len(data) > maxResponse {
		return nil, "", syscall.EFBIG
	}
	return data, resp.Header.Get("ETag"), nil
}

// putExisting uses a nonempty ETag and never retries. A 2xx without a new ETag
// is ambiguous because the transfer could have changed the object.
func (p *parTransport) putExisting(ctx context.Context, u parURL, oldETag string, data []byte) (string, error) {
	if _, e := p.validate(u.raw); e != nil {
		return "", e
	}
	if strings.TrimSpace(oldETag) == "" || strings.TrimSpace(oldETag) == "*" || strings.ContainsAny(oldETag, "\r\n") {
		return "", errors.New("a valid ETag is required")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPut, u.raw, bytes.NewReader(data))
	if e != nil {
		return "", errors.New("invalid PAR request")
	}
	req.Header.Set("If-Match", oldETag)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, e := p.client.Do(req)
	if e != nil {
		return "", ErrOutcomeUnknown
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
			return "", ErrOutcomeUnknown
		}
		return "", &parHTTPError{status: resp.StatusCode}
	}
	etag := resp.Header.Get("ETag")
	if etag == "" || strings.ContainsAny(etag, "\r\n") {
		return "", ErrOutcomeUnknown
	}
	return etag, nil
}

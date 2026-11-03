package aidp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"aidpfs/internal/backend"
	"aidpfs/internal/config"
)

// MutateFile makes exactly one request. Caller must serialize operations and
// maintain a durable intent. The API does not provide verified CAS here.
func (r *Reader) MutateFile(ctx context.Context, operation, source, destination string) error {
	remote, err := config.Resolve(r.root, source)
	if err != nil || source == "" {
		return syscall.EINVAL
	}
	kind, err := r.ListedKind(ctx, source)
	if err != nil {
		return err
	}
	if kind == backend.Directory {
		return syscall.EISDIR
	}
	if kind != backend.File {
		return syscall.EOPNOTSUPP
	}
	var method string
	var payload []byte
	switch operation {
	case "unlink":
		if destination != "" {
			return syscall.EINVAL
		}
		method = http.MethodDelete
	case "rename":
		target, e := config.Resolve(r.root, destination)
		if e != nil || destination == "" || target == remote {
			return syscall.EINVAL
		}
		method = http.MethodPatch
		payload, err = json.Marshal(map[string]string{"path": strings.TrimPrefix(target, "/")})
		if err != nil {
			return syscall.EINVAL
		}
	default:
		return syscall.EINVAL
	}
	endpoint := r.base + "/notebook/api/contents/" + url.PathEscape(strings.TrimPrefix(remote, "/"))
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return syscall.EINVAL
	}
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/json")
	}
	if err = r.signer.Sign(req); err != nil {
		return syscall.EIO
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return ErrOutcomeUnknown
	}
	defer resp.Body.Close()
	if (method == http.MethodPatch && resp.StatusCode == http.StatusOK) || (method == http.MethodDelete && resp.StatusCode == http.StatusNoContent) {
		return nil
	}
	if resp.StatusCode == http.StatusConflict {
		return syscall.EEXIST
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500 || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return ErrOutcomeUnknown
	}
	return &APIError{Status: resp.StatusCode}
}

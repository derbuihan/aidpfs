package aidp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"syscall"
	"time"

	"aidpfs/internal/backend"
	"aidpfs/internal/config"
)

// MutateFile makes exactly one request. Caller must serialize operations and
// maintain a durable intent. The API does not provide verified CAS here.
// Errors returned before the request is sent, and HTTP 400/401/403/404/409
// rejections, are wrapped as backend.NotAppliedError: the tested tenant left
// source and destination unchanged in those cases. Transport errors, 2xx
// other than the expected status, 408 and 5xx (including "Lock already held"
// during concurrent renames) are ErrOutcomeUnknown and must not be retried.
func (r *Reader) MutateFile(ctx context.Context, operation, source, destination string) error {
	err := r.mutateFile(ctx, operation, source, destination)
	var api *APIError
	if errors.As(err, &api) {
		switch api.Status {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict:
			return backend.NotApplied(err)
		}
	}
	return err
}

func (r *Reader) mutateFile(ctx context.Context, operation, source, destination string) error {
	remote, err := config.Resolve(r.root, source)
	if err != nil || source == "" {
		return backend.NotApplied(syscall.EINVAL)
	}
	kind, err := r.ListedKind(ctx, source)
	if err != nil {
		return backend.NotApplied(err)
	}
	if kind == backend.Directory {
		return backend.NotApplied(syscall.EISDIR)
	}
	if kind != backend.File {
		return backend.NotApplied(syscall.EOPNOTSUPP)
	}
	var method string
	var payload []byte
	switch operation {
	case "unlink":
		if destination != "" {
			return backend.NotApplied(syscall.EINVAL)
		}
		method = http.MethodDelete
	case "rename":
		target, e := config.Resolve(r.root, destination)
		if e != nil || destination == "" || target == remote || !backend.ValidName(path.Base(target)) {
			return backend.NotApplied(syscall.EINVAL)
		}
		method = http.MethodPatch
		payload, err = json.Marshal(map[string]string{"path": strings.TrimPrefix(target, "/")})
		if err != nil {
			return backend.NotApplied(syscall.EINVAL)
		}
	default:
		return backend.NotApplied(syscall.EINVAL)
	}
	endpoint := r.base + "/notebook/api/contents/" + url.PathEscape(strings.TrimPrefix(remote, "/"))
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return backend.NotApplied(syscall.EINVAL)
	}
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/json")
	}
	if err = r.signer.Sign(req); err != nil {
		return backend.NotApplied(syscall.EIO)
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
		// An existing FILE or NOTEBOOK destination; source is left unchanged.
		return backend.NotApplied(syscall.EEXIST)
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500 || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return ErrOutcomeUnknown
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
	return &APIError{Status: resp.StatusCode, Code: body.Code, RequestID: resp.Header.Get("opc-request-id")}
}

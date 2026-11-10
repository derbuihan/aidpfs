package aidp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"syscall"
	"time"

	"aidpfs/internal/backend"
	"aidpfs/internal/config"
)

// CreateEmptyExperimental makes one non-overwriting attempt to create a regular
// empty FILE. A transport error or server failure is ambiguous: the file may
// already exist, so callers must not retry automatically. Failures before the
// request is sent and HTTP 400/401/403/404/409 are backend.NotAppliedError:
// is-overwrite=false left an existing object unchanged in the tested tenant.
func (r *Reader) CreateEmptyExperimental(ctx context.Context, rel string) error {
	return r.createObjectExperimental(ctx, rel, "FILE")
}

// CreateDirectoryExperimental creates an empty FOLDER without overwrite.
func (r *Reader) CreateDirectoryExperimental(ctx context.Context, rel string) error {
	return r.createObjectExperimental(ctx, rel, "FOLDER")
}

func (r *Reader) createObjectExperimental(ctx context.Context, rel, kind string) error {
	remote, err := config.Resolve(r.root, rel)
	if err != nil || rel == "" || !backend.ValidName(path.Base(remote)) {
		return backend.NotApplied(syscall.EINVAL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.base+"/objects", bytes.NewReader(nil))
	if err != nil {
		return backend.NotApplied(syscall.EINVAL)
	}
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("path", remote)
	req.Header.Set("type", kind)
	req.Header.Set("is-overwrite", "false")
	req.Header.Set("Content-Type", "application/octet-stream")
	if err = r.signer.Sign(req); err != nil {
		return backend.NotApplied(errors.New("unable to sign AIDP request"))
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return ErrOutcomeUnknown
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusConflict:
		return backend.NotApplied(syscall.EEXIST)
	case http.StatusRequestTimeout:
		return ErrOutcomeUnknown
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
		return backend.NotApplied(&APIError{Status: resp.StatusCode, Code: body.Code, RequestID: resp.Header.Get("opc-request-id")})
	default:
		if resp.StatusCode >= 500 {
			return ErrOutcomeUnknown
		}
		return &APIError{Status: resp.StatusCode}
	}
}

package aidp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"syscall"
	"time"

	"aidpfs/internal/config"
)

// CreateEmptyExperimental makes one non-overwriting attempt to create a normal
// empty file. A transport error or server failure is ambiguous: the file may
// already exist, so callers must not retry automatically.
func (r *Reader) CreateEmptyExperimental(ctx context.Context, rel string) error {
	remote, err := config.Resolve(r.root, rel)
	if err != nil || rel == "" {
		return syscall.EINVAL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.base+"/objects", bytes.NewReader(nil))
	if err != nil {
		return syscall.EINVAL
	}
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("path", remote)
	req.Header.Set("type", "FILE")
	req.Header.Set("is-overwrite", "false")
	req.Header.Set("Content-Type", "application/octet-stream")
	if err = r.signer.Sign(req); err != nil {
		return errors.New("unable to sign AIDP request")
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
		return syscall.EEXIST
	case http.StatusRequestTimeout:
		return ErrOutcomeUnknown
	default:
		if resp.StatusCode >= 500 {
			return ErrOutcomeUnknown
		}
		return &APIError{Status: resp.StatusCode}
	}
}

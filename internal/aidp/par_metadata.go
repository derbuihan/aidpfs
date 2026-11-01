package aidp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"syscall"
	"time"

	"aidpfs/internal/config"
)

type parMetadata struct {
	URL  parURL
	ETag string
	Size *int64
}

// parMetadataRequest makes one signed POST only. REFRESH can issue a bearer
// capability; UPDATE changes metadata. Neither operation is automatically retried.
// The caller must reconcile unknown outcomes and protect its recovery data.
func (r *Reader) parMetadataRequest(ctx context.Context, transport *parTransport, rel, action, etag string, size int64) (parMetadata, error) {
	if transport == nil || (action != "DOWNLOAD" && action != "REFRESH" && action != "UPDATE") {
		return parMetadata{}, syscall.EINVAL
	}
	remote, err := config.Resolve(r.root, rel)
	if err != nil || rel == "" {
		return parMetadata{}, syscall.EINVAL
	}
	var body []byte
	var endpoint string
	q := url.Values{"shouldGenerateNewPar": {"true"}}
	if action == "DOWNLOAD" {
		endpoint = r.base + "/actions/downloadFileMeta"
	} else {
		endpoint = r.base + "/actions/uploadFileMeta"
		q.Set("shouldCreateRecursively", "false")
		payload := map[string]any{"action": action}
		if action == "UPDATE" {
			if etag == "" || size < 0 {
				return parMetadata{}, syscall.EINVAL
			}
			payload["eTag"] = etag
			payload["size"] = size
			q.Del("shouldGenerateNewPar")
		}
		body, err = json.Marshal(payload)
		if err != nil {
			return parMetadata{}, syscall.EINVAL
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"?"+q.Encode(), bytes.NewReader(body))
	if err != nil {
		return parMetadata{}, syscall.EINVAL
	}
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("path", remote)
	req.Header.Set("type", "FILE")
	req.Header.Set("should-update-recent", "false")
	// OCI signs content-type even for a bodyless POST. It must be present
	// on the wire with the same value that the signer included.
	req.Header.Set("Content-Type", "application/json")
	if err = r.signer.Sign(req); err != nil {
		return parMetadata{}, errors.New("unable to sign AIDP request")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return parMetadata{}, ErrOutcomeUnknown
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
			return parMetadata{}, ErrOutcomeUnknown
		}
		return parMetadata{}, &APIError{Status: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return parMetadata{}, ErrOutcomeUnknown
	}
	var raw struct {
		URL  string `json:"parUrl"`
		ETag string `json:"eTag"`
		Size *int64 `json:"size"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return parMetadata{}, ErrOutcomeUnknown
	}
	result := parMetadata{ETag: raw.ETag, Size: raw.Size}
	if action != "UPDATE" {
		result.URL, err = transport.validate(raw.URL)
		if err != nil {
			return parMetadata{}, err
		}
	}
	return result, nil
}

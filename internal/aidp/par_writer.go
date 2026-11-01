package aidp

import (
	"bytes"
	"context"
	"path"
	"strings"
	"syscall"

	"aidpfs/internal/backend"
	"aidpfs/internal/config"
	"aidpfs/internal/recovery"
	"aidpfs/internal/writeback"
)

// parWriter connects the experimental writeback coordinator to OCI. It is
// intentionally not exposed through FUSE until recovery and rename semantics
// have been verified end-to-end.
type parWriter struct {
	reader    *Reader
	transport *parTransport
}
type parUpload struct {
	transport *parTransport
	url       parURL
}

func (u *parUpload) Put(ctx context.Context, etag string, data []byte) (string, error) {
	return u.transport.putExisting(ctx, u.url, etag, data)
}

func (w *parWriter) relative(t writeback.Target) (string, error) {
	r := w.reader
	if t.PlatformID != r.platformID || t.WorkspaceKey != r.workspaceKey || t.RemotePath == r.root {
		return "", syscall.EINVAL
	}
	if !strings.HasPrefix(t.RemotePath, strings.TrimRight(r.root, "/")+"/") {
		return "", syscall.EINVAL
	}
	rel := strings.TrimPrefix(t.RemotePath, strings.TrimRight(r.root, "/")+"/")
	resolved, err := config.Resolve(r.root, rel)
	if err != nil || resolved != t.RemotePath || path.Clean(t.RemotePath) != t.RemotePath {
		return "", syscall.EINVAL
	}
	return rel, nil
}
func (w *parWriter) Refresh(ctx context.Context, t writeback.Target) (writeback.Upload, error) {
	rel, err := w.relative(t)
	if err != nil {
		return nil, err
	}
	m, err := w.reader.parMetadataRequest(ctx, w.transport, rel, "REFRESH", "", 0)
	if err != nil {
		return nil, err
	}
	return &parUpload{w.transport, m.URL}, nil
}
func (w *parWriter) Complete(ctx context.Context, t writeback.Target, tag string, size int64) error {
	rel, err := w.relative(t)
	if err != nil {
		return err
	}
	_, err = w.reader.parMetadataRequest(ctx, w.transport, rel, "UPDATE", tag, size)
	return err
}
func (w *parWriter) Verify(ctx context.Context, t writeback.Target, tag string, expected []byte) error {
	rel, err := w.relative(t)
	if err != nil {
		return err
	}
	m, err := w.reader.parMetadataRequest(ctx, w.transport, rel, "DOWNLOAD", "", 0)
	if err != nil {
		return err
	}
	if m.ETag == "" || normalizeETag(m.ETag) != normalizeETag(tag) || m.Size == nil || *m.Size != int64(len(expected)) {
		return syscall.ESTALE
	}
	data, err := w.transport.downloadVersioned(ctx, m.URL, m.ETag)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return syscall.ESTALE
	}
	return nil
}

var _ writeback.Remote = (*parWriter)(nil)

// OpenExperimentalBuffer prepares an existing normal file for isolated FUSE
// experiments. It is not exposed by the CLI or a production write mount.
func (r *Reader) OpenExperimentalBuffer(ctx context.Context, rel, region string, journal *recovery.Store) (*writeback.Buffer, error) {
	if journal == nil || rel == "" {
		return nil, syscall.EINVAL
	}
	remote, err := config.Resolve(r.root, rel)
	if err != nil {
		return nil, syscall.EINVAL
	}
	kind, err := r.listed(ctx, rel)
	if err != nil {
		return nil, err
	}
	if kind != backend.File {
		return nil, syscall.EOPNOTSUPP
	}
	transport, err := newOCIParTransport(region)
	if err != nil {
		return nil, err
	}
	meta, err := r.parMetadataRequest(ctx, transport, rel, "DOWNLOAD", "", 0)
	if err != nil {
		return nil, err
	}
	if meta.ETag == "" || meta.Size == nil || *meta.Size < 0 {
		return nil, syscall.ESTALE
	}
	data, err := transport.downloadVersioned(ctx, meta.URL, meta.ETag)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != *meta.Size {
		return nil, syscall.ESTALE
	}
	return writeback.NewBuffer(writeback.Target{PlatformID: r.platformID, WorkspaceKey: r.workspaceKey, RemotePath: remote}, meta.ETag, data, journal, &parWriter{reader: r, transport: transport})
}

package aidp

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"strings"
	"syscall"

	"aidpfs/internal/backend"
	"aidpfs/internal/config"
	"aidpfs/internal/recovery"
	"aidpfs/internal/writeback"
)

// parWriter connects the supported single-mount FILE and Notebook write path
// to OCI. Failures retain local recovery state.
type parWriter struct {
	reader    *Reader
	transport *parTransport
	kind      backend.Kind // backend.File or backend.Notebook; zero means File
}

func (w *parWriter) objectKind() backend.Kind {
	if w.kind == 0 {
		return backend.File
	}
	return w.kind
}

// Validate rejects a Notebook save whose bytes are not an nbformat 4 document,
// so a partial or malformed write never replaces a workspace Notebook.
func (w *parWriter) Validate(data []byte) error {
	if w.objectKind() != backend.Notebook {
		return nil
	}
	return validNotebook(data)
}

func validNotebook(data []byte) error {
	var nb struct {
		Cells    []json.RawMessage `json:"cells"`
		Metadata map[string]any    `json:"metadata"`
		Major    *int              `json:"nbformat"`
		Minor    *int              `json:"nbformat_minor"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&nb); err != nil || dec.More() {
		return syscall.EINVAL
	}
	if nb.Cells == nil || nb.Metadata == nil || nb.Major == nil || *nb.Major != 4 || nb.Minor == nil || *nb.Minor < 0 {
		return syscall.EINVAL
	}
	for _, c := range nb.Cells {
		var cell struct {
			Type string `json:"cell_type"`
		}
		if json.Unmarshal(c, &cell) != nil || (cell.Type != "code" && cell.Type != "markdown" && cell.Type != "raw") {
			return syscall.EINVAL
		}
	}
	return nil
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
	m, err := w.reader.parMetadataRequestKind(ctx, w.transport, rel, "REFRESH", "", 0, w.objectKind())
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
	_, err = w.reader.parMetadataRequestKind(ctx, w.transport, rel, "UPDATE", tag, size, w.objectKind())
	return err
}
func (w *parWriter) Verify(ctx context.Context, t writeback.Target, tag string, expected []byte) error {
	rel, err := w.relative(t)
	if err != nil {
		return err
	}
	m, err := w.reader.parMetadataRequestKind(ctx, w.transport, rel, "DOWNLOAD", "", 0, w.objectKind())
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

// OpenExperimentalBuffer prepares an existing regular FILE or Notebook for the
// write-enabled mount and pins the initial storage version. A Notebook is
// exposed as its raw stored .ipynb bytes, not the contents API JSON.
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
	if kind != backend.File && kind != backend.Notebook {
		return nil, syscall.EOPNOTSUPP
	}
	transport, err := newOCIParTransport(region)
	if err != nil {
		return nil, err
	}
	meta, err := r.parMetadataRequestKind(ctx, transport, rel, "DOWNLOAD", "", 0, kind)
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
	return writeback.NewBuffer(writeback.Target{PlatformID: r.platformID, WorkspaceKey: r.workspaceKey, RemotePath: remote}, meta.ETag, data, journal, &parWriter{reader: r, transport: transport, kind: kind})
}

// Snapshot fetches a version-bound normal-file snapshot for recovery diagnosis.
// It generates a download PAR; it does not replay a write.
func (w *parWriter) Snapshot(ctx context.Context, t writeback.Target) (writeback.Snapshot, error) {
	rel, err := w.relative(t)
	if err != nil {
		return writeback.Snapshot{}, err
	}
	kind, err := w.reader.listed(ctx, rel)
	if err != nil {
		return writeback.Snapshot{}, err
	}
	if kind != backend.File && kind != backend.Notebook {
		return writeback.Snapshot{}, syscall.EOPNOTSUPP
	}
	meta, err := w.reader.parMetadataRequestKind(ctx, w.transport, rel, "DOWNLOAD", "", 0, kind)
	if err != nil {
		return writeback.Snapshot{}, err
	}
	if meta.ETag == "" || meta.Size == nil || *meta.Size < 0 {
		return writeback.Snapshot{}, syscall.ESTALE
	}
	data, err := w.transport.downloadVersioned(ctx, meta.URL, meta.ETag)
	if err != nil {
		return writeback.Snapshot{}, err
	}
	if int64(len(data)) != *meta.Size {
		return writeback.Snapshot{}, syscall.ESTALE
	}
	return writeback.Snapshot{ETag: meta.ETag, Data: data}, nil
}

// InspectRecovery obtains a fresh version-bound snapshot and never retries a
// journalled write. PAR generation is a side-effecting metadata POST.
func (r *Reader) InspectRecovery(ctx context.Context, region string, record recovery.Record, data []byte) (writeback.Observation, error) {
	transport, err := newOCIParTransport(region)
	if err != nil {
		return "", err
	}
	return writeback.Inspect(ctx, record, data, &parWriter{reader: r, transport: transport})
}

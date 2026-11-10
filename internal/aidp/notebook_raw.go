package aidp

import (
	"context"
	"syscall"

	"aidpfs/internal/backend"
)

// rawNotebookReader is the write-enabled mount's view: a Notebook's size and
// contents are its raw stored .ipynb bytes, matching what the write path
// saves through PAR. The default read-only mount keeps the contents API JSON
// and never generates PARs.
type rawNotebookReader struct {
	*Reader
	region string
}

// WithRawNotebooks returns a reader that reports Notebooks as raw .ipynb bytes.
// Each Notebook Stat or ReadFile generates a download PAR (a metadata POST).
func (r *Reader) WithRawNotebooks(region string) backend.Reader {
	return &rawNotebookReader{Reader: r, region: region}
}

func (r *rawNotebookReader) Stat(ctx context.Context, rel string) (backend.Metadata, error) {
	m, err := r.Reader.Stat(ctx, rel)
	if err != nil || m.Kind != backend.Notebook {
		return m, err
	}
	transport, err := newOCIParTransport(r.region)
	if err != nil {
		return backend.Metadata{}, err
	}
	meta, err := r.parMetadataRequestKind(ctx, transport, rel, "DOWNLOAD", "", 0, backend.Notebook)
	if err != nil {
		return backend.Metadata{}, err
	}
	if meta.Size == nil || *meta.Size < 0 {
		return backend.Metadata{}, syscall.ESTALE
	}
	m.Size = *meta.Size
	m.Version = meta.ETag
	return m, nil
}

func (r *rawNotebookReader) ReadFile(ctx context.Context, rel string) ([]byte, error) {
	kind, err := r.listed(ctx, rel)
	if err != nil {
		return nil, err
	}
	if kind != backend.Notebook {
		return r.Reader.ReadFile(ctx, rel)
	}
	transport, err := newOCIParTransport(r.region)
	if err != nil {
		return nil, err
	}
	meta, err := r.parMetadataRequestKind(ctx, transport, rel, "DOWNLOAD", "", 0, backend.Notebook)
	if err != nil {
		return nil, err
	}
	if meta.ETag == "" || meta.Size == nil {
		return nil, syscall.ESTALE
	}
	data, err := transport.downloadVersioned(ctx, meta.URL, meta.ETag)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != *meta.Size {
		return nil, syscall.ESTALE
	}
	return data, nil
}

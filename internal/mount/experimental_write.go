package mount

import (
	"context"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"sync"
	"syscall"
	"time"

	"aidpfs/internal/backend"
	"aidpfs/internal/writeback"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// experimentalBuffers is deliberately not exported or exposed by the CLI.
// It retains buffers for the lifetime of the mount to avoid dropping dirty or
// failed state. Production lifecycle, external invalidation, create/rename and
// cross-process locking remain unresolved.
type experimentalBuffers struct {
	mu     sync.Mutex
	values map[string]*writeback.Buffer
	open   func(context.Context, string) (*writeback.Buffer, error)
}

func (m *experimentalBuffers) get(ctx context.Context, rel string) (*writeback.Buffer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.values[rel]; b != nil {
		return b, nil
	}
	b, e := m.open(ctx, rel)
	if e != nil {
		return nil, e
	}
	m.values[rel] = b
	return b, nil
}
func (m *experimentalBuffers) find(rel string) *writeback.Buffer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.values[rel]
}

type experimentalNode struct {
	fs.Inode
	reader  backend.Reader
	buffers *experimentalBuffers
	rel     string
}
type editHandle struct {
	buffer   *writeback.Buffer
	writable bool
}

func editAttrs(out *fuse.Attr, m backend.Metadata, b *writeback.Buffer) {
	attributes(out, m)
	if m.Kind != backend.Directory {
		out.Mode = syscall.S_IFREG | 0600
	}
	if b != nil {
		out.Size = uint64(b.Size())
	}
}
func (n *experimentalNode) Getattr(ctx context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	m, e := n.reader.Stat(ctx, n.rel)
	if e != nil {
		return errno(e)
	}
	editAttrs(&out.Attr, m, n.buffers.find(n.rel))
	return 0
}
func (n *experimentalNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name == "" || name == "." || name == ".." || path.Base(name) != name {
		return nil, syscall.EINVAL
	}
	rel := name
	if n.rel != "" {
		rel = n.rel + "/" + name
	}
	m, e := n.reader.Stat(ctx, rel)
	if e != nil {
		return nil, errno(e)
	}
	editAttrs(&out.Attr, m, n.buffers.find(rel))
	child := &experimentalNode{reader: n.reader, buffers: n.buffers, rel: rel}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: mode(m.Kind) & syscall.S_IFMT}), 0
}
func (n *experimentalNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, e := n.reader.ReadDir(ctx, n.rel)
	if e != nil {
		return nil, errno(e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	out := make([]fuse.DirEntry, 0, len(entries))
	for _, v := range entries {
		out = append(out, fuse.DirEntry{Name: v.Name, Mode: mode(v.Kind)})
	}
	return fs.NewListDirStream(out), 0
}
func (n *experimentalNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&syscall.O_APPEND != 0 {
		return nil, 0, syscall.EOPNOTSUPP
	}
	b, e := n.buffers.get(ctx, n.rel)
	if e != nil {
		return nil, 0, errno(e)
	}
	writable := flags&syscall.O_ACCMODE != syscall.O_RDONLY
	if flags&syscall.O_TRUNC != 0 {
		if !writable {
			return nil, 0, syscall.EACCES
		}
		if e = b.Truncate(0); e != nil {
			return nil, 0, errno(e)
		}
	}
	return &editHandle{b, writable}, fuse.FOPEN_DIRECT_IO, 0
}
func (n *experimentalNode) Read(_ context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h, ok := fh.(*editHandle)
	if !ok {
		return nil, syscall.EBADF
	}
	count, e := h.buffer.ReadAt(dest, off)
	if e != nil && e != io.EOF {
		return nil, errno(e)
	}
	return fuse.ReadResultData(dest[:count]), 0
}
func (n *experimentalNode) Write(_ context.Context, fh fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	h, ok := fh.(*editHandle)
	if !ok || !h.writable {
		return 0, syscall.EBADF
	}
	count, e := h.buffer.WriteAt(data, off)
	return uint32(count), errno(e)
}
func (n *experimentalNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	size, ok := in.GetSize()
	if !ok || in.Valid & ^uint32(fuse.FATTR_SIZE|fuse.FATTR_FH|fuse.FATTR_LOCKOWNER) != 0 {
		return syscall.EOPNOTSUPP
	}
	if size > math.MaxInt64 {
		return syscall.EFBIG
	}
	var b *writeback.Buffer
	if h, ok := fh.(*editHandle); ok {
		if !h.writable {
			return syscall.EBADF
		}
		b = h.buffer
	} else {
		var e error
		b, e = n.buffers.get(ctx, n.rel)
		if e != nil {
			return errno(e)
		}
	}
	if e := b.Truncate(int64(size)); e != nil {
		return errno(e)
	}
	return n.Getattr(ctx, fh, out)
}
func (n *experimentalNode) Flush(ctx context.Context, fh fs.FileHandle) syscall.Errno {
	h, ok := fh.(*editHandle)
	if !ok {
		return syscall.EBADF
	}
	return errno(h.buffer.Sync(ctx))
}
func (n *experimentalNode) Fsync(ctx context.Context, fh fs.FileHandle, _ uint32) syscall.Errno {
	h, ok := fh.(*editHandle)
	if !ok {
		return syscall.EBADF
	}
	return errno(h.buffer.Sync(ctx))
}
func mountExperimental(ctx context.Context, mountpoint string, reader backend.Reader, open func(context.Context, string) (*writeback.Buffer, error)) (*fuse.Server, error) {
	m, e := reader.Stat(ctx, "")
	if e != nil {
		return nil, e
	}
	if m.Kind != backend.Directory {
		return nil, syscall.ENOTDIR
	}
	z := time.Duration(0)
	opts := &fs.Options{MountOptions: fuse.MountOptions{Options: []string{"default_permissions"}, FsName: "aidpfs-test", Name: "aidpfs-test", DisableReadDirPlus: true}, EntryTimeout: &z, AttrTimeout: &z, NegativeTimeout: &z, UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	buffers := &experimentalBuffers{values: map[string]*writeback.Buffer{}, open: open}
	return fs.Mount(mountpoint, &experimentalNode{reader: reader, buffers: buffers}, opts)
}

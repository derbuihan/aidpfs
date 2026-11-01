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

// experimentalBuffers keeps dirty or failed buffers after last close. Clean
// buffers are discarded after their last reference, allowing a later open to
// fetch a fresh version. This remains a test-only, process-local policy.
type experimentalEntry struct {
	buffer *writeback.Buffer
	refs   int
}
type experimentalBuffers struct {
	mu     sync.Mutex
	values map[string]*experimentalEntry
	open   func(context.Context, string) (*writeback.Buffer, error)
	create func(context.Context, string) error
	mkdir  func(context.Context, string) error
}

func (m *experimentalBuffers) get(ctx context.Context, rel string) (*writeback.Buffer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.values[rel]; entry != nil {
		entry.refs++
		return entry.buffer, nil
	}
	b, err := m.open(ctx, rel)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, syscall.EIO
	}
	m.values[rel] = &experimentalEntry{buffer: b, refs: 1}
	return b, nil
}
func (m *experimentalBuffers) release(rel string, b *writeback.Buffer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.values[rel]
	if entry == nil || entry.buffer != b || entry.refs == 0 {
		return
	}
	entry.refs--
	if entry.refs == 0 && !entry.buffer.Dirty() {
		delete(m.values, rel)
	}
}
func (m *experimentalBuffers) find(rel string) *writeback.Buffer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.values[rel]; entry != nil {
		return entry.buffer
	}
	return nil
}

func (m *experimentalBuffers) pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, entry := range m.values {
		if entry.buffer.Dirty() {
			count++
		}
	}
	return count
}

type experimentalNode struct {
	fs.Inode
	reader  backend.Reader
	buffers *experimentalBuffers
	rel     string
}
type editHandle struct {
	buffer      *writeback.Buffer
	writable    bool
	syncWrite   bool
	appendWrite bool
	metadata    backend.Metadata
}

func editAttrs(out *fuse.Attr, m backend.Metadata, b *writeback.Buffer) {
	attributes(out, m)
	if m.Kind == backend.Directory {
		out.Mode = syscall.S_IFDIR | 0700
	} else {
		out.Mode = syscall.S_IFREG | 0600
	}
	if b != nil {
		out.Size = uint64(b.Size())
	}
}

func (n *experimentalNode) Create(ctx context.Context, name string, flags uint32, _ uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.buffers.create == nil {
		return nil, nil, 0, syscall.EOPNOTSUPP
	}
	if name == "" || name == "." || name == ".." || path.Base(name) != name {
		return nil, nil, 0, syscall.EINVAL
	}
	rel := name
	if n.rel != "" {
		rel = n.rel + "/" + name
	}
	if err := n.buffers.create(ctx, rel); err != nil {
		return nil, nil, 0, errno(err)
	}
	// Once the remote create succeeds, failure to acquire a versioned snapshot
	// leaves the empty file present; callers must inspect it before retrying.
	b, err := n.buffers.get(ctx, rel)
	if err != nil {
		return nil, nil, 0, errno(err)
	}
	m := backend.Metadata{Name: name, Kind: backend.File}
	editAttrs(&out.Attr, m, b)
	child := &experimentalNode{reader: n.reader, buffers: n.buffers, rel: rel}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG})
	h := &editHandle{buffer: b, writable: flags&syscall.O_ACCMODE != syscall.O_RDONLY, syncWrite: flags&syscall.O_DSYNC != 0, appendWrite: flags&syscall.O_APPEND != 0, metadata: m}
	return inode, h, fuse.FOPEN_DIRECT_IO, 0
}

func (n *experimentalNode) Mkdir(ctx context.Context, name string, _ uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.buffers.mkdir == nil {
		return nil, syscall.EOPNOTSUPP
	}
	if name == "" || name == "." || name == ".." || path.Base(name) != name {
		return nil, syscall.EINVAL
	}
	rel := name
	if n.rel != "" {
		rel = n.rel + "/" + name
	}
	if err := n.buffers.mkdir(ctx, rel); err != nil {
		return nil, errno(err)
	}
	m := backend.Metadata{Name: name, Kind: backend.Directory}
	editAttrs(&out.Attr, m, nil)
	child := &experimentalNode{reader: n.reader, buffers: n.buffers, rel: rel}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
}

// The remote delete endpoints do not enforce the tested preconditions and can
// recursively remove nonempty folders. Refuse instead of risking data loss.
func (n *experimentalNode) Unlink(context.Context, string) syscall.Errno {
	return syscall.EOPNOTSUPP
}

func (n *experimentalNode) Rmdir(context.Context, string) syscall.Errno {
	return syscall.EOPNOTSUPP
}

// AIDP rename preconditions and open/dirty-handle semantics are unresolved.
func (n *experimentalNode) Rename(context.Context, string, fs.InodeEmbedder, string, uint32) syscall.Errno {
	return syscall.EOPNOTSUPP
}
func (n *experimentalNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if h, ok := fh.(*editHandle); ok {
		editAttrs(&out.Attr, h.metadata, h.buffer)
		return 0
	}
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
	m, e := n.reader.Stat(ctx, n.rel)
	if e != nil {
		return nil, 0, errno(e)
	}
	if m.Kind == backend.Directory {
		return nil, 0, syscall.EISDIR
	}
	b, e := n.buffers.get(ctx, n.rel)
	if e != nil {
		return nil, 0, errno(e)
	}
	writable := flags&syscall.O_ACCMODE != syscall.O_RDONLY
	if flags&syscall.O_TRUNC != 0 {
		if !writable {
			n.buffers.release(n.rel, b)
			return nil, 0, syscall.EACCES
		}
		if e = b.Truncate(0); e != nil {
			n.buffers.release(n.rel, b)
			return nil, 0, errno(e)
		}
	}
	return &editHandle{buffer: b, writable: writable, syncWrite: flags&syscall.O_DSYNC != 0, appendWrite: flags&syscall.O_APPEND != 0, metadata: m}, fuse.FOPEN_DIRECT_IO, 0
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
func (n *experimentalNode) Write(ctx context.Context, fh fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	h, ok := fh.(*editHandle)
	if !ok || !h.writable {
		return 0, syscall.EBADF
	}
	var count int
	var e error
	if h.appendWrite {
		count, e = h.buffer.Append(data)
	} else {
		count, e = h.buffer.WriteAt(data, off)
	}
	if e == nil && count > 0 && h.syncWrite {
		if e = h.buffer.Sync(ctx); e != nil {
			return 0, errno(e)
		}
	}
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
	var m backend.Metadata
	if h, ok := fh.(*editHandle); ok {
		if !h.writable {
			return syscall.EBADF
		}
		b = h.buffer
		m = h.metadata
	} else {
		var e error
		m, e = n.reader.Stat(ctx, n.rel)
		if e != nil {
			return errno(e)
		}
		b, e = n.buffers.get(ctx, n.rel)
		if e != nil {
			return errno(e)
		}
		defer n.buffers.release(n.rel, b)
	}
	if e := b.Truncate(int64(size)); e != nil {
		return errno(e)
	}
	editAttrs(&out.Attr, m, b)
	return 0
}
func (n *experimentalNode) Flush(ctx context.Context, fh fs.FileHandle) syscall.Errno {
	h, ok := fh.(*editHandle)
	if !ok {
		return syscall.EBADF
	}
	if !h.writable {
		return 0
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

// Release cannot report errors to close, so it never starts remote I/O.
func (n *experimentalNode) Release(_ context.Context, fh fs.FileHandle) syscall.Errno {
	if h, ok := fh.(*editHandle); ok {
		n.buffers.release(n.rel, h.buffer)
	}
	return 0
}
func mountExperimental(ctx context.Context, mountpoint string, reader backend.Reader, open func(context.Context, string) (*writeback.Buffer, error)) (*fuse.Server, error) {
	return mountExperimentalCreate(ctx, mountpoint, reader, open, nil, nil)
}
func mountExperimentalCreate(ctx context.Context, mountpoint string, reader backend.Reader, open func(context.Context, string) (*writeback.Buffer, error), create, mkdir func(context.Context, string) error) (*fuse.Server, error) {
	mounted, err := mountExperimentalWithState(ctx, mountpoint, reader, open, create, mkdir)
	if err != nil {
		return nil, err
	}
	return mounted.Server, nil
}

// ExperimentalMount exposes pending state after the FUSE server stops.
type ExperimentalMount struct {
	Server  *fuse.Server
	buffers *experimentalBuffers
}

// Pending returns the number of unsaved or failed file buffers.
func (m *ExperimentalMount) Pending() int { return m.buffers.pending() }

func mountExperimentalWithState(ctx context.Context, mountpoint string, reader backend.Reader, open func(context.Context, string) (*writeback.Buffer, error), create, mkdir func(context.Context, string) error) (*ExperimentalMount, error) {
	m, e := reader.Stat(ctx, "")
	if e != nil {
		return nil, e
	}
	if m.Kind != backend.Directory {
		return nil, syscall.ENOTDIR
	}
	z := time.Duration(0)
	opts := &fs.Options{MountOptions: fuse.MountOptions{Options: []string{"default_permissions"}, FsName: "aidpfs-test", Name: "aidpfs-test", DisableReadDirPlus: true}, EntryTimeout: &z, AttrTimeout: &z, NegativeTimeout: &z, UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	buffers := &experimentalBuffers{values: map[string]*experimentalEntry{}, open: open, create: create, mkdir: mkdir}
	server, err := fs.Mount(mountpoint, &experimentalNode{reader: reader, buffers: buffers}, opts)
	if err != nil {
		return nil, err
	}
	return &ExperimentalMount{Server: server, buffers: buffers}, nil
}

// MountExperimental provides explicitly opted-in experimental file writes. It is
// incomplete and must not be presented as a general purpose writable mount.
func MountExperimental(ctx context.Context, mountpoint string, reader backend.Reader, open func(context.Context, string) (*writeback.Buffer, error), create, mkdir func(context.Context, string) error) (*ExperimentalMount, error) {
	return mountExperimentalWithState(ctx, mountpoint, reader, open, create, mkdir)
}

package mount

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strings"
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
	mu              sync.Mutex
	namespace       sync.Mutex
	mutationBlocked bool
	activity        operationGate
	values          map[string]*experimentalEntry
	open            func(context.Context, string) (*writeback.Buffer, error)
	create          func(context.Context, string) error
	mkdir           func(context.Context, string) error
	mutate          func(context.Context, string, string, string) error
}

// operationGate prevents new state-changing operations after shutdown begins
// and waits for operations that already entered to finish.
type operationGate struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func (g *operationGate) begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.wg.Add(1)
	return true
}
func (g *operationGate) end() { g.wg.Done() }
func (g *operationGate) sealAndWait() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.wg.Wait()
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
	relMu   sync.RWMutex
	rel     string
}

func (n *experimentalNode) path() string {
	n.relMu.RLock()
	defer n.relMu.RUnlock()
	return n.rel
}

func (n *experimentalNode) setPath(rel string) {
	n.relMu.Lock()
	defer n.relMu.Unlock()
	n.rel = rel
}

type editHandle struct {
	buffer    *writeback.Buffer
	writable  bool
	syncWrite bool
	metadata  backend.Metadata
}

func (n *experimentalNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	return unsupportedStatfs(ctx, out)
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
	if !n.buffers.activity.begin() {
		return nil, nil, 0, syscall.ESHUTDOWN
	}
	defer n.buffers.activity.end()
	n.buffers.namespace.Lock()
	defer n.buffers.namespace.Unlock()
	if n.buffers.mutationBlocked {
		return nil, nil, 0, syscall.EIO
	}
	if n.buffers.create == nil {
		return nil, nil, 0, syscall.EOPNOTSUPP
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") || path.Base(name) != name {
		return nil, nil, 0, syscall.EINVAL
	}
	rel := name
	if n.path() != "" {
		rel = n.path() + "/" + name
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
	h := &editHandle{buffer: b, writable: flags&syscall.O_ACCMODE != syscall.O_RDONLY, syncWrite: flags&syscall.O_DSYNC != 0, metadata: m}
	return inode, h, fuse.FOPEN_DIRECT_IO, 0
}

func (n *experimentalNode) Mkdir(ctx context.Context, name string, _ uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if !n.buffers.activity.begin() {
		return nil, syscall.ESHUTDOWN
	}
	defer n.buffers.activity.end()
	n.buffers.namespace.Lock()
	defer n.buffers.namespace.Unlock()
	if n.buffers.mutationBlocked {
		return nil, syscall.EIO
	}
	if n.buffers.mkdir == nil {
		return nil, syscall.EOPNOTSUPP
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") || path.Base(name) != name {
		return nil, syscall.EINVAL
	}
	rel := name
	if n.path() != "" {
		rel = n.path() + "/" + name
	}
	if err := n.buffers.mkdir(ctx, rel); err != nil {
		return nil, errno(err)
	}
	m := backend.Metadata{Name: name, Kind: backend.Directory}
	editAttrs(&out.Attr, m, nil)
	child := &experimentalNode{reader: n.reader, buffers: n.buffers, rel: rel}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
}

// Unlink is limited to FILEs and requires the caller's exclusive use of the
// subtree: remote delete preconditions are not enforced in the tested tenant.
func (n *experimentalNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.mutate(ctx, "unlink", name, nil, "", 0)
}

func (n *experimentalNode) Rmdir(context.Context, string) syscall.Errno {
	return syscall.EOPNOTSUPP
}

// Rename rejects open buffers, replacement, and flags. It is not a distributed
// transaction and requires the same exclusive-use contract as unlink.
func (n *experimentalNode) Rename(ctx context.Context, name string, parent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	return n.mutate(ctx, "rename", name, parent, newName, flags)
}

func (n *experimentalNode) mutate(ctx context.Context, op, name string, parent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.buffers == nil || n.buffers.mutate == nil {
		return syscall.EOPNOTSUPP
	}
	if !n.buffers.activity.begin() {
		return syscall.ESHUTDOWN
	}
	defer n.buffers.activity.end()
	if n.buffers.mutate == nil {
		return syscall.EOPNOTSUPP
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") || path.Base(name) != name {
		return syscall.EINVAL
	}
	if flags != 0 {
		return syscall.EOPNOTSUPP
	}
	source := path.Join(n.path(), name)
	dest := ""
	if op == "rename" {
		p, ok := parent.(*experimentalNode)
		if !ok || p.buffers != n.buffers {
			return syscall.EXDEV
		}
		if newName == "" || newName == "." || newName == ".." || strings.ContainsAny(newName, "/\x00") || path.Base(newName) != newName {
			return syscall.EINVAL
		}
		dest = path.Join(p.path(), newName)
	}
	n.buffers.namespace.Lock()
	defer n.buffers.namespace.Unlock()
	if n.buffers.mutationBlocked {
		return syscall.EIO
	}
	n.buffers.mu.Lock()
	busy := n.buffers.values[source] != nil || (dest != "" && n.buffers.values[dest] != nil)
	n.buffers.mu.Unlock()
	if busy {
		return syscall.EBUSY
	}
	m, err := n.reader.Stat(ctx, source)
	if err != nil {
		return errno(err)
	}
	if m.Kind == backend.Directory {
		return syscall.EISDIR
	}
	if m.Kind != backend.File {
		return syscall.EOPNOTSUPP
	}
	if source == dest {
		return 0
	}
	if dest != "" {
		_, err = n.reader.Stat(ctx, dest)
		if err == nil {
			return syscall.EOPNOTSUPP
		}
		if !errors.Is(err, syscall.ENOENT) {
			return errno(err)
		}
	}
	if err = n.buffers.mutate(ctx, op, source, dest); err != nil {
		n.buffers.mutationBlocked = true
		return errno(err)
	}
	if dest != "" {
		if child := n.GetChild(name); child != nil {
			if node, ok := child.Operations().(*experimentalNode); ok {
				node.setPath(dest)
			}
		}
	}
	return 0
}
func (n *experimentalNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if h, ok := fh.(*editHandle); ok {
		editAttrs(&out.Attr, h.metadata, h.buffer)
		return 0
	}
	m, e := n.reader.Stat(ctx, n.path())
	if e != nil {
		return errno(e)
	}
	editAttrs(&out.Attr, m, n.buffers.find(n.path()))
	return 0
}
func (n *experimentalNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") || path.Base(name) != name {
		return nil, syscall.EINVAL
	}
	rel := name
	if n.path() != "" {
		rel = n.path() + "/" + name
	}
	m, e := n.reader.Stat(ctx, rel)
	if e != nil {
		return nil, errno(e)
	}
	editAttrs(&out.Attr, m, n.buffers.find(rel))
	// Reuse the live path inode while it remains in the FUSE tree. AIDP does
	// not expose a verified stable object identity, so this cannot detect all
	// external delete/recreate races.
	if existing := n.GetChild(name); existing != nil && existing.StableAttr().Mode == mode(m.Kind)&syscall.S_IFMT {
		return existing, 0
	}
	child := &experimentalNode{reader: n.reader, buffers: n.buffers, rel: rel}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: mode(m.Kind) & syscall.S_IFMT}), 0
}
func (n *experimentalNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, e := n.reader.ReadDir(ctx, n.path())
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
	if !n.buffers.activity.begin() {
		return nil, 0, syscall.ESHUTDOWN
	}
	defer n.buffers.activity.end()
	n.buffers.namespace.Lock()
	defer n.buffers.namespace.Unlock()
	if n.buffers.mutationBlocked {
		return nil, 0, syscall.EIO
	}
	m, e := n.reader.Stat(ctx, n.path())
	if e != nil {
		return nil, 0, errno(e)
	}
	if m.Kind == backend.Directory {
		return nil, 0, syscall.EISDIR
	}
	b, e := n.buffers.get(ctx, n.path())
	if e != nil {
		return nil, 0, errno(e)
	}
	writable := flags&syscall.O_ACCMODE != syscall.O_RDONLY
	if flags&syscall.O_TRUNC != 0 {
		if !writable {
			n.buffers.release(n.path(), b)
			return nil, 0, syscall.EACCES
		}
		if e = b.Truncate(0); e != nil {
			n.buffers.release(n.path(), b)
			return nil, 0, errno(e)
		}
	}
	return &editHandle{buffer: b, writable: writable, syncWrite: flags&syscall.O_DSYNC != 0, metadata: m}, fuse.FOPEN_DIRECT_IO, 0
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
	if !n.buffers.activity.begin() {
		return 0, syscall.ESHUTDOWN
	}
	defer n.buffers.activity.end()
	h, ok := fh.(*editHandle)
	if !ok || !h.writable {
		return 0, syscall.EBADF
	}
	// The kernel supplies the effective offset, including the current
	// O_APPEND state, which may change after open via fcntl.
	count, e := h.buffer.WriteAt(data, off)
	if e == nil && count > 0 && h.syncWrite {
		if e = h.buffer.Sync(ctx); e != nil {
			return 0, errno(e)
		}
	}
	return uint32(count), errno(e)
}
func (n *experimentalNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if !n.buffers.activity.begin() {
		return syscall.ESHUTDOWN
	}
	defer n.buffers.activity.end()
	n.buffers.namespace.Lock()
	defer n.buffers.namespace.Unlock()
	if n.buffers.mutationBlocked {
		return syscall.EIO
	}
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
		m, e = n.reader.Stat(ctx, n.path())
		if e != nil {
			return errno(e)
		}
		b, e = n.buffers.get(ctx, n.path())
		if e != nil {
			return errno(e)
		}
		defer n.buffers.release(n.path(), b)
	}
	if e := b.Truncate(int64(size)); e != nil {
		return errno(e)
	}
	editAttrs(&out.Attr, m, b)
	return 0
}
func (n *experimentalNode) Flush(ctx context.Context, fh fs.FileHandle) syscall.Errno {
	if !n.buffers.activity.begin() {
		return syscall.ESHUTDOWN
	}
	defer n.buffers.activity.end()
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
	if !n.buffers.activity.begin() {
		return syscall.ESHUTDOWN
	}
	defer n.buffers.activity.end()
	h, ok := fh.(*editHandle)
	if !ok {
		return syscall.EBADF
	}
	return errno(h.buffer.Sync(ctx))
}

// Release cannot report errors to close, so it never starts remote I/O.
func (n *experimentalNode) Release(_ context.Context, fh fs.FileHandle) syscall.Errno {
	if !n.buffers.activity.begin() {
		return 0
	}
	defer n.buffers.activity.end()
	if h, ok := fh.(*editHandle); ok {
		n.buffers.release(n.path(), h.buffer)
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

// Drain must be called after the FUSE serve loops stop, before inspecting
// pending state and the journal. New mutations are rejected after it starts.
func (m *ExperimentalMount) Drain() { m.buffers.activity.sealAndWait() }

func mountExperimentalWithState(ctx context.Context, mountpoint string, reader backend.Reader, open func(context.Context, string) (*writeback.Buffer, error), create, mkdir func(context.Context, string) error) (*ExperimentalMount, error) {
	return mountWithMutations(ctx, mountpoint, reader, open, create, mkdir, nil)
}

func mountWithMutations(ctx context.Context, mountpoint string, reader backend.Reader, open func(context.Context, string) (*writeback.Buffer, error), create, mkdir func(context.Context, string) error, mutate func(context.Context, string, string, string) error) (*ExperimentalMount, error) {
	m, e := reader.Stat(ctx, "")
	if e != nil {
		return nil, e
	}
	if m.Kind != backend.Directory {
		return nil, syscall.ENOTDIR
	}
	z := time.Duration(0)
	opts := &fs.Options{MountOptions: fuse.MountOptions{Options: []string{"default_permissions"}, FsName: "aidpfs-test", Name: "aidpfs-test", DisableReadDirPlus: true}, EntryTimeout: &z, AttrTimeout: &z, NegativeTimeout: &z, UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	buffers := &experimentalBuffers{values: map[string]*experimentalEntry{}, open: open, create: create, mkdir: mkdir, mutate: mutate}
	server, err := fs.Mount(mountpoint, &experimentalNode{reader: reader, buffers: buffers}, opts)
	if err != nil {
		return nil, err
	}
	return &ExperimentalMount{Server: server, buffers: buffers}, nil
}

func MountWithMutations(ctx context.Context, mountpoint string, reader backend.Reader, open func(context.Context, string) (*writeback.Buffer, error), create, mkdir func(context.Context, string) error, mutate func(context.Context, string, string, string) error) (*ExperimentalMount, error) {
	return mountWithMutations(ctx, mountpoint, reader, open, create, mkdir, mutate)
}

// MountExperimental provides the supported, explicitly enabled FILE write
// operations. Notebook writes, rename and deletion remain unsupported.
func MountExperimental(ctx context.Context, mountpoint string, reader backend.Reader, open func(context.Context, string) (*writeback.Buffer, error), create, mkdir func(context.Context, string) error) (*ExperimentalMount, error) {
	return mountExperimentalWithState(ctx, mountpoint, reader, open, create, mkdir)
}

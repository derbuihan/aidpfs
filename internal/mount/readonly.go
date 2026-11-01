// Package mount provides a Linux, owner-only, read-only filesystem.
package mount

import (
	"context"
	"errors"
	"os"
	"path"
	"sort"
	"syscall"
	"time"

	"aidpfs/internal/backend"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type node struct {
	fs.Inode
	reader backend.Reader
	rel    string
}
type snapshot struct {
	data     []byte
	metadata backend.Metadata
}

func errno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	var e syscall.Errno
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.Canceled) {
		return syscall.EINTR
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return syscall.ETIMEDOUT
	}
	return syscall.EIO
}
func mode(kind backend.Kind) uint32 {
	if kind == backend.Directory {
		return syscall.S_IFDIR | 0500
	}
	return syscall.S_IFREG | 0400
}
func attributes(out *fuse.Attr, m backend.Metadata) {
	out.Mode = mode(m.Kind)
	out.Owner = fuse.Owner{Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	if m.Size > 0 {
		out.Size = uint64(m.Size)
	}
	out.Nlink = 1
	if m.Kind == backend.Directory {
		out.Nlink = 2
	}
	if !m.Modified.IsZero() {
		out.SetTimes(nil, &m.Modified, nil)
	}
}
func (n *node) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if s, ok := fh.(*snapshot); ok {
		attributes(&out.Attr, s.metadata)
		return 0
	}
	m, err := n.reader.Stat(ctx, n.rel)
	if err != nil {
		return errno(err)
	}
	attributes(&out.Attr, m)
	return 0
}
func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name == "" || name == "." || name == ".." || path.Base(name) != name {
		return nil, syscall.EINVAL
	}
	rel := name
	if n.rel != "" {
		rel = n.rel + "/" + name
	}
	m, err := n.reader.Stat(ctx, rel)
	if err != nil {
		return nil, errno(err)
	}
	attributes(&out.Attr, m)
	child := &node{reader: n.reader, rel: rel}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: mode(m.Kind) & syscall.S_IFMT}), 0
}
func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := n.reader.ReadDir(ctx, n.rel)
	if err != nil {
		return nil, errno(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	list := make([]fuse.DirEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, fuse.DirEntry{Name: e.Name, Mode: mode(e.Kind)})
	}
	return fs.NewListDirStream(list), 0
}
func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&syscall.O_ACCMODE != syscall.O_RDONLY || flags&(syscall.O_TRUNC|syscall.O_APPEND) != 0 {
		return nil, 0, syscall.EROFS
	}
	metadata, err := n.reader.Stat(ctx, n.rel)
	if err != nil {
		return nil, 0, errno(err)
	}
	if metadata.Kind == backend.Directory {
		return nil, 0, syscall.EISDIR
	}
	data, err := n.reader.ReadFile(ctx, n.rel)
	if err != nil {
		return nil, 0, errno(err)
	}
	metadata.Size = int64(len(data))
	return &snapshot{data: data, metadata: metadata}, fuse.FOPEN_DIRECT_IO, 0
}
func (s *snapshot) Read(_ context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off < 0 {
		return nil, syscall.EINVAL
	}
	if off >= int64(len(s.data)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end < off || end > int64(len(s.data)) {
		end = int64(len(s.data))
	}
	return fuse.ReadResultData(s.data[off:end]), 0
}

// Mount validates the root before mounting. There is no daemon or disk cache.
// Each open receives a snapshot; subsequent opens fetch current content.
func Mount(ctx context.Context, mountpoint string, reader backend.Reader) (*fuse.Server, error) {
	m, err := reader.Stat(ctx, "")
	if err != nil {
		return nil, err
	}
	if m.Kind != backend.Directory {
		return nil, syscall.ENOTDIR
	}
	zero := time.Duration(0)
	opts := &fs.Options{MountOptions: fuse.MountOptions{Options: []string{"ro", "default_permissions"}, FsName: "aidpfs", Name: "aidpfs", DisableReadDirPlus: true}, EntryTimeout: &zero, AttrTimeout: &zero, NegativeTimeout: &zero, UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	return fs.Mount(mountpoint, &node{reader: reader}, opts)
}

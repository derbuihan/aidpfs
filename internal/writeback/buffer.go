package writeback

import (
	"context"
	"errors"
	"io"
	"sync"
	"syscall"

	"aidpfs/internal/recovery"
)

const maxBuffer = 64 << 20

var ErrRecoveryRequired = errors.New("write outcome requires recovery before further changes")

// Buffer is one shared, serialized view of an existing file. All handles for
// the same file must share it; it does not implement cross-process locking.
// Every accepted mutation first persists a full
// recovery snapshot, so a failed local journal write never changes the buffer.
type Buffer struct {
	mu          sync.Mutex
	target      Target
	baseETag    string
	data        []byte
	dirty       bool
	blocked     bool
	recoveryIDs []string
	latestID    string
	journal     *recovery.Store
	saver       Saver
}

func NewBuffer(target Target, baseETag string, initial []byte, journal *recovery.Store, remote Remote) (*Buffer, error) {
	if journal == nil || remote == nil || baseETag == "" || len(initial) > maxBuffer {
		return nil, syscall.EINVAL
	}
	return &Buffer{target: target, baseETag: baseETag, data: append([]byte(nil), initial...), journal: journal, saver: Saver{Journal: journal, Remote: remote}}, nil
}
func (b *Buffer) Size() int64 { b.mu.Lock(); defer b.mu.Unlock(); return int64(len(b.data)) }
func (b *Buffer) ReadAt(dst []byte, off int64) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if off < 0 {
		return 0, syscall.EINVAL
	}
	if off >= int64(len(b.data)) {
		return 0, io.EOF
	}
	n := copy(dst, b.data[off:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}
func (b *Buffer) WriteAt(src []byte, off int64) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writeAtLocked(src, off)
}

// Append selects the current end and persists the mutation under one lock.
func (b *Buffer) Append(src []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writeAtLocked(src, int64(len(b.data)))
}

func (b *Buffer) writeAtLocked(src []byte, off int64) (int, error) {
	if b.blocked {
		return 0, ErrRecoveryRequired
	}
	if off < 0 {
		return 0, syscall.EINVAL
	}
	if len(src) == 0 {
		return 0, nil
	}
	if off > maxBuffer || int64(len(src)) > int64(maxBuffer)-off {
		return 0, syscall.EFBIG
	}
	size := int(off) + len(src)
	if size < len(b.data) {
		size = len(b.data)
	}
	next := make([]byte, size)
	copy(next, b.data)
	copy(next[int(off):], src)
	if err := b.persist(next); err != nil {
		return 0, err
	}
	return len(src), nil
}
func (b *Buffer) Truncate(size int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blocked {
		return ErrRecoveryRequired
	}
	if size < 0 {
		return syscall.EINVAL
	}
	if size > maxBuffer {
		return syscall.EFBIG
	}
	if int64(len(b.data)) == size {
		return nil
	}
	next := make([]byte, int(size))
	copy(next, b.data)
	return b.persist(next)
}
func (b *Buffer) persist(data []byte) error {
	id, err := b.journal.CreateAfter(b.target.PlatformID, b.target.WorkspaceKey, b.target.RemotePath, b.baseETag, b.latestID, data)
	if id != "" {
		b.recoveryIDs = append(b.recoveryIDs, id)
	}
	if err != nil {
		return err
	}
	b.data = data
	b.latestID = id
	b.dirty = true
	return nil
}

// Sync returns only after conditional PUT, metadata completion and versioned
// verification. A failure freezes this buffer to prevent implicit retry or loss
// of the retained snapshot. Callers can obtain IDs for manual recovery.
func (b *Buffer) Sync(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blocked {
		return ErrRecoveryRequired
	}
	if !b.dirty {
		return nil
	}
	id, err := b.saver.SavePrepared(ctx, b.latestID, b.target, b.baseETag, b.data)
	if err != nil {
		b.blocked = true
		return err
	}
	record, _, err := b.journal.Load(id)
	if err != nil || record.Stage != recovery.Verified || record.PutETag == "" {
		b.blocked = true
		return ErrRecoveryRequired
	}
	b.baseETag = record.PutETag
	b.latestID = ""
	b.dirty = false
	return nil
}
func (b *Buffer) RecoveryIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.recoveryIDs...)
}
func (b *Buffer) Dirty() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.dirty }

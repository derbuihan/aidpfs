// Package writeback coordinates a conditional update and a local recovery
// journal. It is not connected to the FUSE mount.
package writeback

import (
	"context"
	"errors"
	"fmt"

	"aidpfs/internal/recovery"
)

type Target struct{ PlatformID, WorkspaceKey, RemotePath string }

// Upload is an ephemeral write capability. It must never be serialized or
// logged, and Put must not automatically retry an ambiguous request.
type Upload interface {
	Put(context.Context, string, []byte) (string, error)
}
type Remote interface {
	Refresh(context.Context, Target) (Upload, error)
	Complete(context.Context, Target, string, int64) error
	// Verify checks both version and content of a consistent remote snapshot.
	Verify(context.Context, Target, string, []byte) error
}
type Journal interface {
	Create(string, string, string, string, []byte) (string, error)
	Advance(string, recovery.Stage, string) error
}
type Saver struct {
	Journal Journal
	Remote  Remote
}

// SaveExisting saves a snapshot obtained together with baseETag. The returned
// journal ID remains valid even on failure, and no entry is deleted on success.
// Success means verification observed this version; it cannot prevent a later
// external change. Callers must not use it as a distributed lock or retry an
// ambiguous error without reconciling the journal and remote state.
func (s Saver) SaveExisting(ctx context.Context, target Target, baseETag string, data []byte) (string, error) {
	if s.Journal == nil || s.Remote == nil {
		return "", errors.New("writeback is not configured")
	}
	// Use one immutable copy for both durable recovery and network transfer.
	snapshot := append([]byte(nil), data...)
	id, err := s.Journal.Create(target.PlatformID, target.WorkspaceKey, target.RemotePath, baseETag, snapshot)
	if err != nil {
		return id, fmt.Errorf("persist recovery data: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return id, err
	}
	upload, err := s.Remote.Refresh(ctx, target)
	if err != nil {
		return id, fmt.Errorf("prepare upload: %w", err)
	}
	if upload == nil {
		return id, errors.New("prepare upload: missing transfer")
	}
	if err = s.Journal.Advance(id, recovery.PutStarted, ""); err != nil {
		return id, fmt.Errorf("persist PUT start: %w", err)
	}
	tag, err := upload.Put(ctx, baseETag, snapshot)
	if err != nil {
		return id, fmt.Errorf("conditional PUT: %w", err)
	}
	if err = s.Journal.Advance(id, recovery.PutConfirmed, tag); err != nil {
		return id, fmt.Errorf("persist PUT result: %w", err)
	}
	if err = s.Journal.Advance(id, recovery.UpdateStarted, ""); err != nil {
		return id, fmt.Errorf("persist metadata update start: %w", err)
	}
	if err = s.Remote.Complete(ctx, target, tag, int64(len(snapshot))); err != nil {
		return id, fmt.Errorf("complete metadata: %w", err)
	}
	if err = s.Remote.Verify(ctx, target, tag, snapshot); err != nil {
		return id, fmt.Errorf("verify remote result: %w", err)
	}
	if err = s.Journal.Advance(id, recovery.Verified, ""); err != nil {
		return id, fmt.Errorf("persist verification: %w", err)
	}
	return id, nil
}

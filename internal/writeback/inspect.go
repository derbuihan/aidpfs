package writeback

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"aidpfs/internal/recovery"
)

type Snapshot struct {
	ETag string
	Data []byte
}
type Inspector interface {
	Snapshot(context.Context, Target) (Snapshot, error)
}
type Observation string

const (
	BaseVersion                Observation = "base_version"
	UploadedVersion            Observation = "uploaded_version"
	ContentMatchUnknownVersion Observation = "content_match_unknown_version"
	RemoteChanged              Observation = "remote_changed"
)

// Inspect compares one consistent remote snapshot with a retained record. It
// does not mutate either side or claim that a previous operation succeeded.
// Even UploadedVersion says nothing about future changes or metadata completion.
func Inspect(ctx context.Context, record recovery.Record, local []byte, remote Inspector) (Observation, error) {
	if remote == nil || record.BaseETag == "" {
		return "", errors.New("invalid recovery inspection")
	}
	snapshot, err := remote.Snapshot(ctx, Target{record.PlatformID, record.WorkspaceKey, record.RemotePath})
	if err != nil {
		return "", err
	}
	if snapshot.ETag == "" {
		return "", errors.New("remote version unavailable")
	}
	eq := func(a, b string) bool {
		normalize := func(s string) string {
			if len(s) >= 2 && strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
				return s[1 : len(s)-1]
			}
			return s
		}
		return normalize(a) == normalize(b)
	}
	if record.PutETag != "" && eq(snapshot.ETag, record.PutETag) && bytes.Equal(snapshot.Data, local) {
		return UploadedVersion, nil
	}
	if eq(snapshot.ETag, record.BaseETag) {
		return BaseVersion, nil
	}
	if bytes.Equal(snapshot.Data, local) {
		return ContentMatchUnknownVersion, nil
	}
	return RemoteChanged, nil
}

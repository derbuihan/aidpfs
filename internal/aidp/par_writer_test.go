package aidp

import (
	"aidpfs/internal/writeback"
	"errors"
	"syscall"
	"testing"
)

func TestPARWriterRejectsDifferentTarget(t *testing.T) {
	r := serverReader(t, nil)
	w := parWriter{reader: r}
	for _, target := range []writeback.Target{
		{PlatformID: "other", WorkspaceKey: "workspace", RemotePath: "/Workspace/a"},
		{PlatformID: "platform", WorkspaceKey: "other", RemotePath: "/Workspace/a"},
		{PlatformID: "platform", WorkspaceKey: "workspace", RemotePath: "/Workspace2/a"},
		{PlatformID: "platform", WorkspaceKey: "workspace", RemotePath: "/Workspace/../outside"},
	} {
		if _, err := w.relative(target); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("unexpected target: %v", err)
		}
	}
}

package aidp

import (
	"aidpfs/internal/recovery"
	"aidpfs/internal/writeback"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
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

func TestExperimentalBufferRejectsNotebook(t *testing.T) {
	r := serverReader(t, func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"items":[{"displayName":"test.ipynb","path":"/Workspace/test.ipynb","type":"NOTEBOOK"}]}`)
	})
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := recovery.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.OpenExperimentalBuffer(context.Background(), "test.ipynb", "ap-osaka-1", journal); !errors.Is(err, syscall.EOPNOTSUPP) {
		t.Fatalf("notebook accepted: %v", err)
	}
}

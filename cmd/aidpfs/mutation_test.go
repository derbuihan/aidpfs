package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"

	"aidpfs/internal/aidp"
	"aidpfs/internal/backend"
	"aidpfs/internal/config"
	"aidpfs/internal/recovery"
)

type mutationFake struct {
	changed bool
	corrupt bool
	calls   int
}

func TestJournalMutationRecordsSuccessAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		store, err := recovery.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		c := config.Config{PlatformID: "p", WorkspaceKey: "w", RemotePath: "/Workspace"}
		err = journalMutation(context.Background(), store, c, "rename", "a", "b", func(_ context.Context, op, src, dst string) error {
			calls++
			items, e := store.ListMutations()
			if e != nil || len(items) != 1 || items[0].Confirmed || items[0].Source != "/Workspace/a" || items[0].Destination != "/Workspace/b" {
				t.Errorf("intent not present before request: %+v %v", items, e)
			}
			if fail {
				return syscall.EIO
			}
			return nil
		})
		if (err != nil) != fail || calls != 1 {
			t.Fatalf("error=%v calls=%d", err, calls)
		}
		items, err := store.ListMutations()
		if err != nil || len(items) != 1 || items[0].Confirmed == fail {
			t.Fatalf("intent=%+v error=%v", items, err)
		}
		if err = checkRecoveryAfterUnmount(0, store); (err != nil) != fail {
			t.Fatalf("shutdown error=%v, fail=%t", err, fail)
		}
	}
}

func (f *mutationFake) ReadFile(_ context.Context, p string) ([]byte, error) {
	if p == "a" && !f.changed {
		return []byte("original"), nil
	}
	if p == "b" && f.changed {
		if f.corrupt {
			return []byte("wrong"), nil
		}
		return []byte("original"), nil
	}
	return nil, syscall.ENOENT
}
func (f *mutationFake) ListedKind(_ context.Context, p string) (backend.Kind, error) {
	if p == "a" && f.changed {
		return 0, syscall.ENOENT
	}
	if p == "b" && f.changed {
		return backend.File, nil
	}
	return 0, syscall.ENOENT
}
func (f *mutationFake) MutateFile(context.Context, string, string, string) error {
	f.calls++
	f.changed = true
	return nil
}

func TestVerifiedMutation(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		f := &mutationFake{corrupt: corrupt}
		err := verifiedMutation(context.Background(), f, "rename", "a", "b")
		if corrupt && !errors.Is(err, aidp.ErrOutcomeUnknown) {
			t.Fatalf("corrupt: %v", err)
		}
		if !corrupt && err != nil {
			t.Fatal(err)
		}
		if f.calls != 1 {
			t.Fatalf("calls: %d", f.calls)
		}
	}
}

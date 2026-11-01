package writeback

import (
	"bytes"
	"context"
	"errors"
	"os"
	"syscall"
	"testing"

	"aidpfs/internal/recovery"
)

var errUnknown = errors.New("outcome unknown")

type fakeRemote struct {
	fail  string
	data  []byte
	tag   string
	calls []string
}

func (f *fakeRemote) Refresh(_ context.Context, _ Target) (Upload, error) {
	f.calls = append(f.calls, "refresh")
	if f.fail == "refresh" {
		return nil, errUnknown
	}
	return f, nil
}
func (f *fakeRemote) Put(_ context.Context, etag string, data []byte) (string, error) {
	f.calls = append(f.calls, "put")
	if f.fail == "put" {
		return "", errUnknown
	}
	if etag != f.tag {
		return "", syscall.ESTALE
	}
	f.tag = "new"
	f.data = append([]byte(nil), data...)
	return f.tag, nil
}
func (f *fakeRemote) Complete(_ context.Context, _ Target, _ string, _ int64) error {
	f.calls = append(f.calls, "complete")
	if f.fail == "complete" {
		return errUnknown
	}
	if f.fail == "external" {
		f.tag = "other"
		f.data = []byte("external")
	}
	return nil
}
func (f *fakeRemote) Verify(_ context.Context, _ Target, tag string, data []byte) error {
	f.calls = append(f.calls, "verify")
	if f.fail == "verify" {
		return errUnknown
	}
	if f.tag != tag || !bytes.Equal(f.data, data) {
		return syscall.ESTALE
	}
	return nil
}
func TestSaveKeepsRecoveryDataAcrossFailures(t *testing.T) {
	for _, tt := range []struct {
		fail  string
		stage recovery.Stage
		want  error
	}{{"", recovery.Verified, nil}, {"refresh", recovery.Prepared, errUnknown}, {"put", recovery.PutStarted, errUnknown}, {"complete", recovery.UpdateStarted, errUnknown}, {"verify", recovery.UpdateStarted, errUnknown}, {"external", recovery.UpdateStarted, syscall.ESTALE}} {
		t.Run(tt.fail, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			journal, err := recovery.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			remote := &fakeRemote{fail: tt.fail, tag: "old"}
			target := Target{"platform", "workspace", "/Workspace/f"}
			id, err := (Saver{journal, remote}).SaveExisting(context.Background(), target, "old", []byte("user data"))
			if id == "" || !errors.Is(err, tt.want) {
				t.Fatalf("id present=%v error=%v", id != "", err)
			}
			record, data, e := journal.Load(id)
			if e != nil || record.Stage != tt.stage || !bytes.Equal(data, []byte("user data")) {
				t.Fatalf("stage %v read error %v", record.Stage, e)
			}
			if tt.stage == recovery.UpdateStarted && record.PutETag != "new" {
				t.Fatal("PUT version not retained")
			}
		})
	}
}
func TestCancelledSaveRetainsPreparedData(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	journal, _ := recovery.Open(dir)
	remote := &fakeRemote{tag: "old"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id, err := (Saver{journal, remote}).SaveExisting(ctx, Target{"p", "w", "/Workspace/f"}, "old", []byte("data"))
	if !errors.Is(err, context.Canceled) || len(remote.calls) != 0 {
		t.Fatalf("err %v calls %v", err, remote.calls)
	}
	r, _, err := journal.Load(id)
	if err != nil || r.Stage != recovery.Prepared {
		t.Fatalf("stage %v err %v", r.Stage, err)
	}
}

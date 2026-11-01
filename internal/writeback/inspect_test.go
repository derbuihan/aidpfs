package writeback

import (
	"aidpfs/internal/recovery"
	"context"
	"errors"
	"testing"
)

type fakeInspector struct {
	s   Snapshot
	err error
}

func (f fakeInspector) Snapshot(context.Context, Target) (Snapshot, error) { return f.s, f.err }
func TestInspectionDoesNotInferSuccessFromContentAlone(t *testing.T) {
	record := recovery.Record{PlatformID: "p", WorkspaceKey: "w", RemotePath: "/Workspace/f", BaseETag: "base", PutETag: "put"}
	for _, tt := range []struct {
		tag, data string
		want      Observation
	}{{"base", "original", BaseVersion}, {`"put"`, "saved", UploadedVersion}, {"other", "saved", ContentMatchUnknownVersion}, {"other", "other", RemoteChanged}, {"put", "other", RemoteChanged}} {
		got, err := Inspect(context.Background(), record, []byte("saved"), fakeInspector{s: Snapshot{tt.tag, []byte(tt.data)}})
		if err != nil || got != tt.want {
			t.Fatalf("got %s error %v", got, err)
		}
	}
	record.PutETag = ""
	got, err := Inspect(context.Background(), record, []byte("saved"), fakeInspector{s: Snapshot{"other", []byte("saved")}})
	if err != nil || got != ContentMatchUnknownVersion {
		t.Fatal("ambiguous PUT treated as success")
	}
	_, err = Inspect(context.Background(), record, nil, fakeInspector{err: errors.New("unavailable")})
	if err == nil {
		t.Fatal("remote failure suppressed")
	}
}

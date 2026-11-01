package writeback

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"aidpfs/internal/recovery"
)

type crashRemote struct{ base string }

func (r crashRemote) Refresh(context.Context, Target) (Upload, error) { return r, nil }
func (r crashRemote) Put(ctx context.Context, _ string, data []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, r.base+"/put", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("test PUT failed")
	}
	return "written-version", nil
}
func (r crashRemote) Complete(ctx context.Context, _ Target, _ string, _ int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.base+"/complete", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("test completion failed")
	}
	return nil
}
func (r crashRemote) Verify(context.Context, Target, string, []byte) error { return nil }
func TestWritebackCrashChild(t *testing.T) {
	if os.Getenv("AIDPFS_WRITEBACK_CRASH") != "1" {
		t.Skip("subprocess helper")
	}
	journal, err := recovery.Open(os.Getenv("AIDPFS_WRITEBACK_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = (Saver{Journal: journal, Remote: crashRemote{os.Getenv("AIDPFS_WRITEBACK_SERVER")}}).SaveExisting(context.Background(), Target{"p", "w", "/Workspace/f"}, "base-version", []byte("synthetic data"))
	if err != nil {
		t.Fatal(err)
	}
}
func TestKillAfterRemoteMutationRetainsAmbiguousJournal(t *testing.T) {
	for _, phase := range []string{"put", "complete"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			reached := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var mu sync.Mutex
			var remoteData []byte
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				if r.URL.Path == "/put" {
					remoteData, _ = io.ReadAll(r.Body)
				}
				mu.Unlock()
				if r.URL.Path == "/"+phase {
					reached <- struct{}{}
					<-release
				}
				w.WriteHeader(200)
			}))
			defer server.Close()
			defer unblock()
			cmd := exec.Command(os.Args[0], "-test.run=^TestWritebackCrashChild$")
			cmd.Env = append(os.Environ(), "AIDPFS_WRITEBACK_CRASH=1", "AIDPFS_WRITEBACK_DIR="+dir, "AIDPFS_WRITEBACK_SERVER="+server.URL)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			select {
			case <-reached:
			case <-time.After(10 * time.Second):
				t.Fatal("remote operation was not reached")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			unblock()
			store, err := recovery.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := store.List()
			if err != nil || len(entries) != 1 || entries[0].Invalid {
				t.Fatalf("journal count %d error %v", len(entries), err)
			}
			record, data, err := store.Load(entries[0].ID)
			if err != nil || !bytes.Equal(data, []byte("synthetic data")) {
				t.Fatalf("recovery data not retained: %v", err)
			}
			expectedStage := recovery.PutStarted
			expectedObservation := ContentMatchUnknownVersion
			if phase == "complete" {
				expectedStage = recovery.UpdateStarted
				expectedObservation = UploadedVersion
				if record.PutETag != "written-version" {
					t.Fatal("confirmed version missing")
				}
			}
			if record.Stage != expectedStage {
				t.Fatalf("stage %s", record.Stage)
			}
			mu.Lock()
			snapshot := append([]byte(nil), remoteData...)
			requestCount := calls
			mu.Unlock()
			obs, err := Inspect(context.Background(), record, data, fakeInspector{s: Snapshot{ETag: "written-version", Data: snapshot}})
			if err != nil || obs != expectedObservation {
				t.Fatalf("observation %s error %v", obs, err)
			}
			expectedCalls := 1
			if phase == "complete" {
				expectedCalls = 2
			}
			if requestCount != expectedCalls {
				t.Fatalf("unexpected requests: %d", requestCount)
			}
		})
	}
}

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"aidpfs/internal/config"
	"aidpfs/internal/recovery"
)

func TestCreateCrashChild(t *testing.T) {
	if os.Getenv("AIDPFS_CREATE_CRASH") != "1" {
		t.Skip("subprocess helper")
	}
	store, err := recovery.Open(os.Getenv("AIDPFS_CREATE_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PlatformID: "platform", WorkspaceKey: "workspace", RemotePath: "/Workspace"}
	err = journalCreate(context.Background(), store, cfg, "file", "FILE", func(ctx context.Context, _ string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, os.Getenv("AIDPFS_CREATE_SERVER"), nil)
		if err != nil {
			return err
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJournalCreateRecordsBeforeRemoteAndConfirmsOnlySuccess(t *testing.T) {
	for _, tc := range []struct {
		name      string
		confirmed bool
	}{
		{name: "success", confirmed: true},
		{name: "remote failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			store, err := recovery.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{PlatformID: "platform", WorkspaceKey: "workspace", RemotePath: "/Workspace"}
			calls := 0
			var wantErr error
			if !tc.confirmed {
				wantErr = errors.New("remote create failed")
			}
			err = journalCreate(context.Background(), store, cfg, "nested/file", "FILE", func(_ context.Context, rel string) error {
				calls++
				if rel != "nested/file" {
					t.Fatalf("remote path argument = %q", rel)
				}
				intents, listErr := store.ListCreates()
				if listErr != nil || len(intents) != 1 || intents[0].Confirmed || intents[0].RemotePath != "/Workspace/nested/file" {
					t.Fatalf("intent not durable before remote create: %v %v", intents, listErr)
				}
				return wantErr
			})
			if calls != 1 {
				t.Fatalf("remote create calls = %d, want 1", calls)
			}
			if (err == nil) != tc.confirmed {
				t.Fatalf("journalCreate error = %v, confirmed = %v", err, tc.confirmed)
			}
			intents, err := store.ListCreates()
			if err != nil || len(intents) != 1 || intents[0].Confirmed != tc.confirmed || intents[0].Invalid {
				t.Fatalf("final intent state = %v, err = %v", intents, err)
			}
		})
	}
}

func TestCreateCrashAfterServerAcceptanceRetainsIntent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached <- struct{}{}
		<-release
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	defer unblock()
	child := exec.Command(os.Args[0], "-test.run=^TestCreateCrashChild$")
	child.Env = append(os.Environ(), "AIDPFS_CREATE_CRASH=1", "AIDPFS_CREATE_DIR="+dir, "AIDPFS_CREATE_SERVER="+server.URL)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("server did not receive create")
	}
	store, err := recovery.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ListCreates()
	if err != nil || len(before) != 1 || before[0].Confirmed || before[0].Invalid {
		t.Fatalf("intent not persisted before request: %v %v", before, err)
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	unblock()
	after, err := store.ListCreates()
	if err != nil || len(after) != 1 || after[0].Confirmed || after[0].Invalid || after[0].RemotePath != "/Workspace/file" {
		t.Fatalf("intent after crash: %v %v", after, err)
	}
	if err = checkRecoveryAfterUnmount(0, store); err == nil {
		t.Fatal("unconfirmed intent not reported")
	}
}

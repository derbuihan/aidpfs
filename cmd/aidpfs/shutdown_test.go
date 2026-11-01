package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"aidpfs/internal/backend"
	"aidpfs/internal/mount"
)

type testMountServer struct {
	stopped  chan struct{}
	attempts chan int
	mu       sync.Mutex
	calls    int
}

type emptyReader struct{}

func (emptyReader) Stat(context.Context, string) (backend.Metadata, error) {
	return backend.Metadata{Kind: backend.Directory}, nil
}
func (emptyReader) ReadDir(context.Context, string) ([]backend.Metadata, error) { return nil, nil }
func (emptyReader) ReadFile(context.Context, string) ([]byte, error)            { return nil, syscall.EISDIR }

func TestSignalUnmountsRealFuseMount(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("FUSE unavailable")
	}
	server, err := mount.Mount(context.Background(), t.TempDir(), emptyReader{})
	if err != nil {
		t.Skipf("FUSE mount unavailable: %v", err)
	}
	signals := make(chan os.Signal, 1)
	var out bytes.Buffer
	returned := make(chan struct{})
	go func() { waitForUnmount(server, signals, &out); close(returned) }()
	signals <- syscall.SIGTERM
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("signal unmount did not complete")
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected unmount error: %s", out.String())
	}
}

func (s *testMountServer) Wait() { <-s.stopped }
func (s *testMountServer) Unmount() error {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	s.attempts <- n
	if n == 1 {
		return errors.New("busy")
	}
	close(s.stopped)
	return nil
}
func TestUnmountFailureReportedAndSignalRetried(t *testing.T) {
	server := &testMountServer{stopped: make(chan struct{}), attempts: make(chan int, 2)}
	signals := make(chan os.Signal, 2)
	var out bytes.Buffer
	returned := make(chan struct{})
	go func() { waitForUnmount(server, signals, &out); close(returned) }()
	signals <- syscall.SIGTERM
	select {
	case <-server.attempts:
	case <-time.After(2 * time.Second):
		t.Fatal("first unmount not attempted")
	}
	select {
	case <-returned:
		t.Fatal("returned after failed unmount")
	default:
	}
	signals <- os.Interrupt
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("second unmount did not stop")
	}
	if !strings.Contains(out.String(), "unmount failed") || !strings.Contains(out.String(), "busy") {
		t.Fatal("failure not reported")
	}
	server.mu.Lock()
	calls := server.calls
	server.mu.Unlock()
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}

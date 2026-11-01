package mount

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFuseFileLimitChild(t *testing.T) {
	if os.Getenv("AIDPFS_TEST_FUSE_LIMIT") != "1" {
		t.Skip("subprocess only")
	}
	remote := &mountRemote{tag: "old"}
	dir, store := testExperimentalMount(t, remote)
	f, err := os.OpenFile(filepath.Join(dir, "file"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	if err = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 1, Max: 1}); err != nil {
		t.Fatal(err)
	}
	n, err := f.WriteAt([]byte("new"), 0)
	if n != 0 || !errors.Is(err, syscall.EFBIG) {
		t.Fatalf("write: %d %v", n, err)
	}
	data := make([]byte, 4)
	if n, err = f.ReadAt(data, 0); err != nil || n != 4 || string(data) != "abcd" {
		t.Fatal("failed write changed data")
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	puts := remote.puts
	remote.mu.Unlock()
	if puts != 0 {
		t.Fatal("failed write reached remote")
	}
	entries, err := store.List()
	if err != nil || len(entries) != 1 || !entries[0].Invalid {
		t.Fatalf("incomplete entry: %v %v", entries, err)
	}
	t.Log("FUSE write rejected and incomplete journal retained")
}

func TestFuseFileLimitFailure(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("FUSE unavailable")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestFuseFileLimitChild$", "-test.v")
	child.Env = append(os.Environ(), "AIDPFS_TEST_FUSE_LIMIT=1")
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v output=%s", err, out)
	}
	if !bytes.Contains(out, []byte("FUSE write rejected and incomplete journal retained")) {
		t.Skipf("FUSE mount unavailable: %s", out)
	}
}

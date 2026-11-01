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

func TestExperimentalMountENOSPCChild(t *testing.T) {
	if os.Getenv("AIDPFS_TEST_ENOSPC") != "1" {
		t.Skip("real filesystem ENOSPC probe requires scripts/tests/run-enospc.sh")
	}
	remote := &mountRemote{tag: "old"}
	dir, _ := testExperimentalMount(t, remote)
	reserve, err := os.CreateTemp(os.TempDir(), "aidpfs-enospc-reserve-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(reserve.Name()) })
	chunk := bytes.Repeat([]byte{'x'}, 1<<20)
	for {
		if _, err = reserve.Write(chunk); errors.Is(err, syscall.ENOSPC) {
			break
		} else if err != nil {
			t.Fatalf("fill test filesystem: %v", err)
		}
	}
	if err = reserve.Sync(); err != nil && !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("sync full test filesystem: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "file"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	written, writeErr := f.WriteAt([]byte("cannot-journal"), 0)
	if written != 0 || !errors.Is(writeErr, syscall.ENOSPC) {
		_ = f.Close()
		t.Fatalf("FUSE write on full recovery filesystem = %d, %v; want ENOSPC", written, writeErr)
	}
	data := make([]byte, 4)
	if n, readErr := f.ReadAt(data, 0); readErr != nil || n != 4 || string(data) != "abcd" {
		_ = f.Close()
		t.Fatalf("failed journal write changed mounted data: %d %q %v", n, data, readErr)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	puts := remote.puts
	remote.mu.Unlock()
	if puts != 0 {
		t.Fatalf("remote PUTs after local ENOSPC = %d", puts)
	}
	t.Log("real ENOSPC rejected the FUSE write, kept the old buffer, and sent no remote PUT")
}

func TestExperimentalMountENOSPC(t *testing.T) {
	if os.Getenv("AIDPFS_TEST_ENOSPC") != "1" {
		t.Skip("real filesystem ENOSPC probe requires scripts/tests/run-enospc.sh")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestExperimentalMountENOSPCChild$", "-test.v")
	child.Env = append(os.Environ(), "AIDPFS_TEST_ENOSPC=1")
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("ENOSPC child failed: %v output=%s", err, out)
	}
	if !bytes.Contains(out, []byte("real ENOSPC rejected the FUSE write")) {
		t.Fatalf("ENOSPC child did not report its evidence: %s", out)
	}
}

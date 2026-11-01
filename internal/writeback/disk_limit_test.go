package writeback

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"

	"aidpfs/internal/recovery"
)

// Run in a subprocess so the per-process file limit cannot affect the test runner.
func TestJournalFileLimitChild(t *testing.T) {
	dir := os.Getenv("AIDPFS_TEST_FILE_LIMIT_DIR")
	if dir == "" {
		t.Skip("subprocess only")
	}
	store, err := recovery.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{tag: "old"}
	buffer, err := NewBuffer(Target{"p", "w", "/Workspace/file"}, "old", []byte("abcd"), store, remote)
	if err != nil {
		t.Fatal(err)
	}
	expected := "abcd"
	if os.Getenv("AIDPFS_TEST_PRIOR_EDIT") == "1" {
		if _, err = buffer.WriteAt([]byte("X"), 0); err != nil {
			t.Fatal(err)
		}
		expected = "Xbcd"
	}
	signal.Ignore(syscall.SIGXFSZ)
	if err = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 1, Max: 1}); err != nil {
		t.Fatal(err)
	}
	n, err := buffer.WriteAt([]byte("new"), 0)
	if n != 0 || !errors.Is(err, syscall.EFBIG) {
		t.Fatalf("expected file limit error: n=%d err=%v", n, err)
	}
	data := make([]byte, 4)
	if n, err = buffer.ReadAt(data, 0); err != nil || n != 4 || string(data) != expected {
		t.Fatal("failed mutation changed buffer")
	}
	if expected == "abcd" {
		if buffer.Dirty() {
			t.Fatal("failed mutation marked dirty")
		}
		if err = buffer.Sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	} else {
		if !buffer.Dirty() {
			t.Fatal("earlier edit lost dirty state")
		}
		entries, listErr := store.List()
		if listErr != nil || len(entries) != 2 {
			t.Fatalf("prior entries: %v", listErr)
		}
		found := false
		for _, entry := range entries {
			if entry.Invalid {
				continue
			}
			_, saved, loadErr := store.Load(entry.ID)
			if loadErr == nil && string(saved) == expected {
				found = true
			}
		}
		if !found {
			t.Fatal("prior edit not retained in journal")
		}
	}
	if len(remote.calls) != 0 {
		t.Fatal("failed mutation reached remote")
	}
	unresolved, err := store.UnresolvedSnapshots()
	want := 1
	if expected != "abcd" {
		want = 2
	}
	if err != nil || len(unresolved) != want {
		t.Fatalf("unresolved=%d err=%v", len(unresolved), err)
	}
}

func TestJournalFileLimitPreservesEarlierEdit(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestJournalFileLimitChild$")
	child.Env = append(os.Environ(), "AIDPFS_TEST_FILE_LIMIT_DIR="+dir, "AIDPFS_TEST_PRIOR_EDIT=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child failed: %v output=%s", err, output)
	}
}

func TestJournalFileLimitFailureRetainsIncompleteEntry(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestJournalFileLimitChild$")
	child.Env = append(os.Environ(), "AIDPFS_TEST_FILE_LIMIT_DIR="+dir)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child failed: %v output=%s", err, output)
	}
	store, err := recovery.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.List()
	if err != nil || len(entries) != 1 || !entries[0].Invalid {
		t.Fatalf("incomplete entry not visible: %v %v", entries, err)
	}
}

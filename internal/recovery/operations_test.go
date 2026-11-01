package recovery

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestListAndExportPreserveJournal(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Create("platform", "workspace", "/Workspace/f", "version", []byte("secret data"))
	if err != nil {
		t.Fatal(err)
	}
	broken := "0123456789abcdef0123456789abcdef"
	if err = os.Mkdir(filepath.Join(dir, broken), 0700); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("entries=%d err=%v", len(list), err)
	}
	foundValid, foundInvalid := false, false
	for _, e := range list {
		if e.ID == id {
			foundValid = !e.Invalid && e.Stage == Prepared
		}
		if e.ID == broken {
			foundInvalid = e.Invalid
		}
	}
	if !foundValid || !foundInvalid {
		t.Fatal("entries missing or misclassified")
	}
	dest := filepath.Join(t.TempDir(), "recovered")
	if err = s.Export(id, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(data, []byte("secret data")) {
		t.Fatalf("export failed: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("export permissions")
	}
	if err = s.Export(id, dest); !os.IsExist(err) {
		t.Fatalf("existing destination overwritten: %v", err)
	}
	if _, _, err = s.Load(id); err != nil {
		t.Fatal("journal not retained")
	}
	if err = s.Export(broken, filepath.Join(t.TempDir(), "bad")); err == nil {
		t.Fatal("incomplete entry exported")
	}
}

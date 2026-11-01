package recovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateIntentRecordsUnknownAndConfirmed(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.BeginCreate("platform", "workspace", "/Workspace/new")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.ListCreates()
	if err != nil || len(entries) != 1 || entries[0].ID != id || entries[0].RemotePath != "/Workspace/new" || entries[0].Confirmed || entries[0].Invalid {
		t.Fatalf("unknown intent: %+v %v", entries, err)
	}
	if err = s.ConfirmCreate(id); err != nil {
		t.Fatal(err)
	}
	entries, err = s.ListCreates()
	if err != nil || !entries[0].Confirmed || entries[0].Invalid {
		t.Fatalf("confirmed intent: %+v %v", entries, err)
	}
	if err = os.WriteFile(filepath.Join(dir, "create-"+id+".done"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err = s.ListCreates()
	if err != nil || !entries[0].Invalid {
		t.Fatalf("bad marker: %+v %v", entries, err)
	}
	if _, err = s.BeginCreate("platform", "workspace", "/Workspace/../escape"); err == nil {
		t.Fatal("invalid path accepted")
	}
	dirID, err := s.BeginCreateKind("platform", "workspace", "/Workspace/dir", "FOLDER")
	if err != nil {
		t.Fatal(err)
	}
	entries, err = s.ListCreates()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.ID == dirID && entry.Kind == "FOLDER" && !entry.Invalid {
			found = true
		}
	}
	if !found {
		t.Fatal("directory intent missing")
	}
}

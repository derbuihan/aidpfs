package recovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMutationIntentDurableAndInvalid(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.BeginMutation("p", "w", "rename", "/Workspace/a", "/Workspace/b")
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.ListMutations()
	if err != nil || len(items) != 1 || items[0].Confirmed || items[0].Invalid || items[0].Destination != "/Workspace/b" {
		t.Fatalf("pending: %+v %v", items, err)
	}
	if err := s.ConfirmMutation(id); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListMutations()
	if err != nil || !items[0].Confirmed {
		t.Fatalf("confirmed: %+v %v", items, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mutation-"+id+".done"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListMutations()
	if err != nil || !items[0].Invalid {
		t.Fatalf("invalid: %+v %v", items, err)
	}
	if _, err = s.BeginMutation("p", "w", "unlink", "/Workspace/a", "/Workspace/b"); err == nil {
		t.Fatal("accepted unlink destination")
	}
	if _, err = s.BeginMutation("p", "w", "rename", "/Workspace/a", "/Workspace/a"); err == nil {
		t.Fatal("accepted identical paths")
	}
}

func TestRejectMutationMarksResolved(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.BeginMutation("p", "w", "unlink", "/Workspace/a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RejectMutation(id); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmMutation(id); err == nil {
		t.Fatal("resolved intent was resolved twice")
	}
	items, err := s.ListMutations()
	if err != nil || len(items) != 1 || !items[0].Rejected || items[0].Confirmed || items[0].Invalid {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}

package main

import (
	"aidpfs/internal/recovery"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryCommands(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := recovery.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Create("platform", "workspace", "/Workspace/f", "tag", []byte("hidden content"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = runRecovery([]string{"list", "--dir", d}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), id) || strings.Contains(out.String(), "hidden content") {
		t.Fatal("list output incorrect")
	}
	intentID, err := s.BeginCreate("platform", "workspace", "/Workspace/new")
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = runRecovery([]string{"list", "--dir", d}, &out); err != nil || !strings.Contains(out.String(), intentID) || !strings.Contains(out.String(), `"confirmed":false`) {
		t.Fatalf("create intent missing: %v", err)
	}
	out.Reset()
	if err = runRecovery([]string{"lineage", "--dir", d, "--id", id}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), id) || strings.Contains(out.String(), "hidden content") {
		t.Fatal("lineage output incorrect")
	}
	dest := filepath.Join(t.TempDir(), "recovered")
	out.Reset()
	if err = runRecovery([]string{"export", "--dir", d, "--id", id, "--output", dest}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "hidden content") {
		t.Fatal("content leaked")
	}
	if err = runRecovery([]string{"export", "--dir", d, "--id", id, "--output", dest}, &out); !os.IsExist(err) {
		t.Fatalf("overwrite not rejected: %v", err)
	}
}

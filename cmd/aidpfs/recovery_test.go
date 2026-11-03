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
	if err = runRecovery([]string{"inspect-create", "--dir", d, "--id", "missing", "--region", "ap-osaka-1"}, &out); err == nil {
		t.Fatal("unknown create intent accepted")
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

func TestRecoveryCommandRejectsInvalidArguments(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
	}{
		{name: "missing command"},
		{name: "unknown flag", args: []string{"list", "--dir", d, "--unknown"}},
		{name: "positional argument", args: []string{"list", "--dir", d, "extra"}},
		{name: "list rejects inspect flags", args: []string{"list", "--dir", d, "--id", "entry"}},
		{name: "lineage requires id", args: []string{"lineage", "--dir", d}},
		{name: "export requires output", args: []string{"export", "--dir", d, "--id", "entry"}},
		{name: "inspect requires region", args: []string{"inspect", "--dir", d, "--id", "entry"}},
		{name: "inspect-create requires region", args: []string{"inspect-create", "--dir", d, "--id", "entry"}},
		{name: "unknown command", args: []string{"repair", "--dir", d}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := runRecovery(tc.args, &out); err == nil {
				t.Fatalf("runRecovery(%v) succeeded", tc.args)
			}
		})
	}
}

func TestRecoveryInspectRejectsRootLevelTargets(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := recovery.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	fileID, err := store.Create("platform", "workspace", "/file", "etag", []byte("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	createID, err := store.BeginCreate("platform", "workspace", "/new")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "inspect", args: []string{"inspect", "--dir", d, "--id", fileID, "--region", "ap-osaka-1"}},
		{name: "inspect-create", args: []string{"inspect-create", "--dir", d, "--id", createID, "--region", "ap-osaka-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runRecovery(tc.args, &out)
			if err == nil || !strings.Contains(err.Error(), "root-level target is not supported") {
				t.Fatalf("runRecovery error = %v, want root-level target rejection", err)
			}
		})
	}
}

func TestRecoveryAfterUnmountReportsPendingWork(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := recovery.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkRecoveryAfterUnmount(0, s); err != nil {
		t.Fatal(err)
	}
	if err = checkRecoveryAfterUnmount(1, s); err == nil {
		t.Fatal("dirty buffer reported clean")
	}
	id, err := s.BeginCreate("platform", "workspace", "/Workspace/new")
	if err != nil {
		t.Fatal(err)
	}
	if err = checkRecoveryAfterUnmount(0, s); err == nil {
		t.Fatal("unconfirmed create reported clean")
	}
	if err = s.ConfirmCreate(id); err != nil {
		t.Fatal(err)
	}
	if err = checkRecoveryAfterUnmount(0, s); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryAfterUnmountReportsIncompleteSnapshot(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := recovery.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(d, "0123456789abcdef0123456789abcdef"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = checkRecoveryAfterUnmount(0, s); err == nil || !strings.Contains(err.Error(), "1 unresolved snapshots") {
		t.Fatalf("unreported partial journal: %v", err)
	}
}

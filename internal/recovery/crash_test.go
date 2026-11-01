package recovery

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRecoveryChildProcess(t *testing.T) {
	if os.Getenv("AIDPFS_CRASH_TEST") != "1" {
		t.Skip("subprocess helper")
	}
	s, err := Open(os.Getenv("AIDPFS_CRASH_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Create("platform", "workspace", "/Workspace/file", "base", []byte("retained"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Advance(id, PutStarted, ""); err != nil {
		t.Fatal(err)
	}
	fmt.Println(id)
	time.Sleep(time.Hour)
}
func TestRecoverySurvivesProcessKill(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryChildProcess$")
	cmd.Env = append(os.Environ(), "AIDPFS_CRASH_TEST=1", "AIDPFS_CRASH_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			line <- scanner.Text()
		} else {
			line <- ""
		}
	}()
	var id string
	select {
	case id = <-line:
	case <-time.After(10 * time.Second):
		t.Fatal("child did not prepare journal")
	}
	if !validID.MatchString(id) {
		t.Fatal("child did not return recovery id")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, data, err := s.Load(id)
	if err != nil || r.Stage != PutStarted || !bytes.Equal(data, []byte("retained")) {
		t.Fatalf("after kill stage=%v err=%v", r.Stage, err)
	}
}

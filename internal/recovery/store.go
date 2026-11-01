// Package recovery stores uncommitted file contents locally. It does not send
// network requests or automatically replay operations after a crash.
package recovery

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type Stage string

const (
	Prepared      Stage = "prepared"
	PutStarted    Stage = "put_started"
	PutConfirmed  Stage = "put_confirmed"
	UpdateStarted Stage = "update_started"
	Verified      Stage = "verified"
)

var transitions = map[Stage]Stage{Prepared: PutStarted, PutStarted: PutConfirmed, PutConfirmed: UpdateStarted, UpdateStarted: Verified}

const maxPayload = 64 << 20

var validID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Record struct {
	Version      int    `json:"version"`
	PlatformID   string `json:"platform_id"`
	WorkspaceKey string `json:"workspace_key"`
	RemotePath   string `json:"remote_path"`
	BaseETag     string `json:"base_etag"`
	PutETag      string `json:"put_etag,omitempty"`
	PreviousID   string `json:"previous_id,omitempty"`
	Generation   uint64 `json:"generation"`
	Stage        Stage  `json:"stage"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
}

type Store struct{ dir string }

// Open requires an existing private directory owned by the current user. It
// rejects symlinks and broad permissions rather than silently changing them.
func Open(dir string) (*Store, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, errors.New("recovery directory must be an absolute clean path")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || st.Uid != uint32(os.Getuid()) {
		return nil, errors.New("recovery directory must be private and owned by current user")
	}
	return &Store{dir: dir}, nil
}

// Create makes a new entry and durably writes content and metadata before
// returning. On a partial failure the returned nonempty id is a recovery handle;
// callers must not delete or automatically retry that entry.
func (s *Store) Create(platformID, workspaceKey, remote, baseETag string, data []byte) (string, error) {
	return s.CreateAfter(platformID, workspaceKey, remote, baseETag, "", data)
}

// CreateAfter links snapshots of successive acknowledged edits. The previous
// entry must belong to the same target and base version and remain prepared.
func (s *Store) CreateAfter(platformID, workspaceKey, remote, baseETag, previousID string, data []byte) (id string, err error) {
	var generation uint64
	if previousID != "" {
		previous, _, e := s.Load(previousID)
		if e != nil {
			return "", e
		}
		if previous.PlatformID != platformID || previous.WorkspaceKey != workspaceKey || previous.RemotePath != remote || previous.BaseETag != baseETag || previous.Stage != Prepared || previous.Generation == ^uint64(0) {
			return "", errors.New("invalid previous recovery snapshot")
		}
		generation = previous.Generation + 1
	}
	if len(data) > maxPayload {
		return "", syscall.EFBIG
	}
	if !validIdentity(platformID) || !validIdentity(workspaceKey) {
		return "", errors.New("invalid recovery target")
	}
	if !strings.HasPrefix(remote, "/") || path.Clean(remote) != remote || remote == "/" || strings.ContainsRune(remote, '\x00') {
		return "", errors.New("invalid remote path")
	}
	if !validETag(baseETag) {
		return "", errors.New("invalid base ETag")
	}
	var key [16]byte
	if _, err = rand.Read(key[:]); err != nil {
		return "", err
	}
	id = hex.EncodeToString(key[:])
	dir := filepath.Join(s.dir, id)
	if err = os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	// Persist the directory entry before placing recoverable data inside it.
	if err = syncDir(s.dir); err != nil {
		return id, err
	}
	if err = writeNew(filepath.Join(dir, "content"), data); err != nil {
		return id, err
	}
	digest := sha256.Sum256(data)
	record := Record{Version: 1, PlatformID: platformID, WorkspaceKey: workspaceKey, RemotePath: remote, BaseETag: baseETag, PreviousID: previousID, Generation: generation, Stage: Prepared, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
	if err = writeRecord(dir, record); err != nil {
		return id, err
	}
	return id, nil
}

// Load verifies the metadata and content hash; an incomplete or corrupt entry
// remains on disk so the operator can inspect it without losing recovery data.
func (s *Store) Load(id string) (Record, []byte, error) {
	dir, err := s.entry(id)
	if err != nil {
		return Record{}, nil, err
	}
	meta, err := readFile(filepath.Join(dir, "record.json"), 64<<10)
	if err != nil {
		return Record{}, nil, err
	}
	var r Record
	if json.Unmarshal(meta, &r) != nil || r.Version != 1 || r.Size < 0 || r.Size > maxPayload || (r.PreviousID != "" && !validID.MatchString(r.PreviousID)) || (r.PreviousID == "" && r.Generation != 0) || (r.PreviousID != "" && r.Generation == 0) || !validStage(r.Stage) || !validIdentity(r.PlatformID) || !validIdentity(r.WorkspaceKey) || !validETag(r.BaseETag) || ((r.Stage == PutConfirmed || r.Stage == UpdateStarted || r.Stage == Verified) && !validETag(r.PutETag)) {
		return Record{}, nil, errors.New("invalid recovery metadata")
	}
	data, err := readFile(filepath.Join(dir, "content"), r.Size)
	if err != nil {
		return Record{}, nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != r.Size || hex.EncodeToString(sum[:]) != r.SHA256 {
		return Record{}, nil, errors.New("recovery content failed integrity check")
	}
	return r, data, nil
}

// Advance records a strictly forward stage; the caller must persist the
// *_started stage before issuing its corresponding remote request.
func (s *Store) Advance(id string, next Stage, putETag string) error {
	if !validStage(next) || (next == PutConfirmed && !validETag(putETag)) || (next != PutConfirmed && putETag != "") {
		return errors.New("invalid confirmed ETag")
	}
	dir, err := s.entry(id)
	if err != nil {
		return err
	}
	fd, err := syscall.Open(filepath.Join(dir, "lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), "recovery lock")
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || st.Uid != uint32(os.Getuid()) {
		return errors.New("invalid recovery lock permissions")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	r, _, err := s.Load(id)
	if err != nil {
		return err
	}
	if transitions[r.Stage] != next {
		return errors.New("invalid recovery stage transition")
	}
	r.Stage = next
	if next == PutConfirmed {
		r.PutETag = putETag
	}
	return writeRecord(dir, r)
}
func validIdentity(s string) bool {
	return s != "" && len(s) <= 1024 && !strings.ContainsAny(s, "/\r\n\x00") && !strings.Contains(s, "://")
}
func validETag(s string) bool {
	return s != "" && s != "*" && len(s) <= 1024 && !strings.ContainsAny(s, "\r\n\x00") && !strings.Contains(s, "://") && !strings.Contains(s, "/p/")
}
func validStage(stage Stage) bool {
	return stage == Prepared || stage == PutStarted || stage == PutConfirmed || stage == UpdateStarted || stage == Verified
}
func (s *Store) entry(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", errors.New("invalid recovery id")
	}
	dir := filepath.Join(s.dir, id)
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || st.Uid != uint32(os.Getuid()) {
		return "", errors.New("invalid recovery entry permissions")
	}
	return dir, nil
}
func writeNew(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if n, writeErr := f.Write(data); writeErr != nil {
		return writeErr
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return f.Close()
}
func writeRecord(dir string, r Record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".record-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if n, writeErr := f.Write(data); writeErr != nil {
		f.Close()
		return writeErr
	} else if n != len(data) {
		f.Close()
		return io.ErrShortWrite
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(dir, "record.json")); err != nil {
		return err
	}
	return syncDir(dir)
}
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func readFile(name string, max int64) ([]byte, error) {
	// O_NOFOLLOW prevents a replaced file from redirecting recovery reads.
	fd, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || st.Uid != uint32(os.Getuid()) {
		return nil, errors.New("invalid recovery file permissions")
	}
	if info.Size() > max {
		return nil, fmt.Errorf("recovery file exceeds expected size")
	}
	return io.ReadAll(io.LimitReader(f, max+1))
}

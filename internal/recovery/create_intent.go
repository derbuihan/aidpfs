package recovery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// CreateIntent records an intended non-overwriting remote file creation.
// Confirmed means the server returned success; it does not prove the file still exists.
type CreateIntent struct {
	ID           string `json:"id"`
	PlatformID   string `json:"platform_id"`
	WorkspaceKey string `json:"workspace_key"`
	RemotePath   string `json:"remote_path"`
	Kind         string `json:"kind,omitempty"`
	Confirmed    bool   `json:"confirmed"`
	Invalid      bool   `json:"invalid"`
}

// BeginCreate durably records the target before any remote create attempt.
func (s *Store) BeginCreate(platform, workspace, remote string) (string, error) {
	return s.BeginCreateKind(platform, workspace, remote, "FILE")
}

// BeginCreateKind records a FILE or FOLDER creation attempt.
func (s *Store) BeginCreateKind(platform, workspace, remote, kind string) (string, error) {
	if kind != "FILE" && kind != "FOLDER" {
		return "", errors.New("invalid create kind")
	}
	if !validIdentity(platform) || !validIdentity(workspace) || !strings.HasPrefix(remote, "/") || path.Clean(remote) != remote || remote == "/" || strings.ContainsRune(remote, '\x00') {
		return "", errors.New("invalid create target")
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(key[:])
	data, err := json.Marshal(CreateIntent{ID: id, PlatformID: platform, WorkspaceKey: workspace, RemotePath: remote, Kind: kind})
	if err != nil {
		return "", err
	}
	if err = writeNew(filepath.Join(s.dir, "create-"+id+".json"), data); err != nil {
		return id, err
	}
	if err = syncDir(s.dir); err != nil {
		return id, err
	}
	return id, nil
}

// ConfirmCreate records only the receipt of a successful create response.
func (s *Store) ConfirmCreate(id string) error {
	if !validID.MatchString(id) {
		return errors.New("invalid create intent id")
	}
	if _, err := readFile(filepath.Join(s.dir, "create-"+id+".json"), 64<<10); err != nil {
		return err
	}
	if err := writeNew(filepath.Join(s.dir, "create-"+id+".done"), []byte("confirmed\n")); err != nil {
		return err
	}
	return syncDir(s.dir)
}

// ListCreates includes invalid or incomplete records rather than hiding them.
func (s *Store) ListCreates() ([]CreateIntent, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []CreateIntent
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "create-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "create-"), ".json")
		if !validID.MatchString(id) {
			continue
		}
		item := CreateIntent{ID: id, Invalid: true}
		raw, e := readFile(filepath.Join(s.dir, name), 64<<10)
		if e == nil {
			var r CreateIntent
			if json.Unmarshal(raw, &r) == nil && r.ID == id && !r.Confirmed && !r.Invalid && (r.Kind == "" || r.Kind == "FILE" || r.Kind == "FOLDER") && validIdentity(r.PlatformID) && validIdentity(r.WorkspaceKey) && strings.HasPrefix(r.RemotePath, "/") && path.Clean(r.RemotePath) == r.RemotePath && r.RemotePath != "/" && !strings.ContainsRune(r.RemotePath, '\x00') {
				item = r
				marker, markErr := readFile(filepath.Join(s.dir, "create-"+id+".done"), 32)
				if markErr == nil {
					if string(marker) == "confirmed\n" {
						item.Confirmed = true
					} else {
						item.Invalid = true
					}
				} else if !os.IsNotExist(markErr) {
					item.Invalid = true
				}
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

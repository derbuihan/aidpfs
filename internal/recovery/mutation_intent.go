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

// MutationIntent records a single potentially destructive remote request.
// Confirmed records a successful response, not enduring remote state.
// Rejected records a definite rejection by the server or a failure before any
// request was sent; either way the operation was not applied.
type MutationIntent struct {
	ID           string `json:"id"`
	PlatformID   string `json:"platform_id"`
	WorkspaceKey string `json:"workspace_key"`
	Operation    string `json:"operation"`
	Source       string `json:"source"`
	Destination  string `json:"destination,omitempty"`
	Confirmed    bool   `json:"confirmed"`
	Rejected     bool   `json:"rejected"`
	Invalid      bool   `json:"invalid"`
}

func validMutationPath(p string) bool {
	return strings.HasPrefix(p, "/") && p != "/" && path.Clean(p) == p && !strings.ContainsRune(p, '\x00')
}

func validMutation(m MutationIntent) bool {
	if !validID.MatchString(m.ID) || !validIdentity(m.PlatformID) || !validIdentity(m.WorkspaceKey) || !validMutationPath(m.Source) || m.Confirmed || m.Rejected || m.Invalid {
		return false
	}
	switch m.Operation {
	case "unlink":
		return m.Destination == ""
	case "rename":
		return validMutationPath(m.Destination) && m.Source != m.Destination
	default:
		return false
	}
}

func (s *Store) BeginMutation(platform, workspace, operation, source, destination string) (string, error) {
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(key[:])
	m := MutationIntent{ID: id, PlatformID: platform, WorkspaceKey: workspace, Operation: operation, Source: source, Destination: destination}
	if !validMutation(m) {
		return "", errors.New("invalid mutation intent")
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if err = writeNew(filepath.Join(s.dir, "mutation-"+id+".json"), data); err != nil {
		return id, err
	}
	if err = syncDir(s.dir); err != nil {
		return id, err
	}
	return id, nil
}

func (s *Store) ConfirmMutation(id string) error { return s.resolveMutation(id, "confirmed\n") }

// RejectMutation marks an intent whose request was definitely not applied.
func (s *Store) RejectMutation(id string) error { return s.resolveMutation(id, "rejected\n") }

func (s *Store) resolveMutation(id, marker string) error {
	if !validID.MatchString(id) {
		return errors.New("invalid mutation intent id")
	}
	data, err := readFile(filepath.Join(s.dir, "mutation-"+id+".json"), 64<<10)
	if err != nil {
		return err
	}
	var m MutationIntent
	if json.Unmarshal(data, &m) != nil || m.ID != id || !validMutation(m) {
		return errors.New("invalid mutation intent")
	}
	if err := writeNew(filepath.Join(s.dir, "mutation-"+id+".done"), []byte(marker)); err != nil {
		return err
	}
	return syncDir(s.dir)
}

func (s *Store) ListMutations() ([]MutationIntent, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []MutationIntent
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "mutation-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "mutation-"), ".json")
		if !validID.MatchString(id) {
			continue
		}
		item := MutationIntent{ID: id, Invalid: true}
		data, e := readFile(filepath.Join(s.dir, name), 64<<10)
		var m MutationIntent
		if e == nil && json.Unmarshal(data, &m) == nil && m.ID == id && validMutation(m) {
			item = m
			marker, markErr := readFile(filepath.Join(s.dir, "mutation-"+id+".done"), 32)
			if markErr == nil {
				switch string(marker) {
				case "confirmed\n":
					item.Confirmed = true
				case "rejected\n":
					item.Rejected = true
				default:
					item.Invalid = true
				}
			} else if !os.IsNotExist(markErr) {
				item.Invalid = true
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

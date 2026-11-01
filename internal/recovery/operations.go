package recovery

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

type Entry struct {
	ID         string
	Stage      Stage
	Size       int64
	PreviousID string
	Generation uint64
	Invalid    bool
}

// Lineage validates the ancestry of an entry and returns it oldest first.
// It does not determine whether the entry is the newest snapshot: other
// processes or incomplete entries may have produced branches.
func (s *Store) Lineage(id string) ([]Entry, error) {
	if !validID.MatchString(id) {
		return nil, errors.New("invalid recovery id")
	}
	seen := make(map[string]bool)
	var chain []Entry
	var child *Record
	for id != "" {
		if seen[id] || len(chain) >= 10000 {
			return nil, errors.New("recovery lineage cycle or length limit")
		}
		seen[id] = true
		r, _, err := s.Load(id)
		if err != nil {
			return nil, fmt.Errorf("cannot load lineage entry %s: %w", id, err)
		}
		if child != nil && (child.PlatformID != r.PlatformID || child.WorkspaceKey != r.WorkspaceKey || child.RemotePath != r.RemotePath || child.BaseETag != r.BaseETag || child.Generation != r.Generation+1 || r.Generation == ^uint64(0)) {
			return nil, errors.New("inconsistent recovery lineage")
		}
		chain = append(chain, Entry{ID: id, Stage: r.Stage, Size: r.Size, PreviousID: r.PreviousID, Generation: r.Generation})
		child = &r
		id = r.PreviousID
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// List includes incomplete and corrupt entries so they cannot silently vanish.
// It never returns journal contents or credentials.
func (s *Store) List() ([]Entry, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if !validID.MatchString(e.Name()) {
			continue
		}
		record, _, err := s.Load(e.Name())
		item := Entry{ID: e.Name(), Invalid: err != nil}
		if err == nil {
			item.Stage = record.Stage
			item.Size = record.Size
			item.PreviousID = record.PreviousID
			item.Generation = record.Generation
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Export copies verified recovery contents to a new owner-only file. It never
// overwrites a destination, deletes the journal, or contacts the remote API.
func (s *Store) Export(id, dest string) error {
	if !filepath.IsAbs(dest) || filepath.Clean(dest) != dest {
		return errors.New("export path must be an absolute clean path")
	}
	_, data, err := s.Load(id)
	if err != nil {
		return fmt.Errorf("cannot load recovery entry: %w", err)
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("export failed; destination may contain incomplete data and recovery entry is retained")
	}
	if err = syncDir(filepath.Dir(dest)); err != nil {
		return errors.New("export directory sync failed; recovery entry is retained")
	}
	return nil
}

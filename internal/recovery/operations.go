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
	ID      string
	Stage   Stage
	Size    int64
	Invalid bool
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

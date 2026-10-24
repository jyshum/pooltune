package store

import (
	"errors"
	"os"
	"path/filepath"
)

// Backup creates a consistent SQLite snapshot without replacing an existing path.
func (s *Store) Backup(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("store: backup path must be absolute")
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return errors.New("store: backup directory must not be group/world writable")
	}
	if _, err := os.Lstat(path); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".pooltune-backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	temp := filepath.Join(staging, "snapshot.db")
	s.mu.Lock()
	_, err = s.db.Exec("VACUUM INTO ?", temp)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.Chmod(temp, 0600); err != nil {
		return err
	}
	return os.Link(temp, path)
}

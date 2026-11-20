package auth

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// File is a Storage in the file it names. Save writes the database to a new
// file with mode 0600 in the same directory, syncs it, renames it over the
// old one and syncs the directory, so that after a power loss the file holds
// either the old or the new database.
type File string

// Load implements Storage.
func (f File) Load() ([]byte, error) {
	data, err := os.ReadFile(string(f))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// Save implements Storage.
func (f File) Save(data []byte) error {
	dir := filepath.Dir(string(f))
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(string(f))+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), string(f)); err != nil {
		return err
	}
	// The new database is in place now, so this cannot fail the Save; it
	// only makes the rename last through a power loss.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

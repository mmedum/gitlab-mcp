package fileperm

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile replaces path with data as one step and restricts it to the
// current account.
//
// The data goes to a temporary file in the same directory first and is
// renamed over path, so a reader in another process sees the old file
// or the new one, never half of either. The temporary name is unique,
// because two processes can save at once. The restriction comes after
// the rename: on Windows the access list belongs to the file at its
// final path, and a temporary file's list does not survive the move.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("fileperm: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("fileperm: temporary file in %s: %w", dir, err)
	}
	name := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fileperm: write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fileperm: close %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("fileperm: replace %s: %w", path, err)
	}
	done = true
	return RestrictToOwner(path)
}

package filesystem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecureMkdirAll creates an absolute directory path without following any
// pre-existing symlink component. Existing components must be directories.
func SecureMkdirAll(path string, mode os.FileMode) error {
	if !filepath.IsAbs(path) {
		return errors.New("directory path must be absolute")
	}
	path = filepath.Clean(path)
	parts := filepath.VolumeName(path)
	cur := parts + string(filepath.Separator)
	rel := path
	if parts != "" {
		rel = path[len(parts):]
	}
	for _, part := range splitPath(rel) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(cur, mode); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			fi, err = os.Lstat(cur)
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, cur)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", cur)
		}
	}
	if err := os.Chmod(path, mode.Perm()); err != nil {
		return err
	}
	return nil
}

func splitPath(path string) []string {
	return strings.Split(strings.TrimLeft(path, string(filepath.Separator)), string(filepath.Separator))
}

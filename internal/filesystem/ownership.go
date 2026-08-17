package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrOutsideRoot = errors.New("path is outside owned root")
	ErrSymlink     = errors.New("symlink is not an owned path")
)

func contained(root, path string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	r, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

func verifyOwnedPath(root, path string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return ErrOutsideRoot
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !contained(root, path) {
		return ErrOutsideRoot
	}
	if err := rejectSymlinkComponents(root, path); err != nil {
		return err
	}
	return nil
}

func rejectSymlinkComponents(root, path string) error {
	if _, err := os.Lstat(root); err != nil {
		return err
	}
	cur := root
	if fi, err := os.Lstat(cur); err != nil {
		return err
	} else if fi.Mode()&os.ModeSymlink != 0 {
		return ErrSymlink
	}
	rel, _ := filepath.Rel(root, path)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
	}
	return nil
}

// SafeRemove removes a file or directory only when it is contained by root.
// A symlink selected as path (or used by a root/component) is rejected. Nested
// symlinks are safe: os.RemoveAll unlinks them without following their target.
func SafeRemove(root, path string) error {
	if err := verifyOwnedPath(root, path); err != nil {
		return err
	}
	if filepath.Clean(root) == filepath.Clean(path) {
		return ErrOutsideRoot
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrSymlink
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return nil
}

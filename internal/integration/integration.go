// Package integration owns TarLink's narrow PATH and desktop integration.
package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/drobilica/tarlink/internal/filesystem"
)

var ErrConflict = errors.New("integration path is occupied by a non-TarLink file")

const maxDesktopBytes = 64 << 10

type Paths struct {
	ExecutableLink string
	DesktopEntry   string
}

type Spec struct {
	ID                string
	Name              string
	Executable        string
	ApplicationRoot   string
	LocalBinDirectory string
	DesktopDirectory  string
	DesktopEnabled    bool
	DesktopCategories []string
	DesktopSHA256     string
}

func ExpectedPaths(spec Spec) Paths {
	return Paths{
		ExecutableLink: filepath.Join(spec.LocalBinDirectory, spec.ID),
		DesktopEntry:   filepath.Join(spec.DesktopDirectory, "tarlink-"+spec.ID+".desktop"),
	}
}

// Ensure creates only stable, TarLink-owned integrations. It never overwrites
// an unrelated file. The returned cleanup removes only integrations created by
// this call and is intended for pre-activation rollback.
func Ensure(spec Spec) (Paths, func() error, error) {
	paths := ExpectedPaths(spec)
	target := filepath.Join(spec.ApplicationRoot, "current", filepath.FromSlash(spec.Executable))
	createdLink, err := ensureSymlink(paths.ExecutableLink, target)
	if err != nil {
		return Paths{}, nil, err
	}
	createdDesktop := false
	if spec.DesktopEnabled {
		content := DesktopFile(spec, paths.ExecutableLink)
		createdDesktop, err = ensureDesktop(paths.DesktopEntry, spec.ID, content)
		if err != nil {
			if createdLink {
				_ = os.Remove(paths.ExecutableLink)
			}
			return Paths{}, nil, err
		}
	}
	cleanup := func() error {
		var errs []error
		if createdDesktop {
			errs = appendIf(errs, os.Remove(paths.DesktopEntry))
		}
		if createdLink {
			errs = appendIf(errs, os.Remove(paths.ExecutableLink))
		}
		return errors.Join(errs...)
	}
	return paths, cleanup, nil
}

func DesktopFile(spec Spec, executableLink string) []byte {
	categories := strings.Join(spec.DesktopCategories, ";")
	if categories != "" {
		categories += ";"
	}
	return []byte(strings.Join([]string{
		"[Desktop Entry]",
		"Type=Application",
		"Name=" + desktopText(spec.Name),
		"Exec=" + desktopExec(executableLink),
		"TryExec=" + executableLink,
		"Icon=application-x-executable",
		"Terminal=false",
		"Categories=" + categories,
		"X-TarLink-AppID=" + spec.ID,
		"",
	}, "\n"))
}

func DesktopDigest(spec Spec, executableLink string) string {
	digest := sha256.Sum256(DesktopFile(spec, executableLink))
	return hex.EncodeToString(digest[:])
}

func ValidateOwned(spec Spec) error {
	paths := ExpectedPaths(spec)
	target := filepath.Join(spec.ApplicationRoot, "current", filepath.FromSlash(spec.Executable))
	if err := validateSymlink(paths.ExecutableLink, target); err != nil {
		return err
	}
	if spec.DesktopEnabled {
		if err := validateDesktop(paths.DesktopEntry, spec.ID, spec.DesktopSHA256); err != nil {
			return err
		}
	}
	return nil
}

func RemoveOwned(spec Spec) error {
	if err := ValidateOwned(spec); err != nil {
		return err
	}
	paths := ExpectedPaths(spec)
	var errs []error
	if spec.DesktopEnabled {
		errs = appendIf(errs, os.Remove(paths.DesktopEntry))
	}
	errs = appendIf(errs, os.Remove(paths.ExecutableLink))
	return errors.Join(errs...)
}

func ensureSymlink(link, target string) (bool, error) {
	if err := filesystem.SecureMkdirAll(filepath.Dir(link), 0o755); err != nil {
		return false, fmt.Errorf("create executable integration directory: %w", err)
	}
	info, err := os.Lstat(link)
	if err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return false, fmt.Errorf("%w: %s", ErrConflict, link)
		}
		existing, readErr := os.Readlink(link)
		if readErr != nil || existing != target {
			return false, fmt.Errorf("%w: %s", ErrConflict, link)
		}
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect executable integration: %w", err)
	}
	// Creating the stable link in place is atomic and, unlike rename, cannot
	// replace a file raced into this path after the Lstat above.
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, fmt.Errorf("%w: %s", ErrConflict, link)
		}
		return false, err
	}
	return true, nil
}

func ensureDesktop(path, id string, content []byte) (bool, error) {
	if err := filesystem.SecureMkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create desktop integration directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("%w: %s", ErrConflict, path)
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return false, readErr
		}
		if string(existing) != string(content) {
			return false, fmt.Errorf("%w: %s", ErrConflict, path)
		}
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, err
	}
	if err := atomicCreate(path, content, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func validateSymlink(link, target string) error {
	info, err := os.Lstat(link)
	if err != nil {
		return fmt.Errorf("inspect executable integration: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%w: %s", ErrConflict, link)
	}
	existing, err := os.Readlink(link)
	if err != nil || existing != target {
		return fmt.Errorf("%w: %s", ErrConflict, link)
	}
	return nil
}

func validateDesktop(path, id, expectedSHA256 string) error {
	if len(expectedSHA256) != sha256.Size*2 {
		return fmt.Errorf("%w: desktop ownership digest is missing", ErrConflict)
	}
	if _, err := hex.DecodeString(expectedSHA256); err != nil || strings.ToLower(expectedSHA256) != expectedSHA256 {
		return fmt.Errorf("%w: desktop ownership digest is invalid", ErrConflict)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect desktop integration: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrConflict, path)
	}
	if info.Size() > maxDesktopBytes {
		return fmt.Errorf("%w: %s", ErrConflict, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxDesktopBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(content) > maxDesktopBytes {
		return fmt.Errorf("%w: cannot verify %s", ErrConflict, path)
	}
	marker := "\nX-TarLink-AppID=" + id + "\n"
	if !strings.Contains("\n"+string(content), marker) {
		return fmt.Errorf("%w: %s", ErrConflict, path)
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != expectedSHA256 {
		return fmt.Errorf("%w: %s was modified", ErrConflict, path)
	}
	return nil
}

func atomicCreate(path string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tarlink-desktop-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(name)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// Linking a completely written temporary inode into place is atomic and
	// fails instead of overwriting a concurrently created user file.
	if err := os.Link(name, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrConflict, path)
		}
		return err
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return err
	}
	keep = true
	return nil
}

func desktopText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return value
}

func desktopExec(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%")
	return "\"" + replacer.Replace(value) + "\""
}

func appendIf(errs []error, err error) []error {
	if err != nil && !os.IsNotExist(err) {
		return append(errs, err)
	}
	return errs
}

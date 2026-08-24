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

// PathConflict describes a PATH issue that could shadow or hide a managed
// command after TarLink installs its executable link.
type PathConflict struct {
	// Type is "not_in_path" when the TarLink bin directory is absent from
	// PATH, or "shadowed" when an earlier PATH entry contains an executable
	// that shares the managed command name.
	Type string
	// Executable is the managed command name (the application ID) being
	// installed.
	Executable string
	// Directory is the PATH entry responsible for the conflict.
	Directory string
	// Candidate is the resolved path of the conflicting entry.
	Candidate string
}

type ExecutableSpec struct {
	Name          string
	Path          string
	CreateBinLink *bool
}

func (e ExecutableSpec) WantsBinLink() bool { return e.CreateBinLink == nil || *e.CreateBinLink }

type ExecutablePath struct{ Name, Link, Target string }

// CheckPath inspects the supplied PATH value for issues that would hide or
// shadow a managed command after install. It is read-only: it never executes
// a command, modifies PATH, or writes to the filesystem. Only the directory
// entries in PATH are inspected for a name collision with spec.ID.
func CheckPath(spec Spec, pathValue string) []PathConflict {
	var conflicts []PathConflict
	if spec.LocalBinDirectory == "" {
		return conflicts
	}
	executables := spec.Executables
	binDir := filepath.Clean(spec.LocalBinDirectory)
	directories := filepath.SplitList(pathValue)
	binIndex := -1
	for index, directory := range directories {
		if filepath.Clean(directory) == binDir {
			binIndex = index
			break
		}
	}
	if binIndex == -1 {
		for _, executable := range executables {
			conflicts = append(conflicts, PathConflict{Type: "not_in_path", Executable: executable.Name, Directory: binDir, Candidate: binDir})
		}
		return conflicts
	}
	for _, executable := range executables {
		for index := 0; index < binIndex; index++ {
			directory := directories[index]
			if directory == "" {
				continue
			}
			candidate := filepath.Join(directory, executable.Name)
			info, err := os.Lstat(candidate)
			if err != nil {
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 != 0 {
				conflicts = append(conflicts, PathConflict{
					Type: "shadowed", Executable: executable.Name, Directory: directory, Candidate: candidate,
				})
			}
		}
	}
	return conflicts
}

type Paths struct {
	Executables  []ExecutablePath
	DesktopEntry string
	IconFile     string
}

type Spec struct {
	ID                string
	Name              string
	Executables       []ExecutableSpec
	ApplicationRoot   string
	LocalBinDirectory string
	DesktopDirectory  string
	IconDirectory     string
	IconSourceRoot    string
	Icon              string
	// IconSize is the explicit hicolor raster size of a remote PNG icon.
	// When positive it overrides extension-based sizing; archive-contained
	// icons leave it zero so SVG maps to scalable and other rasters to 48x48.
	IconSize          int
	DesktopEnabled    bool
	DesktopCategories []string
	DesktopExecutable string
	WorkingDirectory  bool
	DesktopSHA256     string
	IconSHA256        string
}

func ExpectedPaths(spec Spec) Paths {
	paths := Paths{
		DesktopEntry: filepath.Join(spec.DesktopDirectory, "tarlink-"+spec.ID+".desktop"),
	}
	if spec.Icon != "" {
		paths.IconFile = filepath.Join(spec.IconDirectory, iconSizeDirectory(spec.IconSize, spec.Icon), "apps", "tarlink-"+spec.ID+filepath.Ext(filepath.FromSlash(spec.Icon)))
	}
	paths.Executables = executablePaths(spec)
	return paths
}

func iconSizeDirectory(size int, icon string) string {
	if size > 0 {
		return fmt.Sprintf("%dx%d", size, size)
	}
	if strings.EqualFold(filepath.Ext(icon), ".svg") {
		return "scalable"
	}
	return "48x48"
}

func executablePaths(spec Spec) []ExecutablePath {
	values := spec.Executables
	paths := make([]ExecutablePath, 0, len(values))
	for _, executable := range values {
		link := ""
		if executable.WantsBinLink() {
			link = filepath.Join(spec.LocalBinDirectory, executable.Name)
		}
		paths = append(paths, ExecutablePath{Name: executable.Name, Link: link, Target: filepath.Join(spec.ApplicationRoot, "current", filepath.FromSlash(executable.Path))})
	}
	return paths
}

// Update replaces only integrations already proven to be TarLink-owned. The
// returned cleanup restores the previous files when activation or state
// persistence fails.
func Update(spec, previous Spec) (Paths, func() error, error) {
	if err := ValidateOwned(previous); err != nil {
		return Paths{}, nil, err
	}
	paths := ExpectedPaths(spec)
	previousPaths := ExpectedPaths(previous)
	if len(spec.Executables) > 0 {
		for _, executable := range paths.Executables {
			if executable.Link == "" {
				continue
			}
			created, err := ensureSymlink(executable.Link, executable.Target)
			if err != nil {
				return Paths{}, nil, err
			}
			if created {
				previousPaths.Executables = append(previousPaths.Executables, executable)
			}
		}
	}
	var undo []func() error
	rollback := func() error {
		var errs []error
		for index := len(undo) - 1; index >= 0; index-- {
			errs = appendIf(errs, undo[index]())
		}
		return errors.Join(errs...)
	}
	for _, old := range previousPaths.Executables {
		if old.Link == "" {
			continue
		}
		kept := false
		for _, current := range paths.Executables {
			if current.Link == old.Link {
				kept = true
				break
			}
		}
		if kept {
			continue
		}
		if err := detachOwned(old.Link, func(link string) error { return validateSymlinkForRemoval(link, old.Target) }); err != nil {
			_ = rollback()
			return Paths{}, nil, err
		}
		oldCopy := old
		undo = append(undo, func() error { _, err := ensureSymlink(oldCopy.Link, oldCopy.Target); return err })
	}
	content := DesktopFile(spec, paths.Executables[0].Link)
	if spec.DesktopEnabled {
		if previous.DesktopEnabled {
			old, err := os.ReadFile(previousPaths.DesktopEntry)
			if err != nil {
				return Paths{}, nil, err
			}
			if string(old) != string(content) {
				undoFile, err := replaceOwned(previousPaths.DesktopEntry, previous.DesktopSHA256, old, content, 0o644)
				if err != nil {
					return Paths{}, nil, err
				}
				undo = append(undo, undoFile)
			}
		} else {
			created, err := ensureDesktop(paths.DesktopEntry, spec.ID, content)
			if err != nil {
				return Paths{}, nil, err
			}
			if created {
				undo = append(undo, func() error { return os.Remove(paths.DesktopEntry) })
			}
		}
	} else if previous.DesktopEnabled {
		old, err := os.ReadFile(previousPaths.DesktopEntry)
		if err != nil {
			return Paths{}, nil, err
		}
		if err := validateDesktop(previousPaths.DesktopEntry, previous.ID, previous.DesktopSHA256); err != nil {
			return Paths{}, nil, err
		}
		if err := detachOwned(previousPaths.DesktopEntry, func(path string) error {
			return validateDesktopForRemoval(path, previous.ID, previous.DesktopSHA256)
		}); err != nil {
			return Paths{}, nil, err
		}
		undo = append(undo, func() error { return atomicCreateExisting(previousPaths.DesktopEntry, old, 0o644) })
	}
	if spec.Icon != "" {
		if err := ensureIconSource(spec); err != nil {
			_ = rollback()
			return Paths{}, nil, err
		}
		content, err := readIconSource(spec)
		if err != nil {
			_ = rollback()
			return Paths{}, nil, err
		}
		if previous.Icon == "" {
			created, createErr := atomicCreateIcon(paths.IconFile, content)
			if createErr != nil {
				_ = rollback()
				return Paths{}, nil, createErr
			}
			if created {
				undo = append(undo, func() error { return os.Remove(paths.IconFile) })
			}
		} else if paths.IconFile == previousPaths.IconFile {
			old, readErr := os.ReadFile(previousPaths.IconFile)
			if readErr != nil {
				_ = rollback()
				return Paths{}, nil, readErr
			}
			if string(old) != string(content) {
				undoFile, replaceErr := replaceOwned(previousPaths.IconFile, previous.IconSHA256, old, content, 0o644)
				if replaceErr != nil {
					_ = rollback()
					return Paths{}, nil, replaceErr
				}
				undo = append(undo, undoFile)
			}
		} else {
			old, readErr := os.ReadFile(previousPaths.IconFile)
			if readErr != nil {
				_ = rollback()
				return Paths{}, nil, readErr
			}
			created, createErr := atomicCreateIcon(paths.IconFile, content)
			if createErr != nil {
				_ = rollback()
				return Paths{}, nil, createErr
			}
			if created {
				undo = append(undo, func() error { return os.Remove(paths.IconFile) })
			}
			oldCopy := append([]byte(nil), old...)
			undo = append(undo, func() error {
				if err := os.Remove(previousPaths.IconFile); err != nil && !os.IsNotExist(err) {
					return err
				}
				return atomicCreateExisting(previousPaths.IconFile, oldCopy, 0o644)
			})
			if err := os.Remove(previousPaths.IconFile); err != nil && !os.IsNotExist(err) {
				_ = rollback()
				return Paths{}, nil, err
			}
		}
	} else if previous.Icon != "" {
		old, err := os.ReadFile(previousPaths.IconFile)
		if err != nil {
			_ = rollback()
			return Paths{}, nil, err
		}
		if err := os.Remove(previousPaths.IconFile); err != nil && !os.IsNotExist(err) {
			_ = rollback()
			return Paths{}, nil, err
		}
		undo = append(undo, func() error { return atomicCreateExisting(previousPaths.IconFile, old, 0o644) })
	}
	return paths, rollback, nil
}

// SwitchIcon changes the owned icon to match a retained application version.
// It is called after the application's current pointer has switched.
func SwitchIcon(current, next Spec) (func() error, error) {
	if current.Icon == "" && next.Icon == "" {
		return func() error { return nil }, nil
	}
	if err := ValidateOwned(current); err != nil {
		return nil, err
	}
	currentPaths := ExpectedPaths(current)
	if next.Icon == "" {
		if current.Icon == "" {
			return func() error { return nil }, nil
		}
		old, err := os.ReadFile(currentPaths.IconFile)
		if err != nil {
			return nil, err
		}
		if err := os.Remove(currentPaths.IconFile); err != nil {
			return nil, err
		}
		return func() error { return atomicCreateExisting(currentPaths.IconFile, old, 0o644) }, nil
	}
	if err := ensureIconSource(next); err != nil {
		return nil, err
	}
	content, err := readIconSource(next)
	if err != nil {
		return nil, err
	}
	nextPaths := ExpectedPaths(next)
	if current.Icon == "" {
		created, err := atomicCreateIcon(nextPaths.IconFile, content)
		if err != nil {
			return nil, err
		}
		if !created {
			return nil, fmt.Errorf("%w: %s", ErrConflict, nextPaths.IconFile)
		}
		return func() error { return os.Remove(nextPaths.IconFile) }, nil
	}
	if nextPaths.IconFile == currentPaths.IconFile {
		old, readErr := os.ReadFile(currentPaths.IconFile)
		if readErr != nil {
			return nil, readErr
		}
		if string(old) == string(content) {
			return func() error { return nil }, nil
		}
		return replaceOwned(currentPaths.IconFile, current.IconSHA256, old, content, 0o644)
	}
	created, err := atomicCreateIcon(nextPaths.IconFile, content)
	if err != nil {
		return nil, err
	}
	old, err := os.ReadFile(currentPaths.IconFile)
	if err != nil {
		if created {
			_ = os.Remove(nextPaths.IconFile)
		}
		return nil, err
	}
	if err := os.Remove(currentPaths.IconFile); err != nil {
		if created {
			_ = os.Remove(nextPaths.IconFile)
		}
		return nil, err
	}
	return func() error {
		if created {
			_ = os.Remove(nextPaths.IconFile)
		}
		return atomicCreateExisting(currentPaths.IconFile, old, 0o644)
	}, nil
}

// Ensure creates only stable, TarLink-owned integrations. It never overwrites
// an unrelated file. The returned cleanup removes only integrations created by
// this call and is intended for pre-activation rollback.
func Ensure(spec Spec) (Paths, func() error, error) {
	paths := ExpectedPaths(spec)
	created := make([]string, 0, len(paths.Executables))
	for _, executable := range paths.Executables {
		if executable.Link == "" {
			continue
		}
		createdLink, err := ensureSymlink(executable.Link, executable.Target)
		if err != nil {
			for _, link := range created {
				_ = os.Remove(link)
			}
			return Paths{}, nil, err
		}
		if createdLink {
			created = append(created, executable.Link)
		}
	}
	createdDesktop := false
	var err error
	if spec.DesktopEnabled {
		content := DesktopFile(spec, paths.Executables[0].Link)
		createdDesktop, err = ensureDesktop(paths.DesktopEntry, spec.ID, content)
		if err != nil {
			if createdDesktop {
				_ = os.Remove(paths.DesktopEntry)
			}
			for _, link := range created {
				_ = os.Remove(link)
			}
			return Paths{}, nil, err
		}
	}
	createdIcon := false
	if spec.Icon != "" {
		createdIcon, err = ensureIcon(spec, paths.IconFile)
		if err != nil {
			if createdDesktop {
				_ = os.Remove(paths.DesktopEntry)
			}
			for _, link := range created {
				_ = os.Remove(link)
			}
			return Paths{}, nil, err
		}
	}
	cleanup := func() error {
		var errs []error
		if createdDesktop {
			errs = appendIf(errs, os.Remove(paths.DesktopEntry))
		}
		for _, link := range created {
			errs = appendIf(errs, os.Remove(link))
		}
		if createdIcon {
			errs = appendIf(errs, os.Remove(paths.IconFile))
		}
		return errors.Join(errs...)
	}
	return paths, cleanup, nil
}

func DesktopFile(spec Spec, executablePath string) []byte {
	categories := strings.Join(spec.DesktopCategories, ";")
	if categories != "" {
		categories += ";"
	}
	if spec.DesktopExecutable != "" {
		executablePath = spec.DesktopExecutable
	}
	lines := []string{
		"[Desktop Entry]",
		"Type=Application",
		"Name=" + desktopText(spec.Name),
		"Exec=" + desktopExec(executablePath),
		"TryExec=" + desktopText(executablePath),
	}
	if spec.WorkingDirectory {
		lines = append(lines, "Path="+desktopText(spec.ApplicationRoot+string(filepath.Separator)+"current"))
	}
	lines = append(lines, []string{
		"Icon=" + desktopText(iconName(spec)),
		"Terminal=false",
		"Categories=" + categories,
		"X-TarLink-AppID=" + spec.ID,
		"",
	}...)
	return []byte(strings.Join(lines, "\n"))
}

func DesktopDigest(spec Spec, executableLink string) string {
	digest := sha256.Sum256(DesktopFile(spec, executableLink))
	return hex.EncodeToString(digest[:])
}

func ValidateOwned(spec Spec) error {
	paths := ExpectedPaths(spec)
	for _, executable := range paths.Executables {
		if executable.Link == "" {
			continue
		}
		if err := validateIntegrationParent(executable.Link); err != nil {
			return err
		}
		if err := validateSymlink(executable.Link, executable.Target); err != nil {
			return err
		}
	}
	if spec.DesktopEnabled {
		if err := validateIntegrationParent(paths.DesktopEntry); err != nil {
			return err
		}
		if err := validateDesktop(paths.DesktopEntry, spec.ID, spec.DesktopSHA256); err != nil {
			return err
		}
	}
	if spec.Icon != "" {
		if err := validateIntegrationParent(paths.IconFile); err != nil {
			return err
		}
		if err := validateIcon(paths.IconFile, spec.IconSHA256); err != nil {
			return err
		}
	}
	return nil
}

func ValidateOwnedForRemoval(spec Spec) error {
	paths := ExpectedPaths(spec)
	for _, executable := range paths.Executables {
		if executable.Link == "" {
			continue
		}
		if err := validateIntegrationParent(executable.Link); err != nil {
			return err
		}
		if err := validateSymlinkForRemoval(executable.Link, executable.Target); err != nil {
			return err
		}
	}
	if spec.DesktopEnabled {
		if err := validateIntegrationParent(paths.DesktopEntry); err != nil {
			return err
		}
		if err := validateDesktopForRemoval(paths.DesktopEntry, spec.ID, spec.DesktopSHA256); err != nil {
			return err
		}
	}
	if spec.Icon != "" {
		if err := validateIntegrationParent(paths.IconFile); err != nil {
			return err
		}
		if err := validateIconForRemoval(paths.IconFile, spec.IconSHA256); err != nil {
			return err
		}
	}
	return nil
}

func validateIntegrationParent(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect integration: %w", err)
	}
	return filesystem.CheckOwnedDirectory(filepath.Dir(path))
}

func RemoveOwned(spec Spec) error {
	if err := ValidateOwnedForRemoval(spec); err != nil {
		return err
	}
	paths := ExpectedPaths(spec)
	var errs []error
	for _, executable := range paths.Executables {
		if executable.Link == "" {
			continue
		}
		errs = appendIf(errs, detachOwned(executable.Link, func(link string) error { return validateSymlinkForRemoval(link, executable.Target) }))
	}
	if spec.DesktopEnabled {
		errs = appendIf(errs, detachOwned(paths.DesktopEntry, func(path string) error { return validateDesktopForRemoval(path, spec.ID, spec.DesktopSHA256) }))
	}
	if spec.Icon != "" {
		errs = appendIf(errs, detachOwned(paths.IconFile, func(path string) error { return validateIconForRemoval(path, spec.IconSHA256) }))
	}
	return errors.Join(errs...)
}

// detachOwned moves the already-validated directory entry to a private
// temporary name before checking and deleting it. A replacement raced into
// the public name is therefore never selected for removal.
func detachOwned(path string, validate func(string) error) error {
	if err := validateIntegrationParent(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tarlink-remove-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return err
	}
	if err := os.Rename(path, temporaryPath); err != nil {
		return err
	}
	if err := validate(temporaryPath); err != nil {
		if _, pathErr := os.Lstat(path); errors.Is(pathErr, os.ErrNotExist) {
			_ = os.Rename(temporaryPath, path)
		}
		return err
	}
	return os.Remove(temporaryPath)
}

const maxIconBytes = 4 << 20

func iconName(spec Spec) string {
	if spec.Icon != "" {
		return "tarlink-" + spec.ID
	}
	return "application-x-executable"
}

func ensureIcon(spec Spec, destination string) (bool, error) {
	content, err := readIconSource(spec)
	if err != nil {
		return false, err
	}
	if err := filesystem.SecureMkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return false, err
	}
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("%w: %s", ErrConflict, destination)
		}
		existing, readErr := readBoundedRegular(destination, maxIconBytes)
		if readErr != nil || string(existing) != string(content) {
			return false, fmt.Errorf("%w: %s", ErrConflict, destination)
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return atomicCreateIcon(destination, content)
}

func ensureIconSource(spec Spec) error {
	content, err := readIconSource(spec)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(content)
	if spec.IconSHA256 == "" || hex.EncodeToString(digest[:]) != spec.IconSHA256 {
		return fmt.Errorf("%w: icon source digest mismatch", ErrConflict)
	}
	return nil
}

func readIconSource(spec Spec) ([]byte, error) {
	root := spec.IconSourceRoot
	if root == "" {
		root = spec.ApplicationRoot
	}
	source, err := safeSource(root, spec.Icon)
	if err != nil {
		return nil, err
	}
	content, err := readBoundedRegular(source, maxIconBytes)
	if err != nil {
		return nil, fmt.Errorf("read icon source: %w", err)
	}
	return content, nil
}

func atomicCreateIcon(destination string, content []byte) (bool, error) {
	if err := filesystem.SecureMkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return false, err
	}
	return atomicCreate(destination, content, 0o644)
}

func atomicCreateExisting(destination string, content []byte, mode os.FileMode) error {
	created, err := atomicCreateIcon(destination, content)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("%w: %s", ErrConflict, destination)
	}
	return nil
}

func replaceOwned(path, expected string, old, next []byte, mode os.FileMode) (func() error, error) {
	if len(expected) != sha256.Size*2 {
		return nil, fmt.Errorf("%w: ownership digest is invalid", ErrConflict)
	}
	digest := sha256.Sum256(old)
	if hex.EncodeToString(digest[:]) != expected {
		return nil, fmt.Errorf("%w: %s was modified", ErrConflict, path)
	}
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	if _, err := atomicCreate(path, next, mode); err != nil {
		_, restoreErr := atomicCreate(path, old, mode)
		return nil, errors.Join(err, restoreErr)
	}
	return func() error {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return atomicCreateExisting(path, old, mode)
	}, nil
}

func IconDigest(root, relative string) (string, error) {
	source, err := safeSource(root, relative)
	if err != nil {
		return "", err
	}
	content, err := readBoundedRegular(source, maxIconBytes)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

func safeSource(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != filepath.FromSlash(relative) {
		return "", fmt.Errorf("icon source is not a clean relative path")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", filesystem.ErrSymlink
	}
	if !rootInfo.IsDir() {
		return "", fmt.Errorf("icon source root is not a directory")
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("icon source escapes application root")
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", filesystem.ErrSymlink
		}
	}
	return path, nil
}

func readBoundedRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("icon source is not a regular file")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("icon exceeds size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("icon exceeds size limit")
	}
	return content, nil
}

func validateIcon(path, expected string) error {
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("%w: icon ownership digest is missing", ErrConflict)
	}
	content, err := readBoundedRegular(path, maxIconBytes)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrConflict, path)
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != expected {
		return fmt.Errorf("%w: %s was modified", ErrConflict, path)
	}
	return nil
}

func validateIconForRemoval(path, expected string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return validateIcon(path, expected)
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
	return atomicCreate(path, content, 0o644)
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

func validateSymlinkForRemoval(link, target string) error {
	info, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
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

func validateDesktopForRemoval(path, id, expectedSHA256 string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect desktop integration: %w", err)
	}
	return validateDesktop(path, id, expectedSHA256)
}

func atomicCreate(path string, content []byte, mode os.FileMode) (bool, error) {
	return atomicCreateWithSync(path, content, mode, syncDirectory)
}

func atomicCreateWithSync(path string, content []byte, mode os.FileMode, syncDir func(string) error) (bool, error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tarlink-desktop-*")
	if err != nil {
		return false, err
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
		return false, err
	}
	if _, err := temporary.Write(content); err != nil {
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	// Linking a completely written temporary inode into place is atomic and
	// fails instead of overwriting a concurrently created user file.
	if err := os.Link(name, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, fmt.Errorf("%w: %s", ErrConflict, path)
		}
		return false, err
	}
	if err := os.Remove(name); err != nil {
		return true, err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return true, err
	}
	keep = true
	return true, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func desktopText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return value
}

func desktopExec(value string) string {
	if !strings.ContainsAny(value, " \t\n\\\"`$%") {
		return value
	}
	replacer := strings.NewReplacer("\\", "\\\\\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%", "\n", " ", "\r", " ")
	return "\"" + replacer.Replace(value) + "\""
}

func appendIf(errs []error, err error) []error {
	if err != nil && !os.IsNotExist(err) {
		return append(errs, err)
	}
	return errs
}

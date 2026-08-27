package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drobilica/tarlink/internal/archive"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/locking"
)

type Syncer struct {
	CacheRoot   string
	LocksRoot   string
	Client      *download.Client
	Progress    func(stage string, current, total int64)
	sourceURL   string
	allowedURL  download.URLPolicy
	lockTimeout time.Duration
	syncCache   func(string) error
	restore     func(cacheRoot, current, previous string) error
	prune       func(generations, current, previous string) error
	// Now is injectable for deterministic freshness and refresh reporting.
	// Production callers leave it nil to use the system clock.
	Now func() time.Time
}

func (s *Syncer) Sync(ctx context.Context) error {
	_, err := s.SyncWithCheckedAt(ctx)
	return err
}

// SyncWithCheckedAt fetches, validates, and atomically activates the official
// registry. The returned time is persisted in the activated generation only
// on success; failures leave the prior generation and its checked-at intact.
func (s *Syncer) SyncWithCheckedAt(ctx context.Context) (time.Time, error) {
	if s.Client == nil {
		s.Client = download.NewClient()
	}
	if s.sourceURL == "" {
		s.sourceURL = OfficialArchiveURL
	}
	if s.allowedURL == nil {
		s.allowedURL = officialRegistryURL
	}
	if !filepath.IsAbs(s.CacheRoot) || !filepath.IsAbs(s.LocksRoot) {
		return time.Time{}, errors.New("registry cache and lock roots must be absolute")
	}
	lock, err := locking.AcquireRegistryWithTimeout(ctx, s.LocksRoot, s.lockTimeout)
	if err != nil {
		return time.Time{}, fmt.Errorf("acquire registry lock: %w", err)
	}
	defer lock.Release()

	if err := filesystem.SecureMkdirAll(s.CacheRoot, 0o700); err != nil {
		return time.Time{}, err
	}
	generations := filepath.Join(s.CacheRoot, "generations")
	if err := filesystem.SecureMkdirAll(generations, 0o700); err != nil {
		return time.Time{}, err
	}
	stage, err := os.MkdirTemp(s.CacheRoot, ".sync-*")
	if err != nil {
		return time.Time{}, fmt.Errorf("create registry staging directory: %w", err)
	}
	defer func() { _ = filesystem.SafeRemove(s.CacheRoot, stage) }()

	s.report("downloading", 0, 0)
	archivePath := filepath.Join(stage, "registry.tar.gz")
	_, err = s.Client.FetchRegistry(ctx, download.RegistryRequest{
		URL: s.sourceURL, Destination: archivePath,
		AllowedURL:     s.allowedURL,
		ReportProgress: func(current, total int64) { s.report("downloading", current, total) },
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("download official registry: %w", err)
	}

	extracted := filepath.Join(stage, "extracted")
	if err := os.Mkdir(extracted, 0o700); err != nil {
		return time.Time{}, err
	}
	s.report("extracting", 0, 0)
	if err := archive.ExtractPath(ctx, archivePath, extracted, archive.FormatTarGz, archive.Limits{
		MaxEntries: 20_000, MaxTotalBytes: 256 << 20, MaxFileBytes: 16 << 20,
		MaxArchiveBytes: download.DefaultMaxRegistryBytes, MaxPathBytes: 4096, MaxDepth: 32,
	}); err != nil {
		return time.Time{}, fmt.Errorf("extract registry: %w", err)
	}
	sourceRoot, err := singleRoot(extracted)
	if err != nil {
		return time.Time{}, err
	}
	s.report("validating", 0, 0)
	if _, err := validateTree(sourceRoot, false); err != nil {
		return time.Time{}, fmt.Errorf("validate staged registry: %w", err)
	}

	generation, err := os.MkdirTemp(generations, "generation-*")
	if err != nil {
		return time.Time{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = filesystem.SafeRemove(generations, generation)
		}
	}()
	if err := os.Rename(filepath.Join(sourceRoot, "apps"), filepath.Join(generation, "apps")); err != nil {
		return time.Time{}, fmt.Errorf("stage validated registry applications: %w", err)
	}
	if err := normalizeTree(filepath.Join(generation, "apps")); err != nil {
		return time.Time{}, fmt.Errorf("normalize registry permissions: %w", err)
	}
	checkedAt := s.clock().UTC().Truncate(time.Second)
	if err := writeGenerationMetadata(generation, checkedAt); err != nil {
		return time.Time{}, fmt.Errorf("write registry generation metadata: %w", err)
	}
	if _, err := ValidateTree(generation); err != nil {
		return time.Time{}, fmt.Errorf("validate sanitized registry: %w", err)
	}
	if err := syncTree(generation); err != nil {
		return time.Time{}, fmt.Errorf("flush registry generation: %w", err)
	}
	syncCache := s.syncCache
	if syncCache == nil {
		syncCache = syncDirectory
	}
	restore := s.restore
	if restore == nil {
		restore = restoreCurrent
	}
	previous, err := activeGeneration(s.CacheRoot, generations)
	if err != nil {
		return time.Time{}, fmt.Errorf("resolve active registry generation: %w", err)
	}
	activated, err := activateGeneration(s.CacheRoot, generation, syncCache, restore)
	published = activated
	if err != nil {
		return time.Time{}, fmt.Errorf("activate registry: %w", err)
	}
	prune := s.prune
	if prune == nil {
		prune = pruneGenerations
	}
	if err := prune(generations, generation, previous); err != nil {
		published, err = rollbackGeneration(s.CacheRoot, generation, previous, fmt.Errorf("prune registry generations: %w", err), restore)
		return time.Time{}, err
	}
	s.report("complete", 0, 0)
	return checkedAt, nil
}

func (s *Syncer) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func writeGenerationMetadata(generation string, checkedAt time.Time) error {
	data, err := json.Marshal(generationMetadata{CheckedAt: checkedAt.UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(generation, ".metadata-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(generation, GenerationMetadataFile))
}

func (s *Syncer) report(stage string, current, total int64) {
	if s.Progress != nil {
		s.Progress(stage, current, total)
	}
}

func officialRegistryURL(candidate *url.URL) bool {
	return candidate != nil && candidate.Scheme == "https" && candidate.User == nil &&
		strings.EqualFold(candidate.Host, "codeload.github.com") &&
		candidate.Path == "/drobilica/tarlink-registry/tar.gz/refs/heads/main" && candidate.RawQuery == "" && candidate.Fragment == ""
}

func singleRoot(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	if len(entries) != 1 || !entries[0].IsDir() || entries[0].Type()&os.ModeSymlink != 0 {
		return "", errors.New("official registry archive must contain one top-level directory")
	}
	return filepath.Join(root, entries[0].Name()), nil
}

func normalizeTree(root string) error {
	return filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("registry generation contains a symlink")
		}
		if entry.IsDir() {
			return os.Chmod(current, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("registry generation contains unsupported file %q", current)
		}
		return os.Chmod(current, 0o600)
	})
}

func pruneGenerations(generations, current, previous string) error {
	keep := map[string]bool{filepath.Clean(current): true}
	if previous != "" {
		keep[filepath.Clean(previous)] = true
	}
	entries, err := os.ReadDir(generations)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(generations, entry.Name())
		if keep[path] {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() || !strings.HasPrefix(entry.Name(), "generation-") {
			return fmt.Errorf("unexpected registry generation entry %q", entry.Name())
		}
		if err := filesystem.SafeRemove(generations, path); err != nil {
			return err
		}
	}
	return nil
}

func activeGeneration(cacheRoot, generations string) (string, error) {
	current := filepath.Join(cacheRoot, "current")
	info, err := os.Lstat(current)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("registry current path is occupied by a non-symlink")
	}
	target, err := os.Readlink(current)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(target) {
		return "", errors.New("registry current pointer must be relative")
	}
	path := filepath.Clean(filepath.Join(cacheRoot, target))
	if filepath.Dir(path) != filepath.Clean(generations) || !strings.HasPrefix(filepath.Base(path), "generation-") {
		return "", errors.New("registry current pointer is not a direct generation")
	}
	return path, nil
}

func syncTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		directory, err := os.Open(directories[i])
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}

func activateGeneration(
	cacheRoot, generation string,
	syncCache func(string) error,
	restore func(cacheRoot, current, previous string) error,
) (bool, error) {
	current := filepath.Join(cacheRoot, "current")
	generations := filepath.Join(cacheRoot, "generations")
	previous, err := activeGeneration(cacheRoot, generations)
	if err != nil {
		return false, err
	}
	if info, err := os.Lstat(current); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return false, errors.New("registry current path is occupied by a non-symlink")
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	temporary, err := os.CreateTemp(cacheRoot, ".current-*")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return false, err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return false, err
	}
	relative, err := filepath.Rel(cacheRoot, generation)
	if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") {
		return false, errors.New("registry generation escapes cache root")
	}
	if err := os.Symlink(relative, temporaryPath); err != nil {
		return false, err
	}
	if err := os.Rename(temporaryPath, current); err != nil {
		_ = os.Remove(temporaryPath)
		return false, err
	}
	if err := syncCache(cacheRoot); err != nil {
		// The pointer replacement is provisional until the cache directory is
		// flushed. Restore the prior active generation on publication failure
		// so a failed refresh cannot advance either catalog or checked-at.
		return rollbackGeneration(cacheRoot, generation, previous, err, restore)
	}
	return true, nil
}

func rollbackGeneration(
	cacheRoot, generation, previous string,
	cause error,
	restore func(cacheRoot, current, previous string) error,
) (bool, error) {
	current := filepath.Join(cacheRoot, "current")
	restoreErr := restore(cacheRoot, current, previous)
	if restoreErr == nil {
		return false, cause
	}
	// Restoration itself can fail before replacing the provisional pointer.
	// Keep the incoming generation whenever it may still be referenced so
	// cleanup cannot turn a recoverable refresh failure into a dangling
	// current pointer. A later successful sync prunes the extra generation.
	generations := filepath.Join(cacheRoot, "generations")
	active, activeErr := activeGeneration(cacheRoot, generations)
	provisionalMayBeActive := activeErr != nil || active == filepath.Clean(generation)
	return provisionalMayBeActive, errors.Join(cause, restoreErr, activeErr)
}

func restoreCurrent(cacheRoot, current, previous string) error {
	if previous == "" {
		if err := os.Remove(current); err != nil && !os.IsNotExist(err) {
			return err
		}
		return syncDirectory(cacheRoot)
	}
	relative, err := filepath.Rel(cacheRoot, previous)
	if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") {
		return errors.New("previous registry generation escapes cache root")
	}
	temporary, err := os.CreateTemp(cacheRoot, ".current-*")
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
	if err := os.Symlink(relative, temporaryPath); err != nil {
		return err
	}
	// Rename over the provisional pointer so readers observe either the new
	// generation or the restored previous generation, never a missing pointer.
	if err := os.Rename(temporaryPath, current); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return syncDirectory(cacheRoot)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

package registry

import (
	"context"
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
}

func (s *Syncer) Sync(ctx context.Context) error {
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
		return errors.New("registry cache and lock roots must be absolute")
	}
	lock, err := locking.AcquireRegistryWithTimeout(ctx, s.LocksRoot, s.lockTimeout)
	if err != nil {
		return fmt.Errorf("acquire registry lock: %w", err)
	}
	defer lock.Release()

	if err := filesystem.SecureMkdirAll(s.CacheRoot, 0o700); err != nil {
		return err
	}
	generations := filepath.Join(s.CacheRoot, "generations")
	if err := filesystem.SecureMkdirAll(generations, 0o700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(s.CacheRoot, ".sync-*")
	if err != nil {
		return fmt.Errorf("create registry staging directory: %w", err)
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
		return fmt.Errorf("download official registry: %w", err)
	}

	extracted := filepath.Join(stage, "extracted")
	if err := os.Mkdir(extracted, 0o700); err != nil {
		return err
	}
	s.report("extracting", 0, 0)
	if err := archive.ExtractPath(ctx, archivePath, extracted, archive.FormatTarGz, archive.Limits{
		MaxEntries: 20_000, MaxTotalBytes: 256 << 20, MaxFileBytes: 16 << 20,
		MaxArchiveBytes: download.DefaultMaxRegistryBytes, MaxPathBytes: 4096, MaxDepth: 32,
	}); err != nil {
		return fmt.Errorf("extract registry: %w", err)
	}
	sourceRoot, err := singleRoot(extracted)
	if err != nil {
		return err
	}
	s.report("validating", 0, 0)
	if _, err := ValidateTree(sourceRoot); err != nil {
		return fmt.Errorf("validate staged registry: %w", err)
	}

	generation, err := os.MkdirTemp(generations, "generation-*")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = filesystem.SafeRemove(generations, generation)
		}
	}()
	if err := os.Rename(filepath.Join(sourceRoot, "apps"), filepath.Join(generation, "apps")); err != nil {
		return fmt.Errorf("stage validated registry applications: %w", err)
	}
	if err := normalizeTree(filepath.Join(generation, "apps")); err != nil {
		return fmt.Errorf("normalize registry permissions: %w", err)
	}
	if _, err := ValidateTree(generation); err != nil {
		return fmt.Errorf("validate sanitized registry: %w", err)
	}
	if err := syncTree(generation); err != nil {
		return fmt.Errorf("flush registry generation: %w", err)
	}
	if err := pruneGenerations(s.CacheRoot, generations, generation); err != nil {
		return fmt.Errorf("prune registry generations: %w", err)
	}
	syncCache := s.syncCache
	if syncCache == nil {
		syncCache = syncDirectory
	}
	activated, err := activateGeneration(s.CacheRoot, generation, syncCache)
	published = activated
	if err != nil {
		return fmt.Errorf("activate registry: %w", err)
	}
	s.report("complete", 0, 0)
	return nil
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

func pruneGenerations(cacheRoot, generations, incoming string) error {
	keep := map[string]bool{filepath.Clean(incoming): true}
	current, err := activeGeneration(cacheRoot, generations)
	if err != nil {
		return err
	}
	if current != "" {
		keep[current] = true
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

func activateGeneration(cacheRoot, generation string, syncCache func(string) error) (bool, error) {
	current := filepath.Join(cacheRoot, "current")
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
		return true, err
	}
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

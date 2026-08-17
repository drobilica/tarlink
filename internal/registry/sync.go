package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	for _, name := range []string{"apps", "policy", "index"} {
		if err := copyTree(filepath.Join(sourceRoot, name), filepath.Join(generation, name)); err != nil {
			return fmt.Errorf("stage validated registry %s: %w", name, err)
		}
	}
	if _, err := ValidateTree(generation); err != nil {
		return fmt.Errorf("validate sanitized registry: %w", err)
	}
	if err := syncTree(generation); err != nil {
		return fmt.Errorf("flush registry generation: %w", err)
	}
	if err := activateGeneration(s.CacheRoot, generation); err != nil {
		return fmt.Errorf("activate registry: %w", err)
	}
	published = true
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

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("registry copy encountered a symlink")
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("registry copy encountered unsupported file %q", current)
		}
		input, err := os.Open(current)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, io.LimitReader(input, 16<<20+1))
		inputErr := input.Close()
		syncErr := output.Sync()
		outputErr := output.Close()
		return errors.Join(copyErr, inputErr, syncErr, outputErr)
	})
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

func activateGeneration(cacheRoot, generation string) error {
	current := filepath.Join(cacheRoot, "current")
	if info, err := os.Lstat(current); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return errors.New("registry current path is occupied by a non-symlink")
		}
	} else if !os.IsNotExist(err) {
		return err
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
	relative, err := filepath.Rel(cacheRoot, generation)
	if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") {
		return errors.New("registry generation escapes cache root")
	}
	if err := os.Symlink(relative, temporaryPath); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, current); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	directory, err := os.Open(cacheRoot)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

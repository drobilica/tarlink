// Package runtime owns immutable, digest-pinned execution-runtime deployments.
package runtime

import (
	"archive/tar"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/state"
	"github.com/ulikunitz/xz"
)

const maxRuntimeBytes int64 = 2 << 30

const (
	maxRuntimeEntries = 100000
	maxRuntimePath    = 4096
	maxRuntimeDepth   = 64
)

// Ensure publishes one complete immutable deployment, or leaves no deployment
// visible. Callers hold TarLink's lifecycle lock; that lock also makes shared
// runtime acquisition converge across applications.
func Ensure(ctx context.Context, layout filesystem.Layout, client *download.Client, value *manifest.Runtime, progress download.Progress) (string, string, error) {
	if value == nil {
		return "", "", errors.New("runtime is nil")
	}
	if err := value.Validate(); err != nil {
		return "", "", err
	}
	fingerprint, err := value.Fingerprint()
	if err != nil {
		return "", "", err
	}
	destination, err := layout.RuntimePath(value.ID, value.Version, fingerprint)
	if err != nil {
		return "", "", err
	}
	if err := filesystem.CheckOwnedDirectoryWithin(layout.Home, layout.Runtimes); err != nil && !os.IsNotExist(err) {
		return "", "", err
	}
	if err := filesystem.CheckOwnedDirectoryWithin(layout.Home, filepath.Dir(destination)); err != nil && !os.IsNotExist(err) {
		return "", "", err
	}
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("runtime destination is not an owned directory")
		}
		if err := validateDeployment(destination); err != nil {
			return "", "", err
		}
		return destination, fingerprint, nil
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	if err := filesystem.SecureMkdirAll(filepath.Dir(destination), 0700); err != nil {
		return "", "", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".tarlink-runtime-stage-")
	if err != nil {
		return "", "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(stage)
		}
	}()
	archivePath := filepath.Join(stage, "artifact.tar.xz")
	if _, err := client.FetchArtifact(ctx, download.ArtifactRequest{URL: value.Artifact.URL, Algorithm: "sha256", Digest: value.Artifact.Verification.Digest, Destination: archivePath, MaxBytes: maxRuntimeBytes, ReportProgress: progress}); err != nil {
		return "", "", err
	}
	extracted := filepath.Join(stage, "extracted")
	if err := os.Mkdir(extracted, 0700); err != nil {
		return "", "", err
	}
	if err := extractValveDeployment(ctx, archivePath, extracted); err != nil {
		return "", "", err
	}
	root, err := singleRoot(extracted)
	if err != nil {
		return "", "", err
	}
	if err := validateDeployment(root); err != nil {
		return "", "", err
	}
	if err := os.Rename(root, destination); err != nil {
		if os.IsExist(err) {
			if err := validateDeployment(destination); err == nil {
				return destination, fingerprint, nil
			}
		}
		return "", "", err
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return "", "", err
	}
	keep = true
	return destination, fingerprint, nil
}

// GC removes only deployments proven unreferenced by every valid retained
// application closure. Any malformed state or unexpected runtime-tree entry
// stops collection before deletion (fail closed).
func GC(layout filesystem.Layout) error {
	if err := validateOwnedOrMissing(layout.Home, layout.States); err != nil {
		return err
	}
	if err := validateOwnedOrMissing(layout.Home, layout.Runtimes); err != nil {
		return err
	}
	states, err := os.ReadDir(layout.States)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for _, entry := range states {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			return errors.New("cannot determine runtime liveness from state directory")
		}
		value, err := state.LoadForApp(layout, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return fmt.Errorf("cannot determine runtime liveness: %w", err)
		}
		for _, candidate := range []*manifest.Runtime{value.Runtime, value.PreviousRuntime} {
			if candidate == nil {
				continue
			}
			fingerprint, err := candidate.Fingerprint()
			if err != nil {
				return err
			}
			path, err := layout.RuntimePath(candidate.ID, candidate.Version, fingerprint)
			if err != nil {
				return err
			}
			live[path] = true
		}
	}
	root, err := os.ReadDir(layout.Runtimes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, id := range root {
		if !id.IsDir() || id.Type()&os.ModeSymlink != 0 || filesystem.ValidateID(id.Name()) != nil {
			return errors.New("runtime root contains unexpected entry")
		}
		versions, err := os.ReadDir(filepath.Join(layout.Runtimes, id.Name()))
		if err != nil {
			return err
		}
		for _, version := range versions {
			if !version.IsDir() || version.Type()&os.ModeSymlink != 0 || filesystem.ValidateVersion(version.Name()) != nil {
				return errors.New("runtime version root contains unexpected entry")
			}
			deployments, err := os.ReadDir(filepath.Join(layout.Runtimes, id.Name(), version.Name()))
			if err != nil {
				return err
			}
			for _, deployment := range deployments {
				if !deployment.IsDir() || deployment.Type()&os.ModeSymlink != 0 || !validDeploymentName(deployment.Name()) {
					return errors.New("runtime version contains unexpected entry")
				}
				candidate := filepath.Join(layout.Runtimes, id.Name(), version.Name(), deployment.Name())
				if !live[candidate] {
					if err := filesystem.SafeRemove(filepath.Join(layout.Runtimes, id.Name(), version.Name()), candidate); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func validDeploymentName(name string) bool {
	const prefix = ".tarlink-runtime-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+64 {
		return false
	}
	digest := name[len(prefix):]
	if strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func validateOwnedOrMissing(root, path string) error {
	err := filesystem.CheckOwnedDirectoryWithin(root, path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func extractValveDeployment(ctx context.Context, source, destination string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := xz.NewReader(file)
	if err != nil {
		return err
	}
	tarReader := tar.NewReader(reader)
	entries := 0
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		entries++
		if entries > maxRuntimeEntries || header.Size < 0 || header.Size > maxRuntimeBytes || total > maxRuntimeBytes-header.Size {
			return errors.New("runtime archive exceeds extraction budget")
		}
		total += header.Size
		name, err := runtimePath(header.Name)
		if err != nil {
			return err
		}
		output := filepath.Join(destination, filepath.FromSlash(name))
		if err := mkdirParents(destination, path.Dir(name)); err != nil {
			return err
		}
		mode := os.FileMode(header.Mode)
		if mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errors.New("runtime archive has special mode bits")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(output, 0755); err != nil && !os.IsExist(err) {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(f, io.LimitReader(tarReader, header.Size))
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if header.Mode&0111 != 0 {
				if err := os.Chmod(output, 0755); err != nil {
					return err
				}
			}
		case tar.TypeSymlink:
			if err := safeLink(name, header.Linkname); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, output); err != nil {
				return err
			}
		default:
			return errors.New("runtime archive contains unsupported entry type")
		}
	}
	return nil
}

func runtimePath(value string) (string, error) {
	trimmed := strings.TrimSuffix(value, "/")
	if value == "" || len(value) > maxRuntimePath || !strings.HasPrefix(value, "SteamLinuxRuntime_4/") || path.Clean(value) != trimmed || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.Contains(value, "../") || pathDepth(trimmed) > maxRuntimeDepth {
		return "", errors.New("unsafe runtime archive path")
	}
	return trimmed, nil
}

func pathDepth(value string) int { return len(strings.Split(value, "/")) }
func safeLink(name, target string) error {
	if target == "" || path.IsAbs(target) || strings.Contains(target, "\\") {
		return errors.New("unsafe runtime symlink")
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if !strings.HasPrefix(resolved, "SteamLinuxRuntime_4/") {
		return errors.New("runtime symlink escapes deployment")
	}
	return nil
}
func mkdirParents(root, relative string) error {
	if relative == "." {
		return nil
	}
	current := root
	for _, component := range strings.Split(relative, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("runtime archive parent is unsafe")
		}
	}
	return nil
}
func singleRoot(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	if len(entries) != 1 || !entries[0].IsDir() || entries[0].Type()&os.ModeSymlink != 0 || entries[0].Name() != "SteamLinuxRuntime_4" {
		return "", errors.New("runtime archive must contain SteamLinuxRuntime_4 deployment")
	}
	return filepath.Join(root, entries[0].Name()), nil
}
func validateDeployment(root string) error {
	entry := filepath.Join(root, "_v2-entry-point")
	info, err := os.Lstat(entry)
	if err != nil {
		return fmt.Errorf("runtime entry point: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return errors.New("runtime entry point is not executable")
	}
	return filepath.WalkDir(root, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return safeLink("SteamLinuxRuntime_4/"+filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator))), target)
		}
		if !entry.Type().IsRegular() && !entry.IsDir() {
			return errors.New("runtime deployment contains special file")
		}
		return nil
	})
}
func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

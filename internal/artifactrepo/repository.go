// Package artifactrepo implements TarLink's static, content-addressed artifact
// repositories. It has no index and is safe to publish with ordinary static
// hosting tools.
package artifactrepo

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/locking"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
	"golang.org/x/sys/unix"
)

const (
	Format                     = "content-repository"
	Version                    = 1
	MaxDescriptorBytes   int64 = 4 << 10
	MaxObjectBytes       int64 = 8 << 30
	MaxRepositoryObjects       = 100000
)

type Descriptor struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
}
type Object struct {
	Algorithm string   `json:"algorithm"`
	Digest    string   `json:"digest"`
	Size      int64    `json:"size"`
	URLs      []string `json:"urls"`
}
type Fetch func(context.Context, string, string, string, string) error

var ErrDestinationWrite = errors.New("repository destination write failed")

type Selection struct {
	App         string `json:"app"`
	Platform    string `json:"platform"`
	AllRetained bool   `json:"all_retained"`
}

func Required(catalog *registry.Catalog, selection Selection) ([]Object, error) {
	plan, err := BuildPlan(catalog, selection)
	if err != nil {
		return nil, err
	}
	return plan.Desired, nil
}

type Plan struct {
	Selection   Selection
	Desired     []Object
	Complete    []Object
	Unsupported []string
}

func objectKey(algorithm, digest string) string { return algorithm + ":" + digest }

func BuildPlan(catalog *registry.Catalog, selection Selection) (*Plan, error) {
	if catalog == nil || catalog.Revision == "" {
		return nil, errors.New("validated registry revision is required")
	}
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	if selection.App != "" {
		if _, ok := catalog.Variants[selection.App]; !ok {
			return nil, fmt.Errorf("unknown application %q", selection.App)
		}
		if selection.Platform != "" {
			platforms := catalog.Variants[selection.App]
			found := false
			for platform := range platforms {
				if platform.OS+"-"+platform.Arch == selection.Platform {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("application %q has no %s artifact", selection.App, selection.Platform)
			}
		}
	}
	desiredIndex := map[string]int{}
	completeIndex := map[string]int{}
	var desired []Object
	var complete []Object
	urlToKey := map[string]string{}
	digestToAlgorithm := map[string]string{}
	recordComplete := func(algorithm, digest, url, id, version string) error {
		if algorithm == "" {
			algorithm = "sha256"
		}
		if _, err := objectPath("/", algorithm, digest); err != nil {
			return fmt.Errorf("%s %s: %w", id, version, err)
		}
		key := objectKey(algorithm, digest)
		if previous, ok := urlToKey[url]; ok && previous != key {
			return fmt.Errorf("conflicting repository metadata for %q", url)
		}
		urlToKey[url] = key
		if previous, ok := digestToAlgorithm[digest]; ok && previous != algorithm {
			return fmt.Errorf("conflicting repository metadata for digest %q", digest)
		}
		digestToAlgorithm[digest] = algorithm
		if index, ok := completeIndex[key]; ok {
			known := false
			for _, existing := range complete[index].URLs {
				if existing == url {
					known = true
					break
				}
			}
			if !known {
				complete[index].URLs = append(complete[index].URLs, url)
			}
		} else {
			completeIndex[key] = len(complete)
			complete = append(complete, Object{Algorithm: algorithm, Digest: digest, URLs: []string{url}})
		}
		return nil
	}
	recordDesired := func(algorithm, digest, url string) error {
		if algorithm == "" {
			algorithm = "sha256"
		}
		if _, err := objectPath("/", algorithm, digest); err != nil {
			return err
		}
		key := objectKey(algorithm, digest)
		if index, ok := desiredIndex[key]; ok {
			for _, existing := range desired[index].URLs {
				if existing == url {
					return nil
				}
			}
			desired[index].URLs = append(desired[index].URLs, url)
		} else {
			desiredIndex[key] = len(desired)
			desired = append(desired, Object{Algorithm: algorithm, Digest: digest, URLs: []string{url}})
		}
		return nil
	}
	ids := make([]string, 0, len(catalog.Variants))
	for id := range catalog.Variants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var unsupported []string
	for _, id := range ids {
		platforms := catalog.Variants[id]
		platformKeys := make([]string, 0, len(platforms))
		for platform := range platforms {
			platformKeys = append(platformKeys, platform.OS+"-"+platform.Arch)
		}
		sort.Strings(platformKeys)
		matched := false
		for _, platformKey := range platformKeys {
			platform, ok := manifest.ParsePlatformKey(platformKey)
			if !ok {
				return nil, fmt.Errorf("unsupported platform %q", platformKey)
			}
			item := platforms[platform]
			if item == nil {
				return nil, fmt.Errorf("application %q platform %q is unavailable", id, platformKey)
			}
			inScope := (selection.App == "" || selection.App == id) && (selection.Platform == "" || selection.Platform == platformKey)
			if inScope {
				matched = true
			}
			for _, release := range item.ReleaseHistory.Releases {
				if err := recordComplete(release.Verification.Algorithm, release.Verification.Digest, release.URL, id, release.Version); err != nil {
					return nil, err
				}
				if inScope {
					if err := recordDesired(release.Verification.Algorithm, release.Verification.Digest, release.URL); err != nil {
						return nil, fmt.Errorf("%s %s: %w", id, release.Version, err)
					}
				}
				if release.Runtime != nil {
					runtime := release.Runtime
					if err := recordComplete(runtime.Artifact.Verification.Algorithm, runtime.Artifact.Verification.Digest, runtime.Artifact.URL, id, release.Version); err != nil {
						return nil, err
					}
					if inScope {
						if err := recordDesired(runtime.Artifact.Verification.Algorithm, runtime.Artifact.Verification.Digest, runtime.Artifact.URL); err != nil {
							return nil, err
						}
					}
				}
			}
			if item.Desktop.Icon.Remote() {
				if err := recordComplete("sha256", item.Desktop.Icon.SHA256, item.Desktop.Icon.URL, id, "desktop icon"); err != nil {
					return nil, fmt.Errorf("%s %s desktop icon: %w", id, platformKey, err)
				}
				if inScope {
					if err := recordDesired("sha256", item.Desktop.Icon.SHA256, item.Desktop.Icon.URL); err != nil {
						return nil, fmt.Errorf("%s %s desktop icon: %w", id, platformKey, err)
					}
				}
			}
		}
		if selection.App == "" && selection.Platform != "" && !matched {
			unsupported = append(unsupported, id)
		}
	}
	if len(desired) == 0 {
		return nil, errors.New("selection matched no retained releases")
	}
	sort.Slice(desired, func(i, j int) bool {
		if desired[i].Algorithm != desired[j].Algorithm {
			return desired[i].Algorithm < desired[j].Algorithm
		}
		return desired[i].Digest < desired[j].Digest
	})
	sort.Slice(complete, func(i, j int) bool {
		if complete[i].Algorithm != complete[j].Algorithm {
			return complete[i].Algorithm < complete[j].Algorithm
		}
		return complete[i].Digest < complete[j].Digest
	})
	sort.Strings(unsupported)
	completeURLs := make(map[string][]string, len(complete))
	for _, object := range complete {
		completeURLs[objectKey(object.Algorithm, object.Digest)] = object.URLs
	}
	for i := range desired {
		if urls, ok := completeURLs[objectKey(desired[i].Algorithm, desired[i].Digest)]; ok {
			desired[i].URLs = append([]string(nil), urls...)
		}
	}
	return &Plan{Selection: selection, Desired: desired, Complete: complete, Unsupported: unsupported}, nil
}

func (s Selection) Validate() error {
	if s.Platform != "" {
		if _, ok := manifest.ParsePlatformKey(s.Platform); !ok {
			return errors.New("unsupported platform")
		}
	}
	return nil
}

func objectPath(root, algorithm, digest string) (string, error) {
	length := map[string]int{"sha256": 64, "sha512": 128}[algorithm]
	if length == 0 || len(digest) != length || strings.ToLower(digest) != digest {
		return "", errors.New("unsupported or invalid object digest")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", errors.New("object digest is not lowercase hexadecimal")
	}
	return filepath.Join(root, "v1", algorithm, digest), nil
}

func Init(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if descriptorInfo, statErr := os.Lstat(filepath.Join(root, "repository.json")); statErr == nil {
		if !descriptorInfo.Mode().IsRegular() || descriptorInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("repository descriptor is not a regular file")
		}
		if err := ensureSyncLock(root); err != nil {
			return err
		}
		return Open(root)
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if rootInfo, statErr := os.Lstat(root); statErr == nil {
		if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("repository root is not a directory")
		}
		if err := filesystem.CheckOwnedDirectory(root); err != nil {
			return fmt.Errorf("repository root: %w", err)
		}
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			return readErr
		}
		if len(entries) != 0 {
			return errors.New("repository descriptor is missing")
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if err = filesystem.SecureMkdirAll(filepath.Join(root, "v1", "sha256"), 0755); err != nil {
		return err
	}
	if err = filesystem.SecureMkdirAll(filepath.Join(root, "v1", "sha512"), 0755); err != nil {
		return err
	}
	for _, directory := range []string{root, filepath.Join(root, "v1")} {
		if err := os.Chmod(directory, 0755); err != nil {
			return err
		}
	}
	data, _ := json.Marshal(Descriptor{Format: Format, Version: Version})
	if err := atomicWrite(filepath.Join(root, "repository.json"), data, 0644); err != nil {
		return err
	}
	if err := ensureSyncLock(root); err != nil {
		return err
	}
	return Open(root)
}

func ensureSyncLock(root string) error {
	if err := filesystem.CheckOwnedDirectory(root); err != nil {
		return fmt.Errorf("repository root: %w", err)
	}
	path := filepath.Join(root, ".sync.lock")
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil {
		_ = syscall.Close(fd)
		return err
	}
	_ = syscall.Close(fd)
	if info.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return errors.New("repository lock path is not a regular file")
	}
	return nil
}

func Open(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("repository path must be absolute and clean")
	}
	if err := filesystem.CheckOwnedDirectory(root); err != nil {
		return fmt.Errorf("repository root: %w", err)
	}
	rootDirectory, err := openRepositoryDirectory(root)
	if err != nil {
		return err
	}
	defer rootDirectory.Close()
	if err := validateRepositoryTree(rootDirectory); err != nil {
		return err
	}
	return nil
}

func validateRepositoryTree(rootDirectory *os.File) error {
	entries, err := rootDirectory.ReadDir(-1)
	if err != nil {
		return err
	}
	seenDescriptor, seenV1 := false, false
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case name == "repository.json":
			if seenDescriptor {
				return errors.New("repository contains duplicate descriptor")
			}
			seenDescriptor = true
			file, openErr := openRepositoryFileAt(rootDirectory, name)
			if openErr != nil {
				return fmt.Errorf("repository descriptor: %w", openErr)
			}
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() || !singleLink(info) {
				file.Close()
				return errors.New("repository descriptor is not a regular file")
			}
			decodeErr := decodeDescriptor(file)
			closeErr := file.Close()
			if decodeErr != nil {
				return decodeErr
			}
			if closeErr != nil {
				return closeErr
			}
		case name == "v1":
			if seenV1 {
				return errors.New("repository contains duplicate v1 directory")
			}
			seenV1 = true
			v1, openErr := openRepositoryDirectoryAt(rootDirectory, name)
			if openErr != nil {
				return fmt.Errorf("repository tree contains unsafe directory %q: %w", name, openErr)
			}
			if err := validateV1Tree(v1); err != nil {
				v1.Close()
				return err
			}
			if closeErr := v1.Close(); closeErr != nil {
				return closeErr
			}
		case name == ".sync.lock":
			if err := validateTransientFile(rootDirectory, name); err != nil {
				return err
			}
		case strings.HasPrefix(name, ".object-stage-") && len(name) > len(".object-stage-"):
			staging, openErr := openRepositoryDirectoryAt(rootDirectory, name)
			if openErr != nil {
				return fmt.Errorf("repository staging entry %q is unsafe: %w", name, openErr)
			}
			if closeErr := staging.Close(); closeErr != nil {
				return closeErr
			}
		case strings.HasPrefix(name, ".descriptor-") && len(name) > len(".descriptor-"):
			if err := validateTransientFile(rootDirectory, name); err != nil {
				return err
			}
		default:
			return fmt.Errorf("repository contains unexpected entry %q", name)
		}
	}
	if !seenDescriptor {
		return errors.New("repository descriptor is missing")
	}
	if !seenV1 {
		return errors.New("repository v1 directory is missing")
	}
	return nil
}

func validateTransientFile(parent *os.File, name string) error {
	file, err := openRepositoryFileAt(parent, name)
	if err != nil {
		return fmt.Errorf("repository transient entry %q is unsafe: %w", name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !singleLink(info) {
		return fmt.Errorf("repository transient entry %q is not a regular file", name)
	}
	return nil
}

func validateV1Tree(v1 *os.File) error {
	entries, err := v1.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "sha256" && entry.Name() != "sha512" {
			return fmt.Errorf("repository v1 contains unexpected entry %q", entry.Name())
		}
		directory, openErr := openRepositoryDirectoryAt(v1, entry.Name())
		if os.IsNotExist(openErr) {
			continue
		}
		if openErr != nil {
			return fmt.Errorf("repository tree contains unsafe directory %q: %w", entry.Name(), openErr)
		}
		if err := validateObjectTree(directory, entry.Name()); err != nil {
			directory.Close()
			return err
		}
		if closeErr := directory.Close(); closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func validateObjectTree(directory *os.File, algorithm string) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe repository object %q", entry.Name())
		}
		if _, err := objectPath("/", algorithm, entry.Name()); err != nil {
			return fmt.Errorf("unsafe repository object %q: %w", entry.Name(), err)
		}
		file, err := openRepositoryFileAt(directory, entry.Name())
		if err != nil {
			return fmt.Errorf("unsafe repository object %q: %w", entry.Name(), err)
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || !info.Mode().IsRegular() || !singleLink(info) {
			return fmt.Errorf("unsafe repository object %q", entry.Name())
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func decodeDescriptor(reader io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(reader, MaxDescriptorBytes+1))
	if err != nil {
		return fmt.Errorf("invalid repository descriptor: %w", err)
	}
	if int64(len(data)) > MaxDescriptorBytes {
		return fmt.Errorf("repository descriptor exceeds %d bytes", MaxDescriptorBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var descriptor Descriptor
	if err := decoder.Decode(&descriptor); err != nil {
		return fmt.Errorf("invalid repository descriptor: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("repository descriptor must contain one JSON object")
	}
	if descriptor.Format != Format || descriptor.Version != Version {
		return fmt.Errorf("unsupported repository descriptor format/version %q/%d", descriptor.Format, descriptor.Version)
	}
	return nil
}

func Verify(root string) ([]Object, error) {
	return VerifyContext(context.Background(), root)
}

func VerifyContext(ctx context.Context, root string) ([]Object, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := Open(root); err != nil {
		return nil, err
	}
	rootDirectory, err := openRepositoryDirectory(root)
	if err != nil {
		return nil, err
	}
	defer rootDirectory.Close()
	v1, err := openRepositoryDirectoryAt(rootDirectory, "v1")
	if err != nil {
		return nil, err
	}
	defer v1.Close()
	var objects []Object
	for _, algorithm := range []string{"sha256", "sha512"} {
		directory, err := openRepositoryDirectoryAt(v1, algorithm)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		defer directory.Close()
		entries, err := directory.ReadDir(MaxRepositoryObjects + 1)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if len(entries) > MaxRepositoryObjects {
			return nil, errors.New("repository contains too many objects")
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
				return nil, fmt.Errorf("unsafe repository object %q", entry.Name())
			}
			if _, err := objectPath(root, algorithm, entry.Name()); err != nil {
				return nil, err
			}
			size, err := verifyFileAt(ctx, directory, entry.Name(), algorithm, entry.Name(), MaxObjectBytes)
			if err != nil {
				return nil, err
			}
			objects = append(objects, Object{Algorithm: algorithm, Digest: entry.Name(), Size: size})
		}
	}
	return objects, nil
}

// VerifyObject hashes one object without treating registry approval as part of
// the result. Callers should call Open first when checking a repository.
func VerifyObject(root string, object Object) (int64, error) {
	return VerifyObjectContext(context.Background(), root, object)
}

func VerifyObjectContext(ctx context.Context, root string, object Object) (int64, error) {
	if err := Open(root); err != nil {
		return 0, err
	}
	if _, err := objectPath(root, object.Algorithm, object.Digest); err != nil {
		return 0, err
	}
	rootDirectory, err := openRepositoryDirectory(root)
	if err != nil {
		return 0, err
	}
	defer rootDirectory.Close()
	v1, err := openRepositoryDirectoryAt(rootDirectory, "v1")
	if err != nil {
		return 0, err
	}
	defer v1.Close()
	directory, err := openRepositoryDirectoryAt(v1, object.Algorithm)
	if err != nil {
		return 0, err
	}
	defer directory.Close()
	return verifyFileAt(ctx, directory, object.Digest, object.Algorithm, object.Digest, MaxObjectBytes)
}

func verifyFileAt(ctx context.Context, directory *os.File, name, algorithm, expected string, maxBytes int64) (int64, error) {
	f, err := openRepositoryFileAt(directory, name)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !singleLink(info) {
		return 0, errors.New("repository object is not regular")
	}
	return verifyOpenedFile(ctx, f, algorithm, expected, maxBytes)
}

func verifyOpenedFile(ctx context.Context, f *os.File, algorithm, expected string, maxBytes int64) (int64, error) {
	var h hash.Hash
	if algorithm == "sha256" {
		h = sha256.New()
	} else {
		h = sha512.New()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	n, err := io.Copy(h, io.LimitReader(contextReader{ctx: ctx, reader: f}, maxBytes+1))
	if err != nil {
		return 0, err
	}
	if n > maxBytes {
		return 0, errors.New("repository object exceeds size limit")
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		return 0, fmt.Errorf("repository object %s has incorrect digest", expected)
	}
	return n, nil
}

func Add(ctx context.Context, root, algorithm, digest string, fetch Fetch, url string) error {
	if err := Open(root); err != nil {
		return err
	}
	destination, err := objectPath(root, algorithm, digest)
	if err != nil {
		return err
	}
	if err := filesystem.SecureMkdirAll(filepath.Dir(destination), 0755); err != nil {
		return fmt.Errorf("%w: prepare object directory: %v", ErrDestinationWrite, err)
	}
	destinationDirectory, err := openRepositoryDirectory(filepath.Dir(destination))
	if err != nil {
		return fmt.Errorf("%w: open object directory: %v", ErrDestinationWrite, err)
	}
	defer destinationDirectory.Close()
	if err := os.Chmod(filepath.Dir(destination), 0755); err != nil {
		return fmt.Errorf("%w: publish object directory permissions: %v", ErrDestinationWrite, err)
	}
	if size, err := VerifyObjectContext(ctx, root, Object{Algorithm: algorithm, Digest: digest}); err == nil && size >= 0 {
		return nil
	}
	if fetch == nil {
		return errors.New("artifact fetcher is not configured")
	}
	rootDirectory, err := openRepositoryDirectory(root)
	if err != nil {
		return fmt.Errorf("%w: open repository root: %v", ErrDestinationWrite, err)
	}
	defer rootDirectory.Close()
	stageDirectoryPath, err := os.MkdirTemp(root, ".object-stage-*")
	if err != nil {
		return fmt.Errorf("%w: create object staging: %v", ErrDestinationWrite, err)
	}
	stageDirectory, err := openRepositoryDirectory(stageDirectoryPath)
	if err != nil {
		_ = unix.Unlinkat(int(rootDirectory.Fd()), filepath.Base(stageDirectoryPath), unix.AT_REMOVEDIR)
		return fmt.Errorf("%w: open object staging: %v", ErrDestinationWrite, err)
	}
	defer func() {
		_ = unix.Unlinkat(int(stageDirectory.Fd()), "object", 0)
		_ = stageDirectory.Close()
		_ = unix.Unlinkat(int(rootDirectory.Fd()), filepath.Base(stageDirectoryPath), unix.AT_REMOVEDIR)
	}()
	stagePath := filepath.Join(stageDirectoryPath, "object")
	stage, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("%w: create object staging: %v", ErrDestinationWrite, err)
	}
	if err := stage.Close(); err != nil {
		return fmt.Errorf("%w: close object staging: %v", ErrDestinationWrite, err)
	}
	if err := fetch(ctx, url, algorithm, digest, stagePath); err != nil {
		return err
	}
	stageFile, err := openRepositoryFileAt(stageDirectory, "object")
	if err != nil {
		return err
	}
	defer stageFile.Close()
	stageInfo, err := stageFile.Stat()
	if err != nil || !stageInfo.Mode().IsRegular() || !singleLink(stageInfo) {
		return errors.New("repository staging file is not regular")
	}
	if _, err := verifyOpenedFile(ctx, stageFile, algorithm, digest, MaxObjectBytes); err != nil {
		return err
	}
	if _, err := VerifyObjectContext(ctx, root, Object{Algorithm: algorithm, Digest: digest}); err == nil {
		return nil
	}
	if err := stageFile.Chmod(0644); err != nil {
		return fmt.Errorf("%w: publish object permissions: %v", ErrDestinationWrite, err)
	}
	if err := renameObjectWithinRepository(root, stageDirectory, "object", destination, stageInfo); err != nil {
		if os.IsExist(err) {
			if _, verifyErr := VerifyObjectContext(ctx, root, Object{Algorithm: algorithm, Digest: digest}); verifyErr == nil {
				return nil
			}
		}
		return fmt.Errorf("%w: replace object: %v", ErrDestinationWrite, err)
	}
	return nil
}

func Sync(ctx context.Context, root string, objects []Object, fetch Fetch) error {
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := Init(root); err != nil {
		return err
	}
	lock, err := locking.AcquireExistingWithTimeout(ctx, filepath.Join(root, ".sync.lock"), locking.DefaultTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	rootDirectory, err := openRepositoryDirectory(root)
	if err != nil {
		return fmt.Errorf("%w: open repository root: %v", ErrDestinationWrite, err)
	}
	defer rootDirectory.Close()
	if err := cleanupStaging(rootDirectory); err != nil {
		return fmt.Errorf("%w: clean interrupted object staging: %v", ErrDestinationWrite, err)
	}
	var failures []error
	for _, object := range objects {
		if len(object.URLs) == 0 {
			failures = append(failures, fmt.Errorf("%s:%s: acquisition failed", object.Algorithm, object.Digest))
			continue
		}
		acquired := false
		var lastErr error
		for _, url := range object.URLs {
			if err := Add(ctx, root, object.Algorithm, object.Digest, fetch, url); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrDestinationWrite) {
					return err
				}
				lastErr = err
				continue
			}
			acquired = true
			break
		}
		if !acquired {
			if lastErr != nil {
				failures = append(failures, fmt.Errorf("%s:%s: %w", object.Algorithm, object.Digest, lastErr))
			} else {
				failures = append(failures, fmt.Errorf("%s:%s: acquisition failed", object.Algorithm, object.Digest))
			}
		}
	}
	return errors.Join(failures...)
}

func cleanupStaging(rootDirectory *os.File) error {
	entries, err := rootDirectory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".object-stage-") || len(entry.Name()) == len(".object-stage-") {
			continue
		}
		stage, err := openRepositoryDirectoryAt(rootDirectory, entry.Name())
		if err != nil {
			return fmt.Errorf("open staging %q: %w", entry.Name(), err)
		}
		children, readErr := stage.ReadDir(-1)
		if readErr != nil {
			stage.Close()
			return fmt.Errorf("read staging %q: %w", entry.Name(), readErr)
		}
		for _, child := range children {
			if !stagingFileName(child.Name()) || child.Type()&os.ModeSymlink != 0 {
				stage.Close()
				return fmt.Errorf("unexpected interrupted staging entry %q/%q", entry.Name(), child.Name())
			}
			if err := unix.Unlinkat(int(stage.Fd()), child.Name(), 0); err != nil {
				stage.Close()
				return fmt.Errorf("remove interrupted staging object: %w", err)
			}
		}
		if err := stage.Close(); err != nil {
			return err
		}
		if err := unix.Unlinkat(int(rootDirectory.Fd()), entry.Name(), unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("remove interrupted staging directory: %w", err)
		}
	}
	return nil
}

func stagingFileName(name string) bool {
	return name == "object" || strings.HasPrefix(name, ".tarlink-download-") || strings.HasPrefix(name, ".tarlink-source-") || strings.HasPrefix(name, ".tarlink-repository-descriptor-")
}
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".descriptor-*")
	if err != nil {
		return err
	}
	p := f.Name()
	defer os.Remove(p)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = renameWithinDirectory(p, path)
	}
	if err == nil {
		err = os.Chmod(path, mode)
	}
	if err == nil {
		err = syncDirectory(filepath.Dir(path))
	}
	return err
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func singleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat.Nlink == 1
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func renameWithinDirectory(source, destination string) error {
	parent := filepath.Dir(destination)
	fd, err := syscall.Open(parent, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(fd), parent)
	if directory == nil {
		_ = syscall.Close(fd)
		return errors.New("open repository directory")
	}
	defer directory.Close()
	if filepath.Dir(source) != parent {
		return errors.New("repository staging file is outside destination directory")
	}
	if err := syscall.Renameat(int(directory.Fd()), filepath.Base(source), int(directory.Fd()), filepath.Base(destination)); err != nil {
		return err
	}
	return directory.Sync()
}

func renameObjectWithinRepository(root string, sourceDirectory *os.File, sourceName, destination string, expected os.FileInfo) error {
	rootDirectory, err := openRepositoryDirectory(root)
	if err != nil {
		return err
	}
	v1, err := openRepositoryDirectoryAt(rootDirectory, "v1")
	rootDirectory.Close()
	if err != nil {
		return err
	}
	algorithm := filepath.Base(filepath.Dir(destination))
	objects, err := openRepositoryDirectoryAt(v1, algorithm)
	v1.Close()
	if err != nil {
		return err
	}
	defer objects.Close()
	if err := syscall.Renameat(int(sourceDirectory.Fd()), sourceName, int(objects.Fd()), filepath.Base(destination)); err != nil {
		return err
	}
	fd, err := syscall.Openat(int(objects.Fd()), filepath.Base(destination), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	published := os.NewFile(uintptr(fd), filepath.Base(destination))
	if published == nil {
		_ = syscall.Close(fd)
		return errors.New("open published repository object")
	}
	defer published.Close()
	publishedInfo, err := published.Stat()
	if err != nil || !os.SameFile(expected, publishedInfo) {
		return errors.New("published repository object differs from staged file")
	}
	return objects.Sync()
}

func openRepositoryDirectory(path string) (*os.File, error) {
	fd, err := openNoFollowPath(path, true)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("open repository directory")
	}
	return file, nil
}

func openNoFollowPath(path string, directory bool) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, errors.New("repository path must be absolute and clean")
	}
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
		if directory || part != filepath.Base(path) {
			flags |= syscall.O_DIRECTORY
		}
		next, openErr := syscall.Openat(fd, part, flags, 0)
		_ = syscall.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func openRepositoryDirectoryAt(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("open repository directory")
	}
	return file, nil
}

func openRepositoryFileAt(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("open repository file")
	}
	return file, nil
}

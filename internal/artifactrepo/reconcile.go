package artifactrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/drobilica/tarlink/internal/locking"
	"golang.org/x/sys/unix"
)

const (
	StateUnchanged  = "unchanged"
	StateDownloaded = "downloaded"
	StateRepaired   = "repaired"
	StateMissing    = "missing"
	StateCorrupt    = "corrupt"
	StateRetained   = "retained"
)

type Status struct {
	Algorithm string   `json:"algorithm"`
	Digest    string   `json:"digest"`
	Size      int64    `json:"size"`
	URLs      []string `json:"urls"`
	State     string   `json:"state"`
}

// RetainedObject describes one stored recognized object outside the desired
// closure. Digest-valid extras carry StateRetained; digest-corrupt extras
// carry StateCorrupt. Both are storage inventory only: they never authorize
// installing releases absent from the validated registry snapshot.
type RetainedObject struct {
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	State     string `json:"state"`
}

type ReconcileResult struct {
	Statuses        []Status
	RetainedObjects []RetainedObject
	Required        int
	Downloaded      int
	Repaired        int
	Unchanged       int
	Removed         int
	Missing         int
	Corrupt         int
	Retained        int
	AvailableBytes  int64
}

func desiredKeys(plan *Plan) map[string]struct{} {
	keys := make(map[string]struct{}, len(plan.Desired))
	for _, object := range plan.Desired {
		keys[objectKey(object.Algorithm, object.Digest)] = struct{}{}
	}
	return keys
}

func listRecognizedKeys(root string) (map[string]struct{}, error) {
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
	result := map[string]struct{}{}
	for _, algorithm := range []string{"sha256", "sha512"} {
		directory, err := openRepositoryDirectoryAt(v1, algorithm)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries, readErr := directory.ReadDir(MaxRepositoryObjects + 1)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			directory.Close()
			return nil, readErr
		}
		if len(entries) > MaxRepositoryObjects {
			directory.Close()
			return nil, errors.New("repository contains too many objects")
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
				directory.Close()
				return nil, fmt.Errorf("unsafe repository object %q", entry.Name())
			}
			if _, err := objectPath("/", algorithm, entry.Name()); err != nil {
				directory.Close()
				return nil, fmt.Errorf("unsafe repository object %q: %w", entry.Name(), err)
			}
			file, err := openRepositoryFileAt(directory, entry.Name())
			if err != nil {
				directory.Close()
				return nil, fmt.Errorf("unsafe repository object %q: %w", entry.Name(), err)
			}
			info, statErr := file.Stat()
			closeErr := file.Close()
			if statErr != nil || !info.Mode().IsRegular() || !singleLink(info) {
				directory.Close()
				return nil, fmt.Errorf("unsafe repository object %q", entry.Name())
			}
			if closeErr != nil {
				directory.Close()
				return nil, closeErr
			}
			result[objectKey(algorithm, entry.Name())] = struct{}{}
		}
		directory.Close()
	}
	return result, nil
}

func objectSize(root, algorithm, digest string) int64 {
	path := filepath.Join(root, "v1", algorithm, digest)
	info, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return 0
	}
	return info.Size()
}

func classifyDesired(ctx context.Context, root string, plan *Plan) ([]Status, error) {
	statuses := make([]Status, 0, len(plan.Desired))
	for _, object := range plan.Desired {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		size, err := VerifyObjectContext(ctx, root, Object{Algorithm: object.Algorithm, Digest: object.Digest})
		if err == nil {
			statuses = append(statuses, Status{Algorithm: object.Algorithm, Digest: object.Digest, Size: size, URLs: append([]string(nil), object.URLs...), State: StateUnchanged})
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		target := filepath.Join(root, "v1", object.Algorithm, object.Digest)
		if _, statErr := os.Lstat(target); os.IsNotExist(statErr) {
			statuses = append(statuses, Status{Algorithm: object.Algorithm, Digest: object.Digest, Size: 0, URLs: append([]string(nil), object.URLs...), State: StateMissing})
			continue
		}
		statuses = append(statuses, Status{Algorithm: object.Algorithm, Digest: object.Digest, Size: objectSize(root, object.Algorithm, object.Digest), URLs: append([]string(nil), object.URLs...), State: StateCorrupt})
	}
	return statuses, nil
}

// inspectRetained hashes every stored recognized object outside the desired
// closure exactly once. Digest-valid extras are reported as retained; corrupt
// extras are reported with state corrupt so they are never hidden. Extras are
// never mutated here and never affect the required-set counters.
func inspectRetained(ctx context.Context, root string, desired map[string]struct{}) ([]RetainedObject, error) {
	existing, err := listRecognizedKeys(root)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(existing))
	for key := range existing {
		if _, ok := desired[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	retained := make([]RetainedObject, 0, len(keys))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		parts := splitKey(key)
		size, err := VerifyObjectContext(ctx, root, Object{Algorithm: parts[0], Digest: parts[1]})
		if err == nil {
			retained = append(retained, RetainedObject{Algorithm: parts[0], Digest: parts[1], Size: size, State: StateRetained})
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		retained = append(retained, RetainedObject{Algorithm: parts[0], Digest: parts[1], Size: objectSize(root, parts[0], parts[1]), State: StateCorrupt})
	}
	return retained, nil
}

func splitKey(key string) [2]string {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return [2]string{key[:i], key[i+1:]}
		}
	}
	return [2]string{"", key}
}

func bestEffortRetained(ctx context.Context, root string, plan *Plan) []RetainedObject {
	if plan == nil {
		return nil
	}
	retained, err := inspectRetained(ctx, root, desiredKeys(plan))
	if err != nil {
		return nil
	}
	return retained
}

func availableSpace(root string) int64 {
	var stat unix.Statfs_t
	if err := unix.Statfs(root, &stat); err != nil {
		return -1
	}
	return int64(stat.Bavail) * int64(stat.Bsize)
}

func isAbsentOrEmpty(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func InspectRead(ctx context.Context, root string, plan *Plan) ([]Status, []RetainedObject, error) {
	empty, err := isAbsentOrEmpty(root)
	if err != nil {
		return nil, nil, err
	}
	if empty {
		statuses := make([]Status, 0, len(plan.Desired))
		for _, object := range plan.Desired {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			statuses = append(statuses, Status{Algorithm: object.Algorithm, Digest: object.Digest, Size: 0, URLs: append([]string(nil), object.URLs...), State: StateMissing})
		}
		return statuses, []RetainedObject{}, nil
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	lock, err := locking.AcquireNoCreateWithTimeout(ctx, filepath.Join(absolute, ".sync.lock"), locking.DefaultTimeout)
	if err != nil {
		return nil, nil, err
	}
	defer lock.Release()
	statuses, err := classifyDesired(ctx, absolute, plan)
	if err != nil {
		return nil, nil, err
	}
	retained, err := inspectRetained(ctx, absolute, desiredKeys(plan))
	if err != nil {
		return nil, nil, err
	}
	return statuses, retained, nil
}

func VerifyAllCollect(ctx context.Context, root string) ([]Status, []Status, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	lock, err := locking.AcquireNoCreateWithTimeout(ctx, filepath.Join(absolute, ".sync.lock"), locking.DefaultTimeout)
	if err != nil {
		return nil, nil, err
	}
	defer lock.Release()
	if err := Open(absolute); err != nil {
		return nil, nil, err
	}
	existing, err := listRecognizedKeys(absolute)
	if err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(existing))
	for key := range existing {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var healthy []Status
	var corrupt []Status
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		parts := splitKey(key)
		size, err := VerifyObjectContext(ctx, absolute, Object{Algorithm: parts[0], Digest: parts[1]})
		if err == nil {
			healthy = append(healthy, Status{Algorithm: parts[0], Digest: parts[1], Size: size, State: StateUnchanged})
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
		corrupt = append(corrupt, Status{Algorithm: parts[0], Digest: parts[1], Size: objectSize(absolute, parts[0], parts[1]), State: StateCorrupt})
	}
	return healthy, corrupt, nil
}

func Reconcile(ctx context.Context, root string, plan *Plan, fetch Fetch) (ReconcileResult, error) {
	var result ReconcileResult
	if plan == nil || len(plan.Desired) == 0 {
		return result, errors.New("repository plan with a nonempty desired closure is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return result, err
	}
	if err := Init(absolute); err != nil {
		return result, err
	}
	lock, err := locking.AcquireExistingWithTimeout(ctx, filepath.Join(absolute, ".sync.lock"), locking.DefaultTimeout)
	if err != nil {
		return result, err
	}
	defer lock.Release()
	result.AvailableBytes = availableSpace(absolute)
	rootDirectory, err := openRepositoryDirectory(absolute)
	if err != nil {
		return result, fmt.Errorf("%w: open repository root: %v", ErrDestinationWrite, err)
	}
	if err := cleanupStaging(rootDirectory); err != nil {
		rootDirectory.Close()
		return result, fmt.Errorf("%w: clean interrupted object staging: %v", ErrDestinationWrite, err)
	}
	rootDirectory.Close()
	statuses, err := classifyDesired(ctx, absolute, plan)
	if err != nil {
		return result, err
	}
	result.Required = len(plan.Desired)
	var failures []error
	for i, status := range statuses {
		if status.State == StateUnchanged {
			continue
		}
		if err := ctx.Err(); err != nil {
			result.Statuses = statuses
			fillCounts(&result, statuses, bestEffortRetained(ctx, absolute, plan))
			return result, err
		}
		var objectURLs []string
		for _, object := range plan.Desired {
			if object.Algorithm == status.Algorithm && object.Digest == status.Digest {
				objectURLs = object.URLs
				break
			}
		}
		acquired := false
		var lastErr error
		for _, url := range objectURLs {
			if err := ctx.Err(); err != nil {
				result.Statuses = statuses
				fillCounts(&result, statuses, bestEffortRetained(ctx, absolute, plan))
				return result, err
			}
			addErr := Add(ctx, absolute, status.Algorithm, status.Digest, fetch, url)
			if addErr == nil {
				acquired = true
				break
			}
			if errors.Is(addErr, context.Canceled) || errors.Is(addErr, context.DeadlineExceeded) || errors.Is(addErr, ErrDestinationWrite) {
				result.Statuses = statuses
				fillCounts(&result, statuses, bestEffortRetained(ctx, absolute, plan))
				result.AvailableBytes = availableSpace(absolute)
				return result, addErr
			}
			lastErr = addErr
		}
		if !acquired {
			if lastErr != nil {
				failures = append(failures, fmt.Errorf("%s:%s: %w", status.Algorithm, status.Digest, lastErr))
			} else {
				failures = append(failures, fmt.Errorf("%s:%s: acquisition failed", status.Algorithm, status.Digest))
			}
			continue
		}
		size, verifyErr := VerifyObjectContext(ctx, absolute, Object{Algorithm: status.Algorithm, Digest: status.Digest})
		if verifyErr != nil {
			if errors.Is(verifyErr, context.Canceled) || errors.Is(verifyErr, context.DeadlineExceeded) {
				result.Statuses = statuses
				fillCounts(&result, statuses, bestEffortRetained(ctx, absolute, plan))
				return result, verifyErr
			}
			failures = append(failures, fmt.Errorf("%s:%s: %w", status.Algorithm, status.Digest, verifyErr))
			continue
		}
		if statuses[i].State == StateMissing {
			statuses[i].State = StateDownloaded
		} else {
			statuses[i].State = StateRepaired
		}
		statuses[i].Size = size
	}
	if len(failures) != 0 {
		retained, retainedErr := inspectRetained(ctx, absolute, desiredKeys(plan))
		if retainedErr != nil {
			failures = append(failures, retainedErr)
			retained = nil
		}
		result.Statuses = statuses
		fillCounts(&result, statuses, retained)
		result.AvailableBytes = availableSpace(absolute)
		return result, errors.Join(failures...)
	}
	for _, status := range statuses {
		if status.State == StateMissing || status.State == StateCorrupt {
			retained, retainedErr := inspectRetained(ctx, absolute, desiredKeys(plan))
			result.Statuses = statuses
			fillCounts(&result, statuses, retained)
			if retainedErr != nil {
				return result, errors.Join(fmt.Errorf("repository desired closure is incomplete"), retainedErr)
			}
			return result, fmt.Errorf("repository desired closure is incomplete")
		}
	}
	if err := ctx.Err(); err != nil {
		result.Statuses = statuses
		fillCounts(&result, statuses, bestEffortRetained(ctx, absolute, plan))
		return result, err
	}
	// Sync is additive-only: every valid stored object is preserved, including
	// objects absent from the current registry and objects outside a narrowed
	// selection. Retained bytes are storage only and never authorize
	// installing releases absent from the validated registry snapshot.
	retained, err := inspectRetained(ctx, absolute, desiredKeys(plan))
	if err != nil {
		result.Statuses = statuses
		fillCounts(&result, statuses, nil)
		return result, err
	}
	result.Statuses = statuses
	fillCounts(&result, statuses, retained)
	result.AvailableBytes = availableSpace(absolute)
	return result, nil
}

func fillCounts(result *ReconcileResult, statuses []Status, retained []RetainedObject) {
	result.Required = len(statuses)
	result.Downloaded, result.Repaired, result.Unchanged, result.Missing, result.Corrupt = 0, 0, 0, 0, 0
	for _, status := range statuses {
		switch status.State {
		case StateDownloaded:
			result.Downloaded++
		case StateRepaired:
			result.Repaired++
		case StateUnchanged:
			result.Unchanged++
		case StateMissing:
			result.Missing++
		case StateCorrupt:
			result.Corrupt++
		}
	}
	result.Removed = 0
	if retained == nil {
		result.RetainedObjects = nil
		result.Retained = 0
		return
	}
	result.RetainedObjects = retained
	result.Retained = 0
	for _, extra := range retained {
		if extra.State == StateRetained {
			result.Retained++
		}
	}
}

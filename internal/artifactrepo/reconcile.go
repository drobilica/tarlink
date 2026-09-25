package artifactrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/drobilica/tarlink/internal/locking"
	"golang.org/x/sys/unix"
)

const (
	StateUnchanged  = "unchanged"
	StateDownloaded = "downloaded"
	StateRepaired   = "repaired"
	StateMissing    = "missing"
	StateCorrupt    = "corrupt"

	OutcomeRemoved               = "removed"
	OutcomeEligible              = "eligible"
	OutcomePreservedProtected    = "preserved-protected"
	OutcomePreservedUnattributed = "preserved-unattributed"
	OutcomeFailed                = "failed"
)

type Status struct {
	Algorithm string   `json:"algorithm"`
	Digest    string   `json:"digest"`
	Size      int64    `json:"size"`
	URLs      []string `json:"urls"`
	State     string   `json:"state"`
}

type Removal struct {
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Outcome   string `json:"outcome"`
	Error     string `json:"error,omitempty"`
}

type ReconcileResult struct {
	Statuses       []Status
	Removals       []Removal
	Required       int
	Downloaded     int
	Repaired       int
	Unchanged      int
	Removed        int
	Missing        int
	Corrupt        int
	Excess         int
	Preserved      int
	AvailableBytes int64
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

func eligibleRemovals(plan *Plan, existing map[string]struct{}) (eligible []Object, preserved []Removal) {
	desired := desiredKeys(plan)
	keys := make([]string, 0)
	for key := range existing {
		if _, ok := desired[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	byKey := map[string]Object{}
	for _, object := range plan.Complete {
		byKey[objectKey(object.Algorithm, object.Digest)] = object
	}
	for _, key := range keys {
		var algorithm, digest string
		found := false
		for _, object := range plan.Complete {
			if objectKey(object.Algorithm, object.Digest) == key {
				algorithm, digest = object.Algorithm, object.Digest
				found = true
				break
			}
		}
		if !found {
			for _, object := range plan.Desired {
				if objectKey(object.Algorithm, object.Digest) == key {
					algorithm, digest = object.Algorithm, object.Digest
					found = true
					break
				}
			}
		}
		refs := plan.Protection[key]
		if !plan.Narrowed {
			if len(refs) == 0 {
				if !found {
					parts := splitKey(key)
					algorithm, digest = parts[0], parts[1]
				}
				eligible = append(eligible, Object{Algorithm: algorithm, Digest: digest})
			} else {
				preserved = append(preserved, Removal{Algorithm: algorithm, Digest: digest, Outcome: OutcomePreservedProtected})
			}
			continue
		}
		if len(refs) == 0 {
			if !found {
				parts := splitKey(key)
				algorithm, digest = parts[0], parts[1]
			}
			preserved = append(preserved, Removal{Algorithm: algorithm, Digest: digest, Outcome: OutcomePreservedUnattributed})
			continue
		}
		inside := true
		for scope := range refs {
			if _, ok := plan.SelectedScopes[scope]; !ok {
				inside = false
				break
			}
		}
		if inside {
			eligible = append(eligible, Object{Algorithm: algorithm, Digest: digest})
		} else {
			preserved = append(preserved, Removal{Algorithm: algorithm, Digest: digest, Outcome: OutcomePreservedProtected})
		}
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].Algorithm != eligible[j].Algorithm {
			return eligible[i].Algorithm < eligible[j].Algorithm
		}
		return eligible[i].Digest < eligible[j].Digest
	})
	sort.Slice(preserved, func(i, j int) bool {
		if preserved[i].Algorithm != preserved[j].Algorithm {
			return preserved[i].Algorithm < preserved[j].Algorithm
		}
		return preserved[i].Digest < preserved[j].Digest
	})
	_ = byKey
	return eligible, preserved
}

func splitKey(key string) [2]string {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return [2]string{key[:i], key[i+1:]}
		}
	}
	return [2]string{"", key}
}

func removeVerified(root, algorithm, digest string) (size int64, removed bool, err error) {
	if _, err := objectPath(root, algorithm, digest); err != nil {
		return 0, false, err
	}
	rootDirectory, err := openRepositoryDirectory(root)
	if err != nil {
		return 0, false, err
	}
	defer rootDirectory.Close()
	v1, err := openRepositoryDirectoryAt(rootDirectory, "v1")
	if err != nil {
		return 0, false, err
	}
	defer v1.Close()
	objects, err := openRepositoryDirectoryAt(v1, algorithm)
	if err != nil {
		return 0, false, err
	}
	defer objects.Close()
	file, err := openRepositoryFileAt(objects, digest)
	if err != nil {
		return 0, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !singleLink(info) {
		return 0, false, errors.New("repository object is not regular")
	}
	size = info.Size()
	var openedStat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &openedStat); err != nil {
		return 0, false, errors.New("repository object identity is unavailable")
	}
	var namedStat unix.Stat_t
	if err := unix.Fstatat(int(objects.Fd()), digest, &namedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, false, fmt.Errorf("repository object changed during removal: %w", err)
	}
	if openedStat.Dev != namedStat.Dev || openedStat.Ino != namedStat.Ino || namedStat.Nlink != 1 || (namedStat.Mode&syscall.S_IFMT) != syscall.S_IFREG {
		return 0, false, errors.New("repository object changed during removal")
	}
	stagePath, err := os.MkdirTemp(root, ".object-stage-*")
	if err != nil {
		return 0, false, fmt.Errorf("%w: create removal staging: %v", ErrDestinationWrite, err)
	}
	stage, err := openRepositoryDirectory(stagePath)
	if err != nil {
		_ = unix.Unlinkat(int(rootDirectory.Fd()), filepath.Base(stagePath), unix.AT_REMOVEDIR)
		return 0, false, err
	}
	keepStaged := false
	defer func() {
		if !keepStaged {
			_ = unix.Unlinkat(int(stage.Fd()), "object", 0)
			_ = unix.Unlinkat(int(rootDirectory.Fd()), filepath.Base(stagePath), unix.AT_REMOVEDIR)
		}
		_ = stage.Close()
	}()
	if err := unix.Renameat(int(objects.Fd()), digest, int(stage.Fd()), "object"); err != nil {
		return 0, false, fmt.Errorf("quarantine repository object: %w", err)
	}
	var stagedStat unix.Stat_t
	if err := unix.Fstatat(int(stage.Fd()), "object", &stagedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || stagedStat.Dev != openedStat.Dev || stagedStat.Ino != openedStat.Ino || (stagedStat.Mode&syscall.S_IFMT) != syscall.S_IFREG {
		if errno := unix.Renameat(int(stage.Fd()), "object", int(objects.Fd()), digest); errno != nil {
			keepStaged = true
			return 0, false, fmt.Errorf("repository object changed during removal and could not be restored: %v", errno)
		}
		return 0, false, errors.New("repository object changed during removal")
	}
	if err := unix.Unlinkat(int(stage.Fd()), "object", 0); err != nil {
		return 0, false, fmt.Errorf("remove quarantined repository object: %w", err)
	}
	var afterStat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &afterStat); err != nil || afterStat.Nlink != 0 {
		return 0, false, errors.New("repository object changed during removal")
	}
	removed = true
	if err := objects.Sync(); err != nil {
		return size, removed, err
	}
	if err := stage.Sync(); err != nil {
		return size, removed, err
	}
	return size, removed, nil
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

func InspectRead(ctx context.Context, root string, plan *Plan) ([]Status, []Object, []Removal, error) {
	empty, err := isAbsentOrEmpty(root)
	if err != nil {
		return nil, nil, nil, err
	}
	if empty {
		statuses := make([]Status, 0, len(plan.Desired))
		for _, object := range plan.Desired {
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, err
			}
			statuses = append(statuses, Status{Algorithm: object.Algorithm, Digest: object.Digest, Size: 0, URLs: append([]string(nil), object.URLs...), State: StateMissing})
		}
		eligible, preserved := eligibleRemovals(plan, map[string]struct{}{})
		return statuses, eligible, preserved, nil
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, nil, err
	}
	lock, err := locking.AcquireNoCreateWithTimeout(ctx, filepath.Join(absolute, ".sync.lock"), locking.DefaultTimeout)
	if err != nil {
		return nil, nil, nil, err
	}
	defer lock.Release()
	statuses, err := classifyDesired(ctx, absolute, plan)
	if err != nil {
		return nil, nil, nil, err
	}
	existing, err := listRecognizedKeys(absolute)
	if err != nil {
		return nil, nil, nil, err
	}
	eligible, preserved := eligibleRemovals(plan, existing)
	for i := range preserved {
		preserved[i].Size = objectSize(absolute, preserved[i].Algorithm, preserved[i].Digest)
	}
	return statuses, eligible, preserved, nil
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
	existing, err := listRecognizedKeys(absolute)
	if err != nil {
		return result, err
	}
	eligible, preserved := eligibleRemovals(plan, existing)
	for i := range preserved {
		preserved[i].Size = objectSize(absolute, preserved[i].Algorithm, preserved[i].Digest)
	}
	result.Required = len(plan.Desired)
	byDigest := map[string]int{}
	for i, status := range statuses {
		byDigest[objectKey(status.Algorithm, status.Digest)] = i
	}
	var failures []error
	for i, status := range statuses {
		if status.State == StateUnchanged {
			continue
		}
		if err := ctx.Err(); err != nil {
			fillCounts(&result, statuses, eligible, preserved)
			result.Statuses = statuses
			result.Removals = preserved
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
				fillCounts(&result, statuses, eligible, preserved)
				result.Statuses = statuses
				result.Removals = preserved
				return result, err
			}
			addErr := Add(ctx, absolute, status.Algorithm, status.Digest, fetch, url)
			if addErr == nil {
				acquired = true
				break
			}
			if errors.Is(addErr, context.Canceled) || errors.Is(addErr, context.DeadlineExceeded) || errors.Is(addErr, ErrDestinationWrite) {
				fillCounts(&result, statuses, eligible, preserved)
				result.Statuses = statuses
				result.Removals = preserved
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
				fillCounts(&result, statuses, eligible, preserved)
				result.Statuses = statuses
				result.Removals = preserved
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
		_ = byDigest
	}
	if len(failures) != 0 {
		fillCounts(&result, statuses, eligible, preserved)
		result.Statuses = statuses
		result.Removals = preserved
		result.AvailableBytes = availableSpace(absolute)
		return result, errors.Join(failures...)
	}
	for _, status := range statuses {
		if status.State == StateMissing || status.State == StateCorrupt {
			fillCounts(&result, statuses, eligible, preserved)
			result.Statuses = statuses
			result.Removals = preserved
			return result, fmt.Errorf("repository desired closure is incomplete")
		}
	}
	if err := ctx.Err(); err != nil {
		fillCounts(&result, statuses, eligible, preserved)
		result.Statuses = statuses
		result.Removals = preserved
		return result, err
	}
	var removals []Removal
	removed := 0
	for _, object := range eligible {
		if err := ctx.Err(); err != nil {
			remaining := remainingRemovals(eligible[removed:], absolute)
			removals = append(removals, remaining...)
			fillRemovalSizes(absolute, removals)
			result.Removals = append(removals, preserved...)
			result.Statuses = statuses
			fillCounts(&result, statuses, eligible, preserved)
			result.Removed = removed
			return result, err
		}
		size, didRemove, err := removeVerified(absolute, object.Algorithm, object.Digest)
		if didRemove {
			removals = append(removals, Removal{Algorithm: object.Algorithm, Digest: object.Digest, Size: size, Outcome: OutcomeRemoved})
			removed++
		}
		if err != nil {
			remaining := remainingRemovals(eligible[removed:], absolute)
			if !didRemove {
				for i := range remaining {
					if remaining[i].Algorithm == object.Algorithm && remaining[i].Digest == object.Digest {
						remaining[i].Outcome = OutcomeFailed
					}
				}
			}
			removals = append(removals, remaining...)
			fillRemovalSizes(absolute, removals)
			result.Removals = append(removals, preserved...)
			result.Statuses = statuses
			fillCounts(&result, statuses, eligible, preserved)
			result.Removed = removed
			result.AvailableBytes = availableSpace(absolute)
			return result, fmt.Errorf("repository cleanup failed for %s:%s: %w", object.Algorithm, object.Digest, err)
		}
	}
	result.Removals = append(removals, preserved...)
	sort.Slice(result.Removals, func(i, j int) bool {
		if result.Removals[i].Algorithm != result.Removals[j].Algorithm {
			return result.Removals[i].Algorithm < result.Removals[j].Algorithm
		}
		return result.Removals[i].Digest < result.Removals[j].Digest
	})
	result.Statuses = statuses
	fillCounts(&result, statuses, eligible, preserved)
	result.Removed = removed
	result.AvailableBytes = availableSpace(absolute)
	return result, nil
}

func remainingRemovals(objects []Object, root string) []Removal {
	removals := make([]Removal, 0, len(objects))
	for _, object := range objects {
		removals = append(removals, Removal{Algorithm: object.Algorithm, Digest: object.Digest, Size: objectSize(root, object.Algorithm, object.Digest), Outcome: OutcomeFailed})
	}
	return removals
}

func fillRemovalSizes(root string, removals []Removal) {
	for i := range removals {
		if removals[i].Size == 0 {
			removals[i].Size = objectSize(root, removals[i].Algorithm, removals[i].Digest)
		}
	}
}

func fillCounts(result *ReconcileResult, statuses []Status, eligible []Object, preserved []Removal) {
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
	result.Excess = len(eligible)
	result.Preserved = len(preserved)
}

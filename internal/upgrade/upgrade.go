// Package upgrade discovers and safely installs official TarLink releases.
package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
)

const (
	officialLatestURL      = "https://github.com/drobilica/tarlink/releases/latest"
	officialReleaseBaseURL = "https://github.com/drobilica/tarlink/releases/download/v"
	cacheMaxAge            = 24 * time.Hour
	upgradeCacheMaxAge     = 5 * time.Minute
	maxBinary              = 256 << 20
)

var stableVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

var (
	ErrDevelopment      = errors.New("development builds cannot self-upgrade")
	ErrNotOwned         = errors.New("TarLink installation is not owned by TarLink")
	ErrNoRelease        = errors.New("no stable TarLink release is available")
	ErrUnsupportedAsset = errors.New("release has no supported binary asset")
)

type Version struct {
	Current string
	Latest  string
}

func IsNewer(current, latest string) bool {
	if !stableVersion.MatchString("v"+normalizeVersion(current)) || !stableVersion.MatchString("v"+normalizeVersion(latest)) {
		return false
	}
	return compare(normalizeVersion(latest), normalizeVersion(current)) > 0
}

type Progress func(phase string, done, total int64)

type Service struct {
	Layout        filesystem.Layout
	Client        *download.Client
	Current       string
	GOOS          string
	GOARCH        string
	Now           func() time.Time
	Executable    func() (string, error)
	syncDirectory func(string) error
	// These hooks are intentionally unexported: production callers cannot
	// redirect self-upgrade traffic away from the official GitHub endpoints.
	// Tests in this package use them with an isolated HTTP test server.
	testAPIURL         string
	testReleaseBaseURL string
}

func (s *Service) Check(ctx context.Context) (Version, error) {
	current := strings.TrimPrefix(strings.TrimSpace(s.Current), "v")
	if current == "" || current == "development" {
		current = "development"
	}
	result := Version{Current: current}
	if current == "development" || !stableVersion.MatchString("v"+current) {
		if current != "development" {
			current = "development"
			result.Current = current
		}
		return result, ErrDevelopment
	}
	cachePath := filepath.Join(s.Layout.Cache, "update-check.json")
	if cached, ok := readCache(cachePath, s.clock()); ok {
		result.Latest = cached.Latest
		return result, nil
	}
	latest, err := s.fetchLatest(ctx)
	if err != nil {
		return result, err
	}
	result.Latest = latest
	_ = writeCache(cachePath, latest, s.clock())
	return result, nil
}

// CheckFresh uses the normal cache age check, while a stale usable cache keeps
// the TUI notification available when the network is down.
func (s *Service) CheckFresh(ctx context.Context) (Version, error) {
	current := strings.TrimPrefix(strings.TrimSpace(s.Current), "v")
	if current == "" || current == "development" {
		current = "development"
	}
	result := Version{Current: current}
	if current == "development" || !stableVersion.MatchString("v"+current) {
		if current != "development" {
			result.Current = "development"
		}
		return result, ErrDevelopment
	}

	cachePath := filepath.Join(s.Layout.Cache, "update-check.json")
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return result, err
		}
	}
	if cached, ok := readCache(cachePath, s.clock()); ok {
		result.Latest = cached.Latest
		return result, nil
	}
	latest, err := s.fetchLatest(ctx)
	if err == nil {
		result.Latest = latest
		_ = writeCache(cachePath, latest, s.clock())
		return result, nil
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
	}
	if cached, ok := readCacheAny(cachePath, s.clock()); ok {
		result.Latest = cached.Latest
		return result, nil
	}
	return result, err
}

func (s *Service) Upgrade(ctx context.Context, progress Progress) (Version, error) {
	if !stableVersion.MatchString("v" + strings.TrimPrefix(s.Current, "v")) {
		return Version{Current: s.Current}, ErrDevelopment
	}
	current := normalizeVersion(s.Current)
	cachePath := filepath.Join(s.Layout.Cache, "update-check.json")
	if cached, ok := readCache(cachePath, s.clock()); ok && s.clock().Sub(cached.CheckedAt) <= upgradeCacheMaxAge && compare(current, cached.Latest) >= 0 {
		return Version{Current: current, Latest: cached.Latest}, nil
	}
	latest, err := s.fetchLatest(ctx)
	if err != nil {
		return Version{Current: current}, err
	}
	_ = writeCache(cachePath, latest, s.clock())
	if compare(current, latest) >= 0 {
		return Version{Current: current, Latest: latest}, nil
	}
	if err := s.replace(ctx, latest, progress); err != nil {
		return Version{Current: current, Latest: latest}, err
	}
	return Version{Current: current, Latest: latest}, nil
}

func (s *Service) fetchLatest(ctx context.Context) (string, error) {
	latestURL := officialLatestURL
	if s.testAPIURL != "" {
		latestURL = s.testAPIURL
	}
	parsed, err := url.Parse(latestURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (s.testAPIURL == "" && !officialURL(latestURL, officialLatestURL)) {
		return "", errors.New("invalid official latest release URL")
	}
	if s.Client == nil {
		s.Client = download.NewClient()
	}
	if s.Client.HTTP == nil {
		return "", errors.New("download client is not configured")
	}
	httpClient := *s.Client.HTTP
	redirects := 0
	var redirectTarget *url.URL
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		redirects++
		if redirects > 1 || len(via) > 1 || !discoveryURL(req.URL, parsed.Host) {
			return errors.New("unexpected latest release redirect")
		}
		copy := *req.URL
		redirectTarget = &copy
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create latest release request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("latest release discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("latest release discovery: HTTP %d", resp.StatusCode)
	}
	if redirectTarget == nil || !discoveryURL(redirectTarget, parsed.Host) {
		return "", errors.New("invalid latest release URL")
	}
	path := redirectTarget.Path
	prefix := "/drobilica/tarlink/releases/tag/"
	if !strings.HasPrefix(path, prefix) || !stableVersion.MatchString(strings.TrimPrefix(path, prefix)) || redirectTarget.RawPath != "" || redirectTarget.RawQuery != "" || redirectTarget.Fragment != "" {
		return "", errors.New("latest release did not resolve to a stable TarLink release")
	}
	return normalizeVersion(strings.TrimPrefix(path, prefix)), nil
}

func discoveryURL(value *url.URL, host string) bool {
	return value != nil && value.Scheme == "https" && value.Host == host && value.User == nil && value.RawQuery == "" && value.Fragment == "" && value.Opaque == "" && value.Path != ""
}

func (s *Service) replace(ctx context.Context, latest string, progress Progress) error {
	if s.GOOS == "" {
		s.GOOS = runtime.GOOS
	}
	if s.GOARCH == "" {
		s.GOARCH = runtime.GOARCH
	}
	if s.GOOS != "linux" || s.GOARCH != "amd64" && s.GOARCH != "arm64" {
		return ErrUnsupportedAsset
	}
	if err := s.verifyInstallation(); err != nil {
		return err
	}
	base := officialReleaseBaseURL
	if s.testReleaseBaseURL != "" {
		base = s.testReleaseBaseURL
	}
	if s.testReleaseBaseURL == "" && !officialURL(base, officialReleaseBaseURL) {
		return errors.New("invalid official release URL")
	}
	base += latest
	assetName := "tarlink-linux-" + s.GOARCH
	cache := filepath.Join(s.Layout.Cache, "upgrade")
	checksums := filepath.Join(cache, latest+"-checksums.txt")
	if s.Client == nil {
		s.Client = download.NewClient()
	}
	if _, err := s.Client.FetchFile(ctx, download.FileRequest{URL: base + "/checksums.txt", Destination: checksums, MaxBytes: 1 << 20}); err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	digest, err := checksumFor(checksums, assetName)
	if err != nil {
		return err
	}
	binaryCache := filepath.Join(cache, latest+"-"+assetName)
	if _, err := s.Client.FetchArtifact(ctx, download.ArtifactRequest{URL: base + "/" + assetName, Algorithm: "sha256", Digest: digest, Destination: binaryCache, MaxBytes: maxBinary, ReportProgress: func(done, total int64) { report(progress, "downloading", done, total) }}); err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	report(progress, "verifying", 0, 0)
	return s.atomicReplace(binaryCache, digest, progress)
}

// officialURL requires the exact HTTPS origin and path prefix used by the
// TarLink release contract. It is deliberately strict so a future edit cannot
// accidentally make self-upgrade configurable or permit an attacker-owned
// endpoint.
func officialURL(value, expected string) bool {
	got, err := url.Parse(value)
	want, wantErr := url.Parse(expected)
	if err != nil || wantErr != nil || got.Scheme != "https" || got.Host != want.Host || got.User != nil || got.Path != want.Path || got.RawQuery != "" || got.Fragment != "" {
		return false
	}
	return true
}

func (s *Service) verifyInstallation() error {
	if s.Executable == nil {
		s.Executable = os.Executable
	}
	executable, err := s.Executable()
	if err != nil {
		return ErrNotOwned
	}
	executable, err = filepath.Abs(executable)
	if err != nil || filepath.Clean(executable) != filepath.Join(s.Layout.Bin, "tarlink") {
		return ErrNotOwned
	}
	if err := filesystem.CheckOwnedDirectoryWithin(s.Layout.Home, s.Layout.Bin); err != nil {
		return ErrNotOwned
	}
	markerDir := filepath.Join(s.Layout.StateHome, "tarlink")
	if err := filesystem.CheckOwnedDirectoryWithin(s.Layout.Home, markerDir); err != nil {
		return ErrNotOwned
	}
	target := filepath.Join(s.Layout.Bin, "tarlink")
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return ErrNotOwned
	}
	marker := filepath.Join(markerDir, "install.sha256")
	markerInfo, err := os.Lstat(marker)
	if err != nil || markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
		return ErrNotOwned
	}
	data, err := os.ReadFile(marker)
	if err != nil || len(data) != 65 || data[64] != '\n' || !hexDigest(string(data[:64])) {
		return ErrNotOwned
	}
	hash, err := fileDigest(target)
	if err != nil || hash != string(data[:64]) {
		return ErrNotOwned
	}
	return nil
}

func (s *Service) atomicReplace(source, digest string, progress Progress) error {
	target := filepath.Join(s.Layout.Bin, "tarlink")
	marker := filepath.Join(s.Layout.StateHome, "tarlink", "install.sha256")
	oldMarker, err := os.ReadFile(marker)
	if err != nil {
		return fmt.Errorf("read ownership marker: %w", err)
	}
	tmp, err := os.CreateTemp(s.Layout.Bin, ".tarlink-upgrade-*")
	if err != nil {
		return fmt.Errorf("create replacement: %w", err)
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, hasher), input)
	closeErr := input.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(hasher.Sum(nil)) != digest {
		return download.ErrChecksumMismatch
	}
	if err := tmp.Chmod(0755); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	backup, err := os.CreateTemp(s.Layout.Bin, ".tarlink-old-*")
	if err != nil {
		return err
	}
	backupName := backup.Name()
	backupMoved := false
	defer func() {
		if !backupMoved {
			_ = os.Remove(backupName)
		}
	}()
	if err := backup.Close(); err != nil {
		return err
	}
	if err := os.Remove(backupName); err != nil {
		return err
	}
	if err := s.verifyInstallation(); err != nil {
		return err
	}
	if err := os.Rename(target, backupName); err != nil {
		return fmt.Errorf("stage current binary: %w", err)
	}
	backupMoved = true
	restored := false
	defer func() {
		if !restored {
			_ = os.Remove(backupName)
		}
	}()
	restoreBinary := func() error {
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(backupName, target); err != nil {
			return err
		}
		backupMoved = false
		restored = true
		return nil
	}
	failBeforeCommit := func(operationErr error) error {
		if rollbackErr := restoreBinary(); rollbackErr != nil {
			return errors.Join(operationErr, fmt.Errorf("rollback upgrade: %w", rollbackErr))
		}
		return operationErr
	}
	if err := os.Rename(tmpName, target); err != nil {
		return failBeforeCommit(fmt.Errorf("replace binary: %w", err))
	}
	markerTmp, err := os.CreateTemp(filepath.Dir(marker), ".install.sha256-*")
	if err != nil {
		return failBeforeCommit(err)
	}
	markerTmpName := markerTmp.Name()
	markerOK := false
	defer func() {
		_ = markerTmp.Close()
		if !markerOK {
			_ = os.Remove(markerTmpName)
		}
	}()
	if err := markerTmp.Chmod(0600); err != nil {
		return failBeforeCommit(err)
	}
	if _, err := markerTmp.WriteString(digest + "\n"); err != nil {
		return failBeforeCommit(err)
	}
	if err := markerTmp.Sync(); err != nil {
		return failBeforeCommit(err)
	}
	if err := markerTmp.Close(); err != nil {
		return failBeforeCommit(err)
	}
	if err := os.Rename(markerTmpName, marker); err != nil {
		return failBeforeCommit(fmt.Errorf("update ownership marker: %w", err))
	}
	markerOK = true
	report(progress, "installing", 0, 0)
	syncDirectory := s.syncDirectory
	if syncDirectory == nil {
		syncDirectory = syncDir
	}
	rollback := func() error {
		var rollbackErrors []error
		if err := restoreMarker(marker, oldMarker); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore ownership marker: %w", err))
		}
		if err := restoreBinary(); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore binary: %w", err))
		}
		if err := syncDirectory(s.Layout.Bin); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("sync binary directory: %w", err))
		}
		if err := syncDirectory(filepath.Dir(marker)); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("sync marker directory: %w", err))
		}
		return errors.Join(rollbackErrors...)
	}
	if err := syncDirectory(s.Layout.Bin); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback upgrade: %w", rollbackErr))
		}
		return err
	}
	if err := syncDirectory(filepath.Dir(marker)); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback upgrade: %w", rollbackErr))
		}
		return err
	}
	_ = os.Remove(backupName)
	backupMoved = false
	restored = true
	keep = true
	report(progress, "complete", 0, 0)
	return nil
}

func restoreMarker(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".install.sha256-rollback-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func checksumFor(path, name string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	found := ""
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 || !hexDigest(fields[0]) || strings.HasPrefix(fields[1], "*") || fields[1] == "" {
			return "", errors.New("malformed checksums.txt")
		}
		if fields[1] == name {
			if found != "" {
				return "", errors.New("duplicate binary checksum")
			}
			found = fields[0]
		}
	}
	if found == "" {
		return "", ErrUnsupportedAsset
	}
	return found, nil
}

func (s *Service) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type cacheFile struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest_version"`
}

func readCache(path string, now time.Time) (cacheFile, bool) {
	value, ok := readCacheAny(path, now)
	if !ok || now.Sub(value.CheckedAt) >= cacheMaxAge {
		return cacheFile{}, false
	}
	return value, true
}

func readCacheAny(path string, now time.Time) (cacheFile, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheFile{}, false
	}
	var value cacheFile
	if json.Unmarshal(data, &value) != nil || !stableVersion.MatchString("v"+value.Latest) || now.Sub(value.CheckedAt) < 0 {
		return cacheFile{}, false
	}
	return value, true
}
func writeCache(path, latest string, now time.Time) error {
	if err := filesystem.SecureMkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, _ := json.Marshal(cacheFile{CheckedAt: now, Latest: latest})
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-check-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
func normalizeVersion(value string) string { return strings.TrimPrefix(value, "v") }
func compare(a, b string) int {
	pa := parts(a)
	pb := parts(b)
	for i := range pa {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}
func parts(value string) [3]int {
	m := stableVersion.FindStringSubmatch("v" + normalizeVersion(value))
	var out [3]int
	if len(m) == 4 {
		for i := 0; i < 3; i++ {
			out[i], _ = strconv.Atoi(m[i+1])
		}
	}
	return out
}
func hexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}
func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func report(progress Progress, phase string, done, total int64) {
	if progress != nil {
		progress(phase, done, total)
	}
}
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func sameHost(raw string) download.URLPolicy {
	parsed, _ := url.Parse(raw)
	return func(value *url.URL) bool { return value.Host == parsed.Host && value.Scheme == "https" }
}

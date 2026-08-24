package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
)

func TestStableVersionComparison(t *testing.T) {
	if !stableVersion.MatchString("v1.2.3") || stableVersion.MatchString("v1.2.3-rc.1") || stableVersion.MatchString("v01.2.3") {
		t.Fatal("stable release parser accepted an invalid tag")
	}
	if compare("1.10.0", "1.9.9") <= 0 || compare("1.0.0", "1.0.0") != 0 || compare("1.0.0", "1.0.1") >= 0 {
		t.Fatal("semantic version comparison is incorrect")
	}
}

func TestLatestReleaseRedirectValidation(t *testing.T) {
	cases := []struct {
		name       string
		location   string
		statusCode int
		want       string
	}{
		{name: "valid", location: "/drobilica/tarlink/releases/tag/v2.3.4", statusCode: http.StatusFound, want: "2.3.4"},
		{name: "wrong host", location: "https://evil.example/releases/tag/v2.3.4"},
		{name: "wrong repository", location: "/other-owner/other-repo/releases/tag/v2.3.4"},
		{name: "invalid scheme", location: "http://github.com/drobilica/tarlink/releases/tag/v2.3.4"},
		{name: "malformed version", location: "/releases/tag/v2.3.4-rc.1"},
		{name: "query", location: "/releases/tag/v2.3.4?download=1"},
		{name: "fragment", location: "/releases/tag/v2.3.4#assets"},
		{name: "userinfo", location: "https://attacker@github.com/drobilica/tarlink/releases/tag/v2.3.4"},
		{name: "missing redirect", statusCode: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/latest" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if tc.statusCode == 0 {
					http.Redirect(w, r, tc.location, http.StatusFound)
					return
				}
				if tc.statusCode == http.StatusFound {
					http.Redirect(w, r, tc.location, tc.statusCode)
					return
				}
				w.WriteHeader(tc.statusCode)
			}))
			defer server.Close()
			service := &Service{Client: &download.Client{HTTP: server.Client()}, testAPIURL: server.URL + "/latest", Current: "1.0.0"}
			got, err := service.fetchLatest(context.Background())
			if tc.want != "" {
				if err != nil || got != tc.want {
					t.Fatalf("latest=%q err=%v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("unsafe redirect accepted: latest=%q", got)
			}
		})
	}
}

func TestChecksumForRejectsMalformedAndSelectsExactAsset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(path, []byte("bad\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := checksumFor(path, "tarlink-linux-amd64"); err == nil {
		t.Fatal("malformed checksums were accepted")
	}
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(path, []byte(digest+"  tarlink-linux-amd64\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := checksumFor(path, "tarlink-linux-amd64")
	if err != nil || got != digest {
		t.Fatalf("checksum=%q err=%v", got, err)
	}
}

func TestUpdateCacheFreshnessAndCorruption(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".upgrade-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "update-check.json")
	now := time.Unix(1000, 0)
	if err := writeCache(path, "1.2.3", now); err != nil {
		t.Fatal(err)
	}
	if value, ok := readCache(path, now.Add(time.Hour)); !ok || value.Latest != "1.2.3" {
		t.Fatal("fresh cache was not used")
	}
	if _, ok := readCache(path, now.Add(25*time.Hour)); ok {
		t.Fatal("stale cache was used")
	}
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readCache(path, now); ok {
		t.Fatal("corrupt cache was used")
	}
}

func TestCheckFreshUsesFreshCacheAndFallsBackOffline(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	service := &Service{Layout: filesystem.Layout{Cache: dir}, Current: "1.0.0", GOARCH: "amd64", Now: func() time.Time { return now }}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		http.Redirect(w, r, "/drobilica/tarlink/releases/tag/v2.0.0", http.StatusFound)
	}))
	defer server.Close()
	service.Client = &download.Client{HTTP: server.Client()}
	service.testAPIURL = server.URL + "/releases"
	if err := writeCache(filepath.Join(dir, "update-check.json"), "1.1.0", now); err != nil {
		t.Fatal(err)
	}

	value, err := service.CheckFresh(context.Background())
	if err != nil || value.Latest != "1.1.0" || requests != 0 {
		t.Fatalf("fresh check value=%#v err=%v requests=%d", value, err, requests)
	}
	now = now.Add(25 * time.Hour)
	value, err = service.CheckFresh(context.Background())
	if err != nil || value.Latest != "1.1.0" || requests != 1 {
		t.Fatalf("offline fallback value=%#v err=%v requests=%d", value, err, requests)
	}
}

func TestCheckFreshHonorsCancellationBeforeCacheFallback(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	service := &Service{Layout: filesystem.Layout{Cache: dir}, Current: "1.0.0", testAPIURL: "https://127.0.0.1/releases", Now: func() time.Time { return now }}
	if err := writeCache(filepath.Join(dir, "update-check.json"), "2.0.0", now); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.CheckFresh(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}

func TestUpgradeUsesRecentCurrentCacheWithoutDiscovery(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	service := &Service{Layout: filesystem.Layout{Cache: dir}, Current: "2.0.0", Now: func() time.Time { return now }}
	if err := writeCache(filepath.Join(dir, "update-check.json"), "2.0.0", now); err != nil {
		t.Fatal(err)
	}
	value, err := service.Upgrade(context.Background(), nil)
	if err != nil || value.Latest != "2.0.0" {
		t.Fatalf("value=%#v err=%v", value, err)
	}
}

func TestUpgradeRefreshesCachedNewerVersionBeforeInstallation(t *testing.T) {
	service, target, _, old := ownedService(t)
	now := time.Unix(1000, 0)
	service.Now = func() time.Time { return now }
	if err := writeCache(filepath.Join(service.Layout.Cache, "update-check.json"), "2.0.0", now); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/drobilica/tarlink/releases/tag/v1.0.0" {
			w.WriteHeader(http.StatusOK)
			return
		}
		requests++
		http.Redirect(w, r, "/drobilica/tarlink/releases/tag/v1.0.0", http.StatusFound)
	}))
	defer server.Close()
	service.Client = &download.Client{HTTP: server.Client()}
	service.testAPIURL = server.URL + "/latest"
	value, err := service.Upgrade(context.Background(), nil)
	if err != nil || value.Latest != "1.0.0" || requests != 1 {
		t.Fatalf("value=%#v err=%v requests=%d", value, err, requests)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || !bytes.Equal(got, old) {
		t.Fatalf("binary changed: %q err=%v", got, readErr)
	}
}

func TestUpgradeDiscoveryFailureDoesNotInstallCachedNewerVersion(t *testing.T) {
	service, target, _, old := ownedService(t)
	now := time.Unix(1000, 0)
	service.Now = func() time.Time { return now }
	if err := writeCache(filepath.Join(service.Layout.Cache, "update-check.json"), "2.0.0", now); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	service.Client = &download.Client{HTTP: server.Client()}
	service.testAPIURL = server.URL + "/latest"
	if _, err := service.Upgrade(context.Background(), nil); err == nil {
		t.Fatal("discovery failure unexpectedly succeeded")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || !bytes.Equal(got, old) {
		t.Fatalf("binary changed after discovery failure: %q err=%v", got, readErr)
	}
}

func TestAtomicReplaceCommitsOnlyAfterBothDirectorySyncs(t *testing.T) {
	dir := t.TempDir()
	bin, state := filepath.Join(dir, "bin"), filepath.Join(dir, "state", "tarlink")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(bin, "tarlink")
	old := []byte("old binary")
	if err := os.WriteFile(target, old, 0755); err != nil {
		t.Fatal(err)
	}
	oldDigest := digestBytes(old)
	marker := filepath.Join(state, "install.sha256")
	if err := os.WriteFile(marker, []byte(oldDigest+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "new")
	newBytes := []byte("new binary")
	if err := os.WriteFile(source, newBytes, 0600); err != nil {
		t.Fatal(err)
	}
	l := filesystem.Layout{Home: dir, Bin: bin, StateHome: filepath.Join(dir, "state"), Cache: filepath.Join(dir, "cache")}
	service := &Service{Layout: l, Executable: func() (string, error) { return target, nil }, syncDirectory: func(string) error { return nil }}
	if err := service.atomicReplace(source, digestBytes(newBytes), nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(newBytes) {
		t.Fatalf("target=%q", got)
	}
	got, _ = os.ReadFile(marker)
	if string(got) != digestBytes(newBytes)+"\n" {
		t.Fatalf("marker=%q", got)
	}

	if err := os.WriteFile(target, old, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(oldDigest+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	callCount := 0
	service.syncDirectory = func(string) error {
		callCount++
		if callCount == 2 {
			return errors.New("sync failed")
		}
		return nil
	}
	if err := service.atomicReplace(source, digestBytes(newBytes), nil); err == nil {
		t.Fatal("sync failure succeeded")
	}
	got, _ = os.ReadFile(target)
	if string(got) != string(old) {
		t.Fatalf("rollback target=%q", got)
	}
	got, _ = os.ReadFile(marker)
	if string(got) != oldDigest+"\n" {
		t.Fatalf("rollback marker=%q", got)
	}
	entries, _ := os.ReadDir(bin)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tarlink-") {
			t.Fatalf("temporary file remains: %s", entry.Name())
		}
	}
}

func digestBytes(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func ownedService(t *testing.T) (*Service, string, string, []byte) {
	t.Helper()
	home, err := os.MkdirTemp(".", ".upgrade-owned-")
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	bin := filepath.Join(home, ".local", "bin")
	markerDir := filepath.Join(home, ".local", "state", "tarlink")
	if err := os.MkdirAll(markerDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(bin, "tarlink")
	content := []byte("owned")
	if err := os.WriteFile(target, content, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "install.sha256"), []byte(digestBytes(content)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	layout := filesystem.Layout{Home: home, Bin: bin, StateHome: filepath.Join(home, ".local", "state"), Cache: filepath.Join(home, ".cache", "tarlink")}
	service := &Service{Layout: layout, Current: "1.0.0", Executable: func() (string, error) { return target, nil }}
	return service, target, filepath.Join(markerDir, "install.sha256"), content
}

func TestVerifyInstallationOwnershipBoundaries(t *testing.T) {
	service, _, _, _ := ownedService(t)
	if err := service.verifyInstallation(); err != nil {
		t.Fatalf("valid ownership rejected: %v", err)
	}
	service, _, marker, _ := ownedService(t)
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := service.verifyInstallation(); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("missing marker err=%v", err)
	}
	service, _, marker, _ = ownedService(t)
	if err := os.WriteFile(marker, []byte("bad\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := service.verifyInstallation(); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("malformed marker err=%v", err)
	}
	service, _, marker, _ = ownedService(t)
	if err := os.WriteFile(marker, []byte(digestBytes([]byte("other"))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := service.verifyInstallation(); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("digest mismatch err=%v", err)
	}
	service, target, _, _ := ownedService(t)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(target), "elsewhere"), target); err != nil {
		t.Fatal(err)
	}
	if err := service.verifyInstallation(); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("binary symlink err=%v", err)
	}
	service, _, marker, _ = ownedService(t)
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(marker), "elsewhere"), marker); err != nil {
		t.Fatal(err)
	}
	if err := service.verifyInstallation(); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("marker symlink err=%v", err)
	}
	service, _, _, _ = ownedService(t)
	service.Executable = func() (string, error) { return filepath.Join(service.Layout.Home, "elsewhere"), nil }
	if err := service.verifyInstallation(); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("noncanonical executable err=%v", err)
	}
}

func TestUpgradeRefusesDevelopmentAndUnsupportedPlatforms(t *testing.T) {
	service, _, _, _ := ownedService(t)
	service.Current = "development"
	if _, err := service.Upgrade(context.Background(), nil); !errors.Is(err, ErrDevelopment) {
		t.Fatalf("development err=%v", err)
	}
	service.Current = "1.0.0"
	service.GOOS = "darwin"
	if err := service.replace(context.Background(), "2.0.0", nil); !errors.Is(err, ErrUnsupportedAsset) {
		t.Fatalf("unsupported OS err=%v", err)
	}
	service.GOOS, service.GOARCH = "linux", "mips64"
	if err := service.replace(context.Background(), "2.0.0", nil); !errors.Is(err, ErrUnsupportedAsset) {
		t.Fatalf("unsupported arch err=%v", err)
	}
}

func TestUpgradeDownloadsVerifiesAndReplacesCanonicalInstallation(t *testing.T) {
	service, target, marker, _ := ownedService(t)
	newBytes := []byte("new release")
	digest := digestBytes(newBytes)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			http.Redirect(w, r, "/drobilica/tarlink/releases/tag/v2.0.0", http.StatusFound)
		case "/drobilica/tarlink/releases/tag/v2.0.0":
			_, _ = w.Write([]byte("release"))
		case "/download/v2.0.0/checksums.txt":
			_, _ = w.Write([]byte(digest + "  tarlink-linux-amd64\n"))
		case "/download/v2.0.0/tarlink-linux-amd64":
			_, _ = w.Write(newBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service.Client = &download.Client{HTTP: server.Client()}
	service.testAPIURL = server.URL + "/releases"
	service.testReleaseBaseURL = server.URL + "/download/v"
	service.GOOS, service.GOARCH = "linux", "amd64"
	value, err := service.Upgrade(context.Background(), nil)
	if err != nil || value.Latest != "2.0.0" {
		t.Fatalf("upgrade value=%#v err=%v", value, err)
	}
	if cached, ok := readCache(filepath.Join(service.Layout.Cache, "update-check.json"), time.Now()); !ok || cached.Latest != "2.0.0" {
		t.Fatalf("upgrade did not refresh cache: %#v ok=%t", cached, ok)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, newBytes) {
		t.Fatalf("binary=%q", got)
	}
	got, _ = os.ReadFile(marker)
	if string(got) != digest+"\n" {
		t.Fatalf("marker=%q", got)
	}
	if err := os.WriteFile(target, []byte("old again"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(digestBytes([]byte("old again"))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(service.Layout.Cache, "source")
	if err := os.WriteFile(source, newBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := service.atomicReplace(source, digestBytes([]byte("wrong")), nil); !errors.Is(err, download.ErrChecksumMismatch) {
		t.Fatalf("source TOCTOU checksum err=%v", err)
	}
	got, _ = os.ReadFile(target)
	if string(got) != "old again" {
		t.Fatalf("checksum failure replaced binary=%q", got)
	}
}

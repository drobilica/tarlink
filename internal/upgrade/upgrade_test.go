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

func TestCheckFreshBypassesCacheAndFallsBackOffline(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	service := &Service{Layout: filesystem.Layout{Cache: dir}, Current: "1.0.0", GOARCH: "amd64", Now: func() time.Time { return now }}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`[{"tag_name":"v2.0.0","assets":[{"name":"tarlink-linux-amd64","browser_download_url":"https://example.test/binary"},{"name":"checksums.txt","browser_download_url":"https://example.test/checksums"}]}]`))
	}))
	defer server.Close()
	service.Client = &download.Client{HTTP: server.Client()}
	service.APIURL = server.URL + "/releases"
	if err := writeCache(filepath.Join(dir, "update-check.json"), "1.1.0", now); err != nil {
		t.Fatal(err)
	}

	value, err := service.CheckFresh(context.Background())
	if err != nil || value.Latest != "2.0.0" || requests != 1 {
		t.Fatalf("fresh check value=%#v err=%v requests=%d", value, err, requests)
	}
	now = now.Add(25 * time.Hour)
	value, err = service.CheckFresh(context.Background())
	if err != nil || value.Latest != "2.0.0" || requests != 2 {
		t.Fatalf("offline fallback value=%#v err=%v requests=%d", value, err, requests)
	}
}

func TestCheckFreshHonorsCancellationBeforeCacheFallback(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	service := &Service{Layout: filesystem.Layout{Cache: dir}, Current: "1.0.0", APIURL: "https://127.0.0.1/releases", Now: func() time.Time { return now }}
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

func TestReleaseAssetMustBeUniqueAndNonEmpty(t *testing.T) {
	item := release{Assets: []asset{{Name: "tarlink-linux-amd64", URL: "https://github.com/a"}}}
	if !releaseHasAsset(item, "tarlink-linux-amd64") {
		t.Fatal("valid asset was rejected")
	}
	item.Assets = append(item.Assets, item.Assets[0])
	if releaseHasAsset(item, "tarlink-linux-amd64") {
		t.Fatal("duplicate asset was accepted")
	}
}

func TestSelectLatestFiltersUnsafeReleasesAndAssets(t *testing.T) {
	makeAsset := func(name string) asset { return asset{Name: name, URL: "https://example.test/" + name} }
	valid := func(tag string) release {
		return release{TagName: tag, Assets: []asset{makeAsset("tarlink-linux-amd64"), makeAsset("checksums.txt")}}
	}
	releases := []release{valid("v1.0.0"), valid("v2.0.0-rc.1"), valid("v2.0.0")}
	releases[1].Pre = true
	releases = append(releases, release{TagName: "v9.0.0", Assets: []asset{makeAsset("checksums.txt")}}, release{TagName: "not-a-version", Assets: valid("v1.0.0").Assets})
	if got, err := selectLatest(releases, "amd64"); err != nil || got != "2.0.0" {
		t.Fatalf("latest=%q err=%v", got, err)
	}
	if _, err := selectLatest(releases, "arm64"); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("missing arm64 asset err=%v", err)
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
	api := []byte(`[{"tag_name":"v2.0.0","draft":false,"prerelease":false,"assets":[{"name":"tarlink-linux-amd64","browser_download_url":"https://example.test/binary"},{"name":"checksums.txt","browser_download_url":"https://example.test/checksums"}]}]`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			_, _ = w.Write(api)
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
	service.APIURL = server.URL + "/releases"
	service.ReleaseBaseURL = server.URL + "/download/v"
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

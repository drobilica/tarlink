package upgrade

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

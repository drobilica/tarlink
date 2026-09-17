package artifactrepo

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryAcceptsUnusedAlgorithmDirectoryAndRepairsCorruption(t *testing.T) {
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "v1", "sha512")); err != nil {
		t.Fatal(err)
	}
	data := []byte("exact bytes")
	digest := sha256.Sum256(data)
	name := fmtDigest(digest[:])
	if err := os.WriteFile(filepath.Join(root, "v1", "sha256", name), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Add(context.Background(), root, "sha256", name, func(_ context.Context, _ string, _, _, destination string) error {
		return os.WriteFile(destination, data, 0600)
	}, "upstream"); err != nil {
		t.Fatal(err)
	}
	objects, err := Verify(root)
	if err != nil || len(objects) != 1 {
		t.Fatalf("verify objects=%v err=%v", objects, err)
	}
}

func TestSyncAggregatesFetchFailures(t *testing.T) {
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	err := Sync(context.Background(), root, []Object{{Algorithm: "sha256", Digest: first, URL: "one"}, {Algorithm: "sha256", Digest: second, URL: "two"}}, func(_ context.Context, url, _, _, _ string) error { return errors.New(url + " failed") })
	if err == nil || !strings.Contains(err.Error(), "one") || !strings.Contains(err.Error(), "two") {
		t.Fatalf("aggregate error=%v", err)
	}
}

func TestRepositoryRejectsUnsafeEntries(t *testing.T) {
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("c", 64)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	object := filepath.Join(root, "v1", "sha256", digest)
	if err := os.Symlink(outside, object); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root); err == nil {
		t.Fatal("symlinked repository object was accepted")
	}
	if err := os.Remove(object); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "repository.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "repository.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root); err == nil {
		t.Fatal("symlinked repository descriptor was accepted")
	}
}

func TestNormalizeSourcePreservesOrderedGlobalInputs(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "working")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	path, err := NormalizeSource("../repository", cwd)
	if err != nil || path != filepath.Join(filepath.Dir(cwd), "repository") {
		t.Fatalf("normalized path=%q err=%v", path, err)
	}
	if _, err := NormalizeSource("https://static.example/prefix", cwd); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeSource("https://static.example/prefix?query", cwd); err == nil {
		t.Fatal("URL query was accepted")
	}
	config := filepath.Join(t.TempDir(), "repositories.json")
	if err := AddSource(config, "/first"); err != nil {
		t.Fatal(err)
	}
	if err := AddSource(config, "/second"); err != nil {
		t.Fatal(err)
	}
	if err := AddSource(config, "/first"); err != nil {
		t.Fatal(err)
	}
	sources, err := LoadSources(config)
	if err != nil || strings.Join(sources, ",") != "/first,/second" {
		t.Fatalf("sources=%v err=%v", sources, err)
	}
}

func fmtDigest(value []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(value)*2)
	for i, b := range value {
		out[2*i], out[2*i+1] = hex[b>>4], hex[b&15]
	}
	return string(out)
}

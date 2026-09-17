package artifactrepo

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
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

func TestRepositoryRejectsUnexpectedPublicEntriesAndOversizedDescriptor(t *testing.T) {
	for _, test := range []struct {
		name string
		make func(string) error
	}{
		{name: "root file", make: func(root string) error {
			return os.WriteFile(filepath.Join(root, "README"), []byte("not public repository content"), 0600)
		}},
		{name: "v1 file", make: func(root string) error {
			return os.WriteFile(filepath.Join(root, "v1", "extra"), []byte("not an algorithm directory"), 0600)
		}},
		{name: "algorithm directory", make: func(root string) error { return os.Mkdir(filepath.Join(root, "v1", "sha256", "extra"), 0700) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := Init(root); err != nil {
				t.Fatal(err)
			}
			if err := test.make(root); err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(root); err == nil {
				t.Fatal("unsafe public entry was accepted")
			}
		})
	}

	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	descriptor := append([]byte(`{"format":"content-repository","version":1}`), []byte(strings.Repeat(" ", int(MaxDescriptorBytes)))...)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), descriptor, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root); err == nil || !strings.Contains(err.Error(), "descriptor") {
		t.Fatalf("oversized descriptor error = %v", err)
	}
}

func TestInitPublishesReadableRepositoryTree(t *testing.T) {
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Join(root, "v1"), filepath.Join(root, "v1", "sha256"), filepath.Join(root, "v1", "sha512")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0755 {
			t.Fatalf("%s mode = %o, want 755", path, info.Mode().Perm())
		}
	}
	descriptorInfo, err := os.Stat(filepath.Join(root, "repository.json"))
	if err != nil {
		t.Fatal(err)
	}
	if descriptorInfo.Mode().Perm() != 0644 {
		t.Fatalf("descriptor mode = %o, want 644", descriptorInfo.Mode().Perm())
	}
	data := []byte("public object")
	digest := sha256.Sum256(data)
	name := fmtDigest(digest[:])
	if err := Add(context.Background(), root, "sha256", name, func(_ context.Context, _, _, _, destination string) error {
		return os.WriteFile(destination, data, 0600)
	}, "fixture"); err != nil {
		t.Fatal(err)
	}
	objectInfo, err := os.Stat(filepath.Join(root, "v1", "sha256", name))
	if err != nil {
		t.Fatal(err)
	}
	if objectInfo.Mode().Perm() != 0644 {
		t.Fatalf("object mode = %o, want 644", objectInfo.Mode().Perm())
	}
	for _, path := range []string{filepath.Join(root, "v1", "sha256"), filepath.Join(root, "v1", "sha512")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0755 {
			t.Fatalf("%s mode = %o, want 755 after Add", path, info.Mode().Perm())
		}
	}
}

func TestInitDoesNotMutateNonEmptyRoot(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.WriteFile(private, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Init(root); err == nil {
		t.Fatal("initialized a non-empty directory")
	}
	after, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("root mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(root, "v1")); !os.IsNotExist(err) {
		t.Fatalf("unexpected v1 directory, err=%v", err)
	}
}

func TestSyncReclaimsInterruptedRootStaging(t *testing.T) {
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, ".object-stage-interrupted")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "object"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, ".tarlink-download-partial"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, ".tarlink-repository-descriptor-partial"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Sync(context.Background(), root, nil, nil); err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if rootInfo.Mode().Perm() != 0755 {
		t.Fatalf("repository root mode = %o, want 755 after Sync", rootInfo.Mode().Perm())
	}
	if _, err := os.Lstat(stage); !os.IsNotExist(err) {
		t.Fatalf("interrupted staging remains, err=%v", err)
	}
}

func TestLoadSourcesRejectsSpecialAndTrailingContent(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "repositories.json")
	if err := os.WriteFile(config, []byte(`{"sources":[]} {"unexpected":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSources(config); err == nil {
		t.Fatal("accepted trailing source configuration")
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(config, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSources(link); err == nil {
		t.Fatal("accepted symlinked source configuration")
	}
	fifo := filepath.Join(root, "sources.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSources(fifo); err == nil {
		t.Fatal("accepted FIFO source configuration")
	}
}

func TestRequiredIncludesResolvedRemoteDesktopIcon(t *testing.T) {
	artifactDigest := strings.Repeat("a", 64)
	iconDigest := strings.Repeat("b", 64)
	catalog := &registry.Catalog{Revision: "fixture", Variants: map[string]map[manifest.Platform]*manifest.Manifest{
		"demo": {
			{OS: "linux", Arch: "amd64"}: {
				ID: "demo", ReleaseHistory: manifest.ReleaseHistory{Releases: []manifest.Release{{Version: "1", URL: "https://example.test/app", Verification: manifest.Verification{Algorithm: "sha256", Digest: artifactDigest}}}},
				Desktop: manifest.Desktop{Icon: manifest.DesktopIcon{URL: "https://example.test/icon.png", SHA256: iconDigest}},
			},
		},
	}}
	objects, err := Required(catalog, Selection{App: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 || objects[1].Digest != iconDigest || objects[1].URL != "https://example.test/icon.png" {
		t.Fatalf("required objects = %#v", objects)
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

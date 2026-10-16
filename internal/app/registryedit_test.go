package app

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/locking"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
	"go.yaml.in/yaml/v3"
)

func registryEditFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeOnboardingManifest(t, filepath.Join(root, "apps", "foo", "manifest.yaml"), onboardingValidManifest)
	return root
}

func registryEditSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += string(data)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += target
		}
		result[rel] = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func newRegistryApplication() []byte {
	return []byte(strings.ReplaceAll(strings.Replace(onboardingValidManifest, "name: Foo", "name: Bar", 1), "foo", "bar"))
}

func TestRegistryEditAddPreviewAndPublication(t *testing.T) {
	root := registryEditFixture(t)
	before := registryEditSnapshot(t, root)
	m := &Maintainer{}
	options := RegistryEditOptions{Root: root, Add: newRegistryApplication(), DryRun: true}
	report, err := m.EditRegistry(context.Background(), options)
	if err != nil || !report.Changed || report.Written || !report.DryRun || report.ID != "bar" {
		t.Fatalf("preview=%+v err=%v", report, err)
	}
	if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
		t.Fatal("dry run changed checkout")
	}
	options.DryRun = false
	report, err = m.EditRegistry(context.Background(), options)
	if err != nil || !report.Written || report.Action != "add" || report.SchemaVersion != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	data, err := os.ReadFile(report.Path)
	if err != nil || !bytes.Equal(data, options.Add) {
		t.Fatalf("published bytes=%q err=%v", data, err)
	}
	catalog, err := registry.ValidateTree(root)
	if err != nil || len(catalog.Variants) != 2 {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatalf("staging or lock entries retained: %v", entries)
	}
	before = registryEditSnapshot(t, root)
	if _, err := m.EditRegistry(context.Background(), options); err == nil {
		t.Fatal("duplicate addition overwrote app")
	}
	if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
		t.Fatal("failed duplicate addition changed checkout")
	}
}

func TestRegistryEditDisableEnablePreservesHistoryAndComments(t *testing.T) {
	root := registryEditFixture(t)
	path := filepath.Join(root, "apps", "foo", "manifest.yaml")
	original := []byte("# Reviewed upstream\n---\n" + onboardingValidManifest + "# Keep this release history\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	m := &Maintainer{}
	enabled := false
	options := RegistryEditOptions{Root: root, ID: "foo", Enabled: &enabled, DryRun: true}
	before := registryEditSnapshot(t, root)
	if report, err := m.EditRegistry(context.Background(), options); err != nil || !report.Changed || report.Written {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
		t.Fatal("disable preview changed checkout")
	}
	options.DryRun = false
	if report, err := m.EditRegistry(context.Background(), options); err != nil || !report.Written {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.Replace(data, []byte("enabled: false\n"), nil, 1), original) {
		t.Fatalf("unrelated bytes changed: %s", data)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("manifest permissions changed")
	}
	catalog, err := registry.ValidateTree(root)
	if err != nil || catalog.Variants["foo"][manifest.Platform{OS: "linux", Arch: "amd64"}].WantsEnabled() {
		t.Fatalf("disable not reflected: %v", err)
	}
	before = registryEditSnapshot(t, root)
	if report, err := m.EditRegistry(context.Background(), options); err != nil || report.Changed || report.Written {
		t.Fatalf("idempotent report=%+v err=%v", report, err)
	}
	if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
		t.Fatal("idempotent edit changed checkout")
	}
	enabled = true
	if report, err := m.EditRegistry(context.Background(), options); err != nil || !report.Written {
		t.Fatalf("enable report=%+v err=%v", report, err)
	}
	data, _ = os.ReadFile(path)
	if !bytes.Equal(bytes.Replace(data, []byte("enabled: true\n"), nil, 1), original) {
		t.Fatalf("enable changed unrelated bytes: %s", data)
	}
}

func TestRegistryEnabledTokenPreservesFlowAndUnicode(t *testing.T) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(onboardingValidManifest), &node); err != nil {
		t.Fatal(err)
	}
	node.Content[0].Style = yaml.FlowStyle
	data, err := yaml.Marshal(&node)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("name: Foo"), []byte("name: Föö"), 1)
	disabled, err := setRegistryEnabled(data, false)
	if err != nil {
		t.Fatal(err)
	}
	// Put enabled after Unicode text to exercise YAML character columns.
	disabled = bytes.Replace(disabled, []byte("enabled: false, "), nil, 1)
	disabled = bytes.Replace(disabled, []byte("name: Föö,"), []byte("name: Föö, enabled: false,"), 1)
	updated, err := setRegistryEnabled(disabled, true)
	if err != nil || !bytes.Equal(updated, bytes.Replace(disabled, []byte("enabled: false"), []byte("enabled: true"), 1)) {
		t.Fatalf("updated=%s err=%v", updated, err)
	}
}

func TestRegistryEditRejectsUnsafeInputsWithoutMutation(t *testing.T) {
	for _, kind := range []string{"duplicate-name", "invalid", "oversized", "traversal", "unknown", "symlink-root", "symlink-parent", "symlink-apps", "symlink-manifest", "invalid-sibling", "cancelled", "locked", "read-only"} {
		t.Run(kind, func(t *testing.T) {
			root := registryEditFixture(t)
			enabled := false
			options := RegistryEditOptions{Root: root, ID: "foo", Enabled: &enabled}
			ctx := context.Background()
			switch kind {
			case "duplicate-name":
				options = RegistryEditOptions{Root: root, Add: bytes.Replace(newRegistryApplication(), []byte("name: Bar"), []byte("name: Foo"), 1)}
			case "invalid":
				options = RegistryEditOptions{Root: root, Add: []byte("schema: 3\n")}
			case "oversized":
				options = RegistryEditOptions{Root: root, Add: bytes.Repeat([]byte("x"), manifest.MaxManifestBytes+1)}
			case "traversal":
				options.ID = "../foo"
			case "unknown":
				options.ID = "missing"
			case "symlink-root", "symlink-parent":
				link := filepath.Join(t.TempDir(), "linked")
				target := root
				if kind == "symlink-parent" {
					target = filepath.Dir(root)
				}
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				options.Root = link
				if kind == "symlink-parent" {
					options.Root = filepath.Join(link, filepath.Base(root))
				}
			case "symlink-apps":
				if err := os.Rename(filepath.Join(root, "apps"), filepath.Join(root, "saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("saved", filepath.Join(root, "apps")); err != nil {
					t.Fatal(err)
				}
			case "symlink-manifest":
				path := filepath.Join(root, "apps", "foo", "manifest.yaml")
				if err := os.Rename(path, filepath.Join(root, "saved.yaml")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "saved.yaml"), path); err != nil {
					t.Fatal(err)
				}
			case "invalid-sibling":
				writeOnboardingManifest(t, filepath.Join(root, "apps", "bad", "manifest.yaml"), "schema: 3\n")
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "locked":
				lock, err := locking.AcquireDirectoryWithTimeout(ctx, root, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Release()
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 25*time.Millisecond)
				defer cancel()
			case "read-only":
				if os.Geteuid() == 0 {
					t.Skip("root can write read-only fixture")
				}
				if err := os.Chmod(root, 0500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(root, 0700)
			}
			before := registryEditSnapshot(t, root)
			report, err := (&Maintainer{}).EditRegistry(ctx, options)
			if err == nil || report.Written {
				t.Fatalf("unsafe edit report=%+v err=%v", report, err)
			}
			if kind == "locked" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lock error=%v", err)
			}
			if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
				t.Fatal("rejected edit changed checkout")
			}
		})
	}
}

func TestRegistryConcurrentAdditionsCannotOverwrite(t *testing.T) {
	root := registryEditFixture(t)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := (&Maintainer{}).EditRegistry(context.Background(), RegistryEditOptions{Root: root, Add: newRegistryApplication()})
			results <- err
		}()
	}
	successes := 0
	for range 2 {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful additions=%d", successes)
	}
	data, err := os.ReadFile(filepath.Join(root, "apps", "bar", "manifest.yaml"))
	if err != nil || !bytes.Equal(data, newRegistryApplication()) {
		t.Fatalf("published=%s err=%v", data, err)
	}
	if _, err := registry.ValidateTree(root); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryEditDirectoryFlushFailuresReportPublicationTruthfully(t *testing.T) {
	for _, kind := range []string{"add-before-publication", "add-after-publication", "disable-after-publication"} {
		t.Run(kind, func(t *testing.T) {
			root := registryEditFixture(t)
			enabled := false
			options := RegistryEditOptions{Root: root, Add: newRegistryApplication()}
			if strings.HasPrefix(kind, "disable") {
				options = RegistryEditOptions{Root: root, ID: "foo", Enabled: &enabled}
			}
			before := registryEditSnapshot(t, root)
			failure := errors.New("injected directory flush failure")
			calls := 0
			report, err := editRegistry(context.Background(), options, func(string) error {
				calls++
				if kind == "add-after-publication" && calls == 1 {
					return nil
				}
				return failure
			})
			if !errors.Is(err, failure) {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if kind == "add-before-publication" {
				if report.Written || !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
					t.Fatal("pre-publication failure changed registry")
				}
			} else {
				if !report.Written || !strings.Contains(err.Error(), "edit published") {
					t.Fatalf("publication hidden: %+v %v", report, err)
				}
				if _, err := registry.ValidateTree(root); err != nil {
					t.Fatalf("published registry invalid: %v", err)
				}
			}
			if entries, _ := os.ReadDir(root); len(entries) != 1 {
				t.Fatalf("staging retained: %v", entries)
			}
		})
	}
}

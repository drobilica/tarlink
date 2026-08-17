package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLayoutForAndRejectsRelativeXDG(t *testing.T) {
	home := t.TempDir()
	l, err := LayoutFor(home, func(k string) string { return map[string]string{"XDG_DATA_HOME": filepath.Join(home, "data")}[k] })
	if err != nil {
		t.Fatal(err)
	}
	if l.Apps != filepath.Join(home, "data", "tarlink", "apps") {
		t.Fatalf("apps=%s", l.Apps)
	}
	if _, err := LayoutFor(home, func(k string) string {
		if k == "XDG_CACHE_HOME" {
			return "cache"
		}
		return ""
	}); err == nil {
		t.Fatal("relative XDG value accepted")
	}
}

func TestNewLayoutHonorsTemporaryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	layout, err := NewLayout()
	if err != nil {
		t.Fatal(err)
	}
	if layout.Bin != filepath.Join(home, ".local", "bin") || layout.Apps != filepath.Join(home, ".local", "share", "tarlink", "apps") {
		t.Fatalf("layout escaped temporary HOME: %#v", layout)
	}
}

func TestValidation(t *testing.T) {
	for _, id := range []string{"demo", "a-1", "Demo", "a_b", "a."} {
		if err := ValidateID(id); (id == "demo" || id == "a-1") && err != nil {
			t.Fatalf("valid ID %q: %v", id, err)
		}
	}
	for _, id := range []string{"Demo", "a_b", "a.", "../x", ""} {
		if err := ValidateID(id); err == nil {
			t.Fatalf("invalid ID accepted: %q", id)
		}
	}
	if err := ValidateVersion(" 1"); err == nil {
		t.Fatal("trim-spaced version accepted")
	}
	if err := ValidateVersion("1/2"); err == nil {
		t.Fatal("slash version accepted")
	}
}

func TestSecureEnsureRejectsPreexistingSymlink(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	outside := t.TempDir()
	if err := os.Symlink(outside, data); err != nil {
		t.Fatal(err)
	}
	l, err := LayoutFor(home, func(k string) string {
		if k == "XDG_DATA_HOME" {
			return data
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Ensure(); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err=%v", err)
	}
}

func TestSafeRemoveDoesNotFollowSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := SafeRemove(root, filepath.Join(root, "link")); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal(err)
	}
}

func TestSafeRemoveUnlinksNestedSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "app")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(target, "current")); err != nil {
		t.Fatal(err)
	}
	if err := SafeRemove(root, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal(err)
	}
}

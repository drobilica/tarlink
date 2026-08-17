package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

func tarFixture(t *testing.T, entries []tar.Header) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg || h.Typeflag == 0 {
			payload := []byte(h.Name)
			if int64(len(payload)) > h.Size {
				payload = payload[:h.Size]
			}
			if int64(len(payload)) < h.Size {
				payload = append(payload, make([]byte, h.Size-int64(len(payload)))...)
			}
			if _, err := tw.Write(payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func zipFixture(t *testing.T, names ...string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func zipSymlinkFixture(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	h := &zip.FileHeader{Name: "link", Method: zip.Store}
	h.SetMode(os.ModeSymlink | 0777)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "target"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func zipContainedSymlinkFixture(t *testing.T, target string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	header := &zip.FileHeader{Name: "app/lib/libexample.so", Method: zip.Store}
	header.SetMode(os.ModeSymlink | 0o777)
	link, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(link, target); err != nil {
		t.Fatal(err)
	}
	regular, err := zw.Create("app/lib/libexample.so.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(regular, "library"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestExtractValidFixtures(t *testing.T) {
	t.Run("tar.gz", func(t *testing.T) {
		dest := t.TempDir()
		data := tarFixture(t, []tar.Header{{Name: "bin", Mode: 0755, Typeflag: tar.TypeDir}, {Name: "bin/run", Mode: 0755, Size: 7, Typeflag: tar.TypeReg}})
		if err := Extract(context.Background(), bytes.NewReader(data), dest, FormatTarGz, Limits{}); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dest, "bin", "run"))
		if err != nil || string(got) != "bin/run" {
			t.Fatalf("file = %q, err = %v", got, err)
		}
		st, err := os.Stat(filepath.Join(dest, "bin", "run"))
		if err != nil || st.Mode().Perm() != 0755 {
			t.Fatalf("mode = %v, err = %v", st.Mode(), err)
		}
	})
	t.Run("tar.xz", func(t *testing.T) {
		var raw bytes.Buffer
		tw := tar.NewWriter(&raw)
		if err := tw.WriteHeader(&tar.Header{Name: "x", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		var compressed bytes.Buffer
		xw, err := xz.NewWriter(&compressed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := xw.Write(raw.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := xw.Close(); err != nil {
			t.Fatal(err)
		}
		dest := t.TempDir()
		if err := Extract(context.Background(), bytes.NewReader(compressed.Bytes()), dest, FormatTarXZ, Limits{}); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(filepath.Join(dest, "x")); err != nil || string(got) != "x" {
			t.Fatalf("file = %q, err = %v", got, err)
		}
	})
	t.Run("zip", func(t *testing.T) {
		dest := t.TempDir()
		if err := Extract(context.Background(), bytes.NewReader(zipFixture(t, "a", "d/b")), dest, FormatZip, Limits{}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dest, "d", "b")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRejectUnsafeTarPaths(t *testing.T) {
	for _, name := range []string{"../escape", "a/../../escape", "/absolute", `C:\\absolute`, "C:/absolute", "a\\b", "a//b", "a/./b", "a/../b"} {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			data := tarFixture(t, []tar.Header{{Name: name, Mode: 0644, Size: int64(len(name)), Typeflag: tar.TypeReg}})
			if err := Extract(context.Background(), bytes.NewReader(data), dest, FormatTarGz, Limits{}); !errors.Is(err, ErrPath) {
				t.Fatalf("error = %v, want ErrPath", err)
			}
		})
	}
}

func TestRejectUnsafeTarTypesAndCollisions(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    tar.Header
	}{
		{"symlink", tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "target"}},
		{"hardlink", tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "target"}},
		{"fifo", tar.Header{Name: "pipe", Typeflag: tar.TypeFifo}},
		{"char device", tar.Header{Name: "char", Typeflag: tar.TypeChar}},
		{"block device", tar.Header{Name: "block", Typeflag: tar.TypeBlock}},
		{"setuid", tar.Header{Name: "setuid", Mode: 04755, Typeflag: tar.TypeReg}},
		{"setgid", tar.Header{Name: "setgid", Mode: 02755, Typeflag: tar.TypeReg}},
		{"sticky", tar.Header{Name: "sticky", Mode: 01755, Typeflag: tar.TypeReg}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Extract(context.Background(), bytes.NewReader(tarFixture(t, []tar.Header{tc.h})), t.TempDir(), FormatTarGz, Limits{})
			if !errors.Is(err, ErrEntryType) {
				t.Fatalf("error = %v, want ErrEntryType", err)
			}
		})
	}
	data := tarFixture(t, []tar.Header{{Name: "same", Mode: 0644, Size: 4, Typeflag: tar.TypeReg}, {Name: "same/child", Mode: 0644, Size: 10, Typeflag: tar.TypeReg}})
	if err := Extract(context.Background(), bytes.NewReader(data), t.TempDir(), FormatTarGz, Limits{}); !errors.Is(err, ErrCollision) {
		t.Fatalf("collision error = %v", err)
	}
}

func TestTarAcceptsContainedSharedLibrarySymlinkChain(t *testing.T) {
	data := tarFixture(t, []tar.Header{
		{Name: "app/lib/libexample.so", Typeflag: tar.TypeSymlink, Linkname: "libexample.so.1", Mode: 0o777},
		{Name: "app/lib/libexample.so.1", Typeflag: tar.TypeSymlink, Linkname: "libexample.so.1.2", Mode: 0o777},
		{Name: "app/lib/libexample.so.1.2", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
	})
	destination := t.TempDir()
	if err := Extract(context.Background(), bytes.NewReader(data), destination, FormatTarGz, Limits{}); err != nil {
		t.Fatal(err)
	}
	for linkPath, want := range map[string]string{
		"app/lib/libexample.so":   "libexample.so.1",
		"app/lib/libexample.so.1": "libexample.so.1.2",
	} {
		got, err := os.Readlink(filepath.Join(destination, linkPath))
		if err != nil || got != want {
			t.Fatalf("link %s = %q, %v; want %q", linkPath, got, err, want)
		}
	}
}

func TestTarAcceptsBoundedPAXGlobalMetadata(t *testing.T) {
	data := tarFixture(t, []tar.Header{
		{Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "registry commit metadata"}},
		{Name: "app/file", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
	})
	destination := t.TempDir()
	if err := Extract(context.Background(), bytes.NewReader(data), destination, FormatTarGz, Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(destination, "app", "file")); err != nil {
		t.Fatal(err)
	}
}

func TestTarRejectsEscapingAndNonLocalSymlinks(t *testing.T) {
	for _, target := range []string{"../outside", "../../outside", "/outside", `C:\outside`, "dir/target"} {
		t.Run(strings.ReplaceAll(target, "/", "_"), func(t *testing.T) {
			data := tarFixture(t, []tar.Header{{Name: "app/link", Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0o777}})
			if err := Extract(context.Background(), bytes.NewReader(data), t.TempDir(), FormatTarGz, Limits{}); !errors.Is(err, ErrPath) {
				t.Fatalf("target %q error = %v", target, err)
			}
		})
	}
}

func TestTarRejectsSymlinkAsExtractionParent(t *testing.T) {
	data := tarFixture(t, []tar.Header{
		{Name: "app/link", Typeflag: tar.TypeSymlink, Linkname: "target", Mode: 0o777},
		{Name: "app/link/file", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
		{Name: "app/target", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
	})
	if err := Extract(context.Background(), bytes.NewReader(data), t.TempDir(), FormatTarGz, Limits{}); !errors.Is(err, ErrCollision) {
		t.Fatalf("symlink parent error = %v", err)
	}
}

func TestZipContainedSymlinkPolicy(t *testing.T) {
	destination := t.TempDir()
	data := zipContainedSymlinkFixture(t, "libexample.so.1")
	if err := Extract(context.Background(), bytes.NewReader(data), destination, FormatZip, Limits{}); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(destination, "app", "lib", "libexample.so")
	if target, err := os.Readlink(link); err != nil || target != "libexample.so.1" {
		t.Fatalf("link target = %q, %v", target, err)
	}

	for _, target := range []string{"../outside", "/outside", "dir/target"} {
		if err := Extract(context.Background(), bytes.NewReader(zipContainedSymlinkFixture(t, target)), t.TempDir(), FormatZip, Limits{}); !errors.Is(err, ErrPath) {
			t.Fatalf("unsafe ZIP link target %q error = %v", target, err)
		}
	}
}

func TestLimitsAndFormatMismatch(t *testing.T) {
	data := tarFixture(t, []tar.Header{{Name: "large", Mode: 0644, Size: 4, Typeflag: tar.TypeReg}})
	if err := Extract(context.Background(), bytes.NewReader(data), t.TempDir(), FormatTarGz, Limits{MaxFileBytes: 3}); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit error = %v", err)
	}
	if err := Extract(context.Background(), bytes.NewReader(data), t.TempDir(), FormatZip, Limits{}); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestDestinationContract(t *testing.T) {
	data := tarFixture(t, []tar.Header{{Name: "x", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}})
	base := t.TempDir()
	if err := Extract(context.Background(), bytes.NewReader(data), filepath.Join(base, "missing"), FormatTarGz, Limits{}); !errors.Is(err, ErrDestination) {
		t.Fatalf("missing destination error = %v", err)
	}
	nonempty := filepath.Join(base, "nonempty")
	if err := os.Mkdir(nonempty, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(nonempty, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Extract(context.Background(), bytes.NewReader(data), nonempty, FormatTarGz, Limits{}); !errors.Is(err, ErrDestination) {
		t.Fatalf("nonempty destination error = %v", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
		t.Fatalf("marker = %q, err = %v", got, err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(nonempty, link); err != nil {
		t.Fatal(err)
	}
	if err := Extract(context.Background(), bytes.NewReader(data), link, FormatTarGz, Limits{}); !errors.Is(err, ErrDestination) {
		t.Fatalf("symlink destination error = %v", err)
	}
}

func TestRollbackAndInputLimits(t *testing.T) {
	dest := t.TempDir()
	data := tarFixture(t, []tar.Header{{Name: "created", Mode: 0644, Size: 7, Typeflag: tar.TypeReg}})
	truncated := data[:len(data)-4]
	if err := Extract(context.Background(), bytes.NewReader(truncated), dest, FormatTarGz, Limits{}); err == nil {
		t.Fatal("truncated archive unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(dest, "created")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back file stat = %v", err)
	}
	zipData := zipFixture(t, "input")
	if err := Extract(context.Background(), bytes.NewReader(zipData), t.TempDir(), FormatZip, Limits{MaxArchiveBytes: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("archive input limit error = %v", err)
	}
}

func TestZipUnsafeAndPathLimits(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", `C:\\absolute`, "a\\b"} {
		t.Run(name, func(t *testing.T) {
			if err := Extract(context.Background(), bytes.NewReader(zipFixture(t, name)), t.TempDir(), FormatZip, Limits{}); !errors.Is(err, ErrPath) {
				t.Fatalf("error = %v, want ErrPath", err)
			}
		})
	}
	if err := Extract(context.Background(), bytes.NewReader(zipSymlinkFixture(t)), t.TempDir(), FormatZip, Limits{}); !errors.Is(err, ErrEntryType) {
		t.Fatalf("zip symlink error = %v", err)
	}
	longName := strings.Repeat("x", 20)
	if err := Extract(context.Background(), bytes.NewReader(zipFixture(t, longName)), t.TempDir(), FormatZip, Limits{MaxPathBytes: 10}); !errors.Is(err, ErrLimit) {
		t.Fatalf("path limit error = %v", err)
	}
	deep := strings.Join([]string{"a", "b", "c"}, "/")
	if err := Extract(context.Background(), bytes.NewReader(zipFixture(t, deep)), t.TempDir(), FormatZip, Limits{MaxDepth: 2}); !errors.Is(err, ErrLimit) {
		t.Fatalf("depth limit error = %v", err)
	}
}

func TestEntryAndTotalLimits(t *testing.T) {
	data := tarFixture(t, []tar.Header{{Name: "a", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}, {Name: "b", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}})
	if err := Extract(context.Background(), bytes.NewReader(data), t.TempDir(), FormatTarGz, Limits{MaxEntries: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("entry limit error = %v", err)
	}
	if err := Extract(context.Background(), bytes.NewReader(data), t.TempDir(), FormatTarGz, Limits{MaxTotalBytes: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("total limit error = %v", err)
	}
}

func TestTarArchiveByteLimit(t *testing.T) {
	data := tarFixture(t, []tar.Header{{Name: "app", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}})
	if err := Extract(context.Background(), bytes.NewReader(data), t.TempDir(), FormatTarGz, Limits{MaxArchiveBytes: int64(len(data) - 1)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("compressed archive limit error = %v", err)
	}
}

func TestBlenderUpstreamArchiveCompatibility(t *testing.T) {
	source := os.Getenv("TARLINK_UPSTREAM_BLENDER_ARCHIVE")
	if source == "" {
		t.Skip("set TARLINK_UPSTREAM_BLENDER_ARCHIVE for the large upstream acceptance test")
	}
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	const expected = "96f6c181a30f4950607839dc84d42a354b250d8a0231b098b59b7bc69c351c48"
	if actual := hex.EncodeToString(hasher.Sum(nil)); actual != expected {
		t.Fatalf("SHA-256 = %s, want %s", actual, expected)
	}

	destination := t.TempDir()
	if err := ExtractPath(context.Background(), source, destination, FormatTarXZ, DefaultLimits()); err != nil {
		t.Fatalf("safe extraction rejected the reviewed Blender artifact: %v", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() || entries[0].Type()&os.ModeSymlink != 0 {
		t.Fatalf("unexpected top-level archive layout: %#v", entries)
	}
	executable := filepath.Join(destination, entries[0].Name(), "blender")
	info, err := os.Lstat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		t.Fatalf("Blender executable has unsafe or unusable mode %s", info.Mode())
	}
}

func TestGodotUpstreamArchiveCompatibility(t *testing.T) {
	source := os.Getenv("TARLINK_UPSTREAM_GODOT_ARCHIVE")
	if source == "" {
		t.Skip("set TARLINK_UPSTREAM_GODOT_ARCHIVE for the upstream acceptance test")
	}
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	hasher := sha512.New()
	if _, err := io.Copy(hasher, file); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	const expected = "4ccdab7a48eeccbe8819a2fc1f6262f8d72065d98601bcb3743fcbd7ebd39f373758a788ee3293a05ec5b2c48538266c437404312e372225cd2df273945a2de9"
	if actual := hex.EncodeToString(hasher.Sum(nil)); actual != expected {
		t.Fatalf("SHA-512 = %s, want %s", actual, expected)
	}

	destination := t.TempDir()
	if err := ExtractPath(context.Background(), source, destination, FormatZip, DefaultLimits()); err != nil {
		t.Fatalf("safe extraction rejected the reviewed Godot artifact: %v", err)
	}
	executable := filepath.Join(destination, "Godot_v4.7.1-stable_linux.x86_64")
	info, err := os.Lstat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		t.Fatalf("Godot executable has unsafe or unusable mode %s", info.Mode())
	}
}

func TestInvalidUTF8AndMalformedFormats(t *testing.T) {
	badName := string([]byte{'b', 0xff, 'd'})
	data := tarFixture(t, []tar.Header{{Name: badName, Mode: 0644, Size: 0, Typeflag: tar.TypeReg}})
	if err := Extract(context.Background(), bytes.NewReader(data), t.TempDir(), FormatTarGz, Limits{}); !errors.Is(err, ErrPath) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	for _, tc := range []struct {
		name   string
		data   []byte
		format Format
	}{
		{"gzip", tarFixture(t, []tar.Header{{Name: "x", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}}), FormatTarGz},
		{"xz", func() []byte {
			var raw bytes.Buffer
			tw := tar.NewWriter(&raw)
			if err := tw.WriteHeader(&tar.Header{Name: "x", Mode: 0644, Size: 0, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			xw, err := xz.NewWriter(&out)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := xw.Write(raw.Bytes()); err != nil {
				t.Fatal(err)
			}
			if err := xw.Close(); err != nil {
				t.Fatal(err)
			}
			return out.Bytes()
		}(), FormatTarXZ},
		{"zip", zipFixture(t, "x"), FormatZip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.data) < 8 {
				t.Fatal("fixture unexpectedly short")
			}
			if err := Extract(context.Background(), bytes.NewReader(tc.data[:len(tc.data)-1]), t.TempDir(), tc.format, Limits{}); err == nil {
				t.Fatal("truncated archive unexpectedly succeeded")
			}
		})
	}
}

func FuzzValidatePath(f *testing.F) {
	for _, seed := range []string{"file", "../file", "/file", `C:\\file`, "a/./b", "a/b", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got, err := validatePath(name, DefaultLimits())
		if err == nil {
			if got == "" || got == "." || got[0] == '/' || bytes.ContainsRune([]byte(got), '\\') {
				t.Fatalf("unsafe normalized path %q from %q", got, name)
			}
		}
	})
}

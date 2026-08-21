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

func zipFilesFixture(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
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

func TestExtractProgressFormatsAndCompletion(t *testing.T) {
	formats := []struct {
		name   string
		data   []byte
		format Format
		total  int64
	}{
		{"tar.gz", tarFixture(t, []tar.Header{{Name: "one", Mode: 0644, Size: 3, Typeflag: tar.TypeReg}}), FormatTarGz, -1},
		{"zip", zipFixture(t, "one", "two"), FormatZip, 6},
	}
	for _, tc := range formats {
		t.Run(tc.name, func(t *testing.T) {
			var events []struct {
				stage          string
				current, total int64
			}
			err := ExtractWithProgress(context.Background(), bytes.NewReader(tc.data), t.TempDir(), tc.format, Limits{}, func(stage string, current, total int64) {
				events = append(events, struct {
					stage          string
					current, total int64
				}{stage, current, total})
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) == 0 || events[len(events)-1].stage != ProgressExtracting || events[len(events)-1].current != events[len(events)-1].total && tc.total >= 0 {
				t.Fatalf("completion events = %+v", events)
			}
			if tc.total >= 0 && events[len(events)-1].total != tc.total {
				t.Fatalf("completion total = %d, want %d", events[len(events)-1].total, tc.total)
			}
		})
	}
}

func TestExtractProgressTarXZ(t *testing.T) {
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
	var last struct {
		stage          string
		current, total int64
	}
	if err := ExtractWithProgress(context.Background(), bytes.NewReader(compressed.Bytes()), t.TempDir(), FormatTarXZ, Limits{}, func(stage string, current, total int64) {
		last = struct {
			stage          string
			current, total int64
		}{stage, current, total}
	}); err != nil {
		t.Fatal(err)
	}
	if last.stage != ProgressExtracting || last.current != 1 || last.total != -1 {
		t.Fatalf("last progress = %+v", last)
	}
}

func TestExtractProgressCancellationAndFailure(t *testing.T) {
	data := tarFixture(t, []tar.Header{{Name: "one", Mode: 0644, Size: 3, Typeflag: tar.TypeReg}})
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := ExtractWithProgress(ctx, bytes.NewReader(data), t.TempDir(), FormatTarGz, Limits{}, func(string, int64, int64) {
		calls++
		cancel()
	})
	if !errors.Is(err, context.Canceled) || calls == 0 {
		t.Fatalf("cancellation error = %v, calls = %d", err, calls)
	}
	completed := false
	bad := append([]byte(nil), data[:len(data)-1]...)
	err = ExtractWithProgress(context.Background(), bytes.NewReader(bad), t.TempDir(), FormatTarGz, Limits{}, func(stage string, current, total int64) {
		if stage == ProgressExtracting && total >= 0 && current == total {
			completed = true
		}
	})
	if err == nil || completed {
		t.Fatalf("failure = %v, completed = %v", err, completed)
	}
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

func TestSharedBudgetCumulativeLimits(t *testing.T) {
	inner := zipFilesFixture(t, map[string][]byte{"payload": []byte("payload")})
	outer := zipFilesFixture(t, map[string][]byte{"inner.zip": inner})

	t.Run("total bytes include inner archive and output", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "outer.zip")
		if err := os.WriteFile(source, outer, 0o600); err != nil {
			t.Fatal(err)
		}
		outerRoot := t.TempDir()
		if err := ExtractNestedPath(context.Background(), source, outerRoot, t.TempDir(), FormatZip, "inner.zip", FormatZip, Limits{MaxTotalBytes: int64(len(inner) + len("payload") - 1)}, nil); !errors.Is(err, ErrLimit) {
			t.Fatalf("inner extraction = %v, want cumulative ErrLimit", err)
		}
	})

	t.Run("entry count includes both layers", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "outer.zip")
		if err := os.WriteFile(source, outer, 0o600); err != nil {
			t.Fatal(err)
		}
		outerRoot := t.TempDir()
		if err := ExtractNestedPath(context.Background(), source, outerRoot, t.TempDir(), FormatZip, "inner.zip", FormatZip, Limits{MaxEntries: 1}, nil); !errors.Is(err, ErrLimit) {
			t.Fatalf("inner extraction = %v, want cumulative ErrLimit", err)
		}
	})
}

func TestNestedInnerPathLimit(t *testing.T) {
	inner := zipFilesFixture(t, map[string][]byte{"payload-long": []byte("x")})
	outer := zipFilesFixture(t, map[string][]byte{"inner.zip": inner})
	source := filepath.Join(t.TempDir(), "outer.zip")
	if err := os.WriteFile(source, outer, 0o600); err != nil {
		t.Fatal(err)
	}
	err := ExtractNestedPath(context.Background(), source, t.TempDir(), t.TempDir(), FormatZip, "inner.zip", FormatZip, Limits{MaxPathBytes: 9}, nil)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("nested path limit error = %v", err)
	}
}

func TestExtractNestedAdversarialInputs(t *testing.T) {
	inner := zipFilesFixture(t, map[string][]byte{"app/run": []byte("run")})
	outer := zipFilesFixture(t, map[string][]byte{"payload/inner.zip": inner})
	writeArchive := func(t *testing.T, data []byte) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "outer.zip")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	run := func(t *testing.T, outerData []byte, innerPath string, innerFormat Format, limits Limits) error {
		t.Helper()
		return ExtractNestedPath(context.Background(), writeArchive(t, outerData), t.TempDir(), t.TempDir(), FormatZip, innerPath, innerFormat, limits, nil)
	}

	t.Run("declared two-layer archive succeeds", func(t *testing.T) {
		outerRoot, finalRoot := t.TempDir(), t.TempDir()
		source := writeArchive(t, outer)
		if err := ExtractNestedPath(context.Background(), source, outerRoot, finalRoot, FormatZip, "payload/inner.zip", FormatZip, Limits{}, nil); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(filepath.Join(finalRoot, "app", "run")); err != nil || string(got) != "run" {
			t.Fatalf("final payload = %q, err = %v", got, err)
		}
	})

	t.Run("missing declared inner archive", func(t *testing.T) {
		if err := run(t, outer, "missing.zip", FormatZip, Limits{}); err == nil {
			t.Fatal("missing inner archive unexpectedly succeeded")
		}
	})
	t.Run("wrong inner magic", func(t *testing.T) {
		bad := zipFilesFixture(t, map[string][]byte{"inner.zip": []byte("not a zip")})
		if err := run(t, bad, "inner.zip", FormatZip, Limits{}); !errors.Is(err, ErrInvalidFormat) {
			t.Fatalf("wrong magic error = %v, want ErrInvalidFormat", err)
		}
	})
	t.Run("corrupt inner archive", func(t *testing.T) {
		corrupt := append([]byte(nil), inner[:len(inner)-3]...)
		bad := zipFilesFixture(t, map[string][]byte{"inner.zip": corrupt})
		if err := run(t, bad, "inner.zip", FormatZip, Limits{}); err == nil {
			t.Fatal("corrupt inner archive unexpectedly succeeded")
		}
	})
	t.Run("unsupported inner format", func(t *testing.T) {
		if err := run(t, outer, "payload/inner.zip", Format("appimage"), Limits{}); !errors.Is(err, ErrInvalidFormat) {
			t.Fatalf("unsupported format error = %v, want ErrInvalidFormat", err)
		}
	})
	t.Run("inner per-file limit", func(t *testing.T) {
		large := zipFilesFixture(t, map[string][]byte{"app/run": []byte("1234")})
		if err := run(t, zipFilesFixture(t, map[string][]byte{"inner.zip": large}), "inner.zip", FormatZip, Limits{MaxFileBytes: 3}); !errors.Is(err, ErrLimit) {
			t.Fatalf("per-file error = %v, want ErrLimit", err)
		}
	})
	t.Run("inner path and depth limits", func(t *testing.T) {
		deep := zipFilesFixture(t, map[string][]byte{"a/b/c/run": []byte("x")})
		if err := run(t, zipFilesFixture(t, map[string][]byte{"inner.zip": deep}), "inner.zip", FormatZip, Limits{MaxDepth: 2}); !errors.Is(err, ErrLimit) {
			t.Fatalf("depth error = %v, want ErrLimit", err)
		}
		unsafe := zipFilesFixture(t, map[string][]byte{"../run": []byte("x")})
		if err := run(t, zipFilesFixture(t, map[string][]byte{"inner.zip": unsafe}), "inner.zip", FormatZip, Limits{}); !errors.Is(err, ErrPath) {
			t.Fatalf("path error = %v, want ErrPath", err)
		}
	})
	t.Run("does not recursively extract archive contents", func(t *testing.T) {
		nested := zipFilesFixture(t, map[string][]byte{"deep.txt": []byte("deep")})
		second := zipFilesFixture(t, map[string][]byte{"nested.zip": nested})
		outerRoot, finalRoot := t.TempDir(), t.TempDir()
		if err := ExtractNestedPath(context.Background(), writeArchive(t, zipFilesFixture(t, map[string][]byte{"inner.zip": second})), outerRoot, finalRoot, FormatZip, "inner.zip", FormatZip, Limits{}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(finalRoot, "deep.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recursive output exists, err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(finalRoot, "nested.zip")); err != nil {
			t.Fatalf("declared inner archive output missing: %v", err)
		}
	})
}

func TestOpenDeclaredFileRejectsRedirectsAndNonCanonicalPaths(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "inner.zip")
	if err := os.WriteFile(regular, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"./inner.zip", "dir/../inner.zip", "../inner.zip", "/inner.zip", "inner.zip/"} {
		t.Run(name, func(t *testing.T) {
			if _, err := OpenDeclaredFile(root, name); !errors.Is(err, ErrPath) {
				t.Fatalf("path error = %v, want ErrPath", err)
			}
		})
	}

	if err := os.Symlink("inner.zip", filepath.Join(root, "symlink.zip")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDeclaredFile(root, "symlink.zip"); !errors.Is(err, ErrPath) {
		t.Fatalf("symlink error = %v, want ErrPath", err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDeclaredFile(root, "directory"); !errors.Is(err, ErrEntryType) {
		t.Fatalf("directory error = %v, want ErrEntryType", err)
	}

	// A hardlink is a second directory entry for the same inode. It must not
	// be accepted as the declared archive, because the outer archive's object
	// identity cannot be established from the path alone.
	if err := os.Link(regular, filepath.Join(root, "hardlink.zip")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDeclaredFile(root, "hardlink.zip"); !errors.Is(err, ErrEntryType) {
		t.Fatalf("hardlink error = %v, want ErrEntryType", err)
	}
}

func TestDeclaredInnerFileResolutionHonorsOperationPathLimits(t *testing.T) {
	root := t.TempDir()
	relative := "one/two/inner.zip"
	full := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("declared path bytes", func(t *testing.T) {
		if _, err := openDeclaredFile(root, relative, Limits{MaxPathBytes: len(relative) - 1}); !errors.Is(err, ErrLimit) {
			t.Fatalf("declared path byte limit error = %v, want ErrLimit", err)
		}
	})
	t.Run("declared path depth", func(t *testing.T) {
		if _, err := openDeclaredFile(root, relative, Limits{MaxDepth: 2}); !errors.Is(err, ErrLimit) {
			t.Fatalf("declared path depth limit error = %v, want ErrLimit", err)
		}
	})
	t.Run("declared path at configured limits", func(t *testing.T) {
		f, err := openDeclaredFile(root, relative, Limits{MaxPathBytes: len(relative), MaxDepth: 3})
		if err != nil {
			t.Fatalf("configured limits rejected declared path: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("default limits", func(t *testing.T) {
		f, err := OpenDeclaredFile(root, relative)
		if err != nil {
			t.Fatalf("default limits rejected declared path: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	})
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
	for _, test := range []struct {
		name        string
		environment string
		digest      string
		executable  string
	}{
		{name: "amd64", environment: "TARLINK_UPSTREAM_GODOT_ARCHIVE", digest: "9aa00f7a605200940bce3027a567b782f49bd8e940dd06ae9e987bd65aee1b1467edd56ed84fcdcbdd44354bf613bdbb4e5d2913e925850368e150c59ed54c65", executable: "Godot_v4.7.2-stable_linux.x86_64"},
		{name: "arm64", environment: "TARLINK_UPSTREAM_GODOT_ARM64_ARCHIVE", digest: "dd59918da086bd49bde2f5450b5e567ff8650cbde9abbd7b8f4ca1197ff8c609baa38834666d032deafb47099078d7822279e2a0e06e5665745468f26533e7e2", executable: "Godot_v4.7.2-stable_linux.arm64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := os.Getenv(test.environment)
			if source == "" {
				t.Skipf("set %s for the upstream acceptance test", test.environment)
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
			if actual := hex.EncodeToString(hasher.Sum(nil)); actual != test.digest {
				t.Fatalf("SHA-512 = %s, want %s", actual, test.digest)
			}

			destination := t.TempDir()
			if err := ExtractPath(context.Background(), source, destination, FormatZip, DefaultLimits()); err != nil {
				t.Fatalf("safe extraction rejected the reviewed Godot artifact: %v", err)
			}
			info, err := os.Lstat(filepath.Join(destination, test.executable))
			if err != nil {
				t.Fatal(err)
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
				t.Fatalf("Godot executable has unsafe or unusable mode %s", info.Mode())
			}
		})
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

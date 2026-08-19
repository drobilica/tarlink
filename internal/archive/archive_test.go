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

func TestTarValidateMode(t *testing.T) {
	// Accepted: ordinary permissions plus recognized, inert Unix file-type
	// bits. Because the Typeflag is authoritative, a recognized file-type
	// bit is accepted even when it mismatches the entry's Typeflag.
	for _, tc := range []struct {
		name string
		h    []tar.Header
	}{
		{"regular 0100644", []tar.Header{{Name: "f", Mode: 0100644, Size: 1, Typeflag: tar.TypeReg}}},
		{"regular 0100755", []tar.Header{{Name: "f", Mode: 0100755, Size: 1, Typeflag: tar.TypeReg}}},
		{"regular-a 0100644", []tar.Header{{Name: "f", Mode: 0100644, Size: 1, Typeflag: tar.TypeRegA}}},
		{"regular-a plain 0644", []tar.Header{{Name: "f", Mode: 0644, Size: 1, Typeflag: tar.TypeRegA}}},
		{"dir 040755", []tar.Header{{Name: "d", Mode: 040755, Typeflag: tar.TypeDir}}},
		{"dir plain 0755", []tar.Header{{Name: "d", Mode: 0755, Typeflag: tar.TypeDir}}},
		{"symlink 0120777", []tar.Header{{Name: "t", Mode: 0100644, Size: 1, Typeflag: tar.TypeReg}, {Name: "l", Mode: 0120777, Typeflag: tar.TypeSymlink, Linkname: "t"}}},
		{"symlink plain 0777", []tar.Header{{Name: "t", Mode: 0100644, Size: 1, Typeflag: tar.TypeReg}, {Name: "l", Mode: 0777, Typeflag: tar.TypeSymlink, Linkname: "t"}}},
		{"plain 0644", []tar.Header{{Name: "f", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}}},
		{"plain 0000", []tar.Header{{Name: "f", Mode: 0000, Size: 1, Typeflag: tar.TypeReg}}},
		{"dir type bits on regular", []tar.Header{{Name: "f", Mode: 040755, Size: 1, Typeflag: tar.TypeReg}}},
		{"regular type bits on dir", []tar.Header{{Name: "d", Mode: 0100755, Typeflag: tar.TypeDir}}},
		{"symlink type bits on regular", []tar.Header{{Name: "f", Mode: 0120755, Size: 1, Typeflag: tar.TypeReg}}},
		{"fifo type bits on regular", []tar.Header{{Name: "f", Mode: 0o10644, Size: 1, Typeflag: tar.TypeReg}}},
		{"char type bits on regular", []tar.Header{{Name: "f", Mode: 0o20644, Size: 1, Typeflag: tar.TypeReg}}},
		{"block type bits on regular", []tar.Header{{Name: "f", Mode: 0o60644, Size: 1, Typeflag: tar.TypeReg}}},
		{"socket type bits on regular", []tar.Header{{Name: "f", Mode: 0o140644, Size: 1, Typeflag: tar.TypeReg}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Extract(context.Background(), bytes.NewReader(tarFixture(t, tc.h)), t.TempDir(), FormatTarGz, Limits{}); err != nil {
				t.Fatalf("Extract error = %v", err)
			}
		})
	}

	// Rejected: special bits (plain or with type bits), unknown/high bits,
	// and negative modes. Recognized file-type bits are inert, but any
	// combination of file types or bit outside the recognized set is
	// rejected as unknown.
	for _, tc := range []struct {
		name string
		h    tar.Header
	}{
		{"setuid plain", tar.Header{Name: "f", Mode: 04755, Size: 1, Typeflag: tar.TypeReg}},
		{"setgid plain", tar.Header{Name: "f", Mode: 02755, Size: 1, Typeflag: tar.TypeReg}},
		{"sticky plain", tar.Header{Name: "f", Mode: 01755, Size: 1, Typeflag: tar.TypeReg}},
		{"setuid with type bits", tar.Header{Name: "f", Mode: 0104755, Size: 1, Typeflag: tar.TypeReg}},
		{"setgid with type bits", tar.Header{Name: "d", Mode: 0402755, Typeflag: tar.TypeDir}},
		{"sticky with type bits", tar.Header{Name: "f", Mode: 0101755, Size: 1, Typeflag: tar.TypeReg}},
		{"combination of file types (fifo|chr)", tar.Header{Name: "f", Mode: 0o30644, Size: 1, Typeflag: tar.TypeReg}},
		{"combination of file types (reg|blk)", tar.Header{Name: "f", Mode: 0o160644, Size: 1, Typeflag: tar.TypeReg}},
		{"combination of file types (fifo|reg)", tar.Header{Name: "f", Mode: 0o110644, Size: 1, Typeflag: tar.TypeReg}},
		{"unknown file type bits", tar.Header{Name: "f", Mode: 0o150644, Size: 1, Typeflag: tar.TypeReg}},
		{"all file type bits", tar.Header{Name: "f", Mode: 0o170644, Size: 1, Typeflag: tar.TypeReg}},
		{"unknown high bit", tar.Header{Name: "f", Mode: 0o200000644, Size: 1, Typeflag: tar.TypeReg}},
		{"negative mode", tar.Header{Name: "f", Mode: -0644, Size: 1, Typeflag: tar.TypeReg}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Extract(context.Background(), bytes.NewReader(tarFixture(t, []tar.Header{tc.h})), t.TempDir(), FormatTarGz, Limits{})
			if !errors.Is(err, ErrEntryType) {
				t.Fatalf("error = %v, want ErrEntryType", err)
			}
		})
	}
}

func TestTarRepeatedDirectoryNoop(t *testing.T) {
	// A TAR directory listed more than once is a no-op when the entry is
	// already tracked as a directory and is a real, non-symlink directory on
	// disk.
	data := tarFixture(t, []tar.Header{
		{Name: "app", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "app", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "app", Typeflag: tar.TypeDir, Mode: 0o755},
	})
	dest := t.TempDir()
	if err := Extract(context.Background(), bytes.NewReader(data), dest, FormatTarGz, Limits{}); err != nil {
		t.Fatalf("repeated TAR directory no-op error = %v", err)
	}
	st, err := os.Lstat(filepath.Join(dest, "app"))
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("app stat = %v, err = %v; want real directory", st, err)
	}
}

func TestTarDirectoryCollisionsRemain(t *testing.T) {
	// Repeated directory no-ops are the only tolerated duplicate. Any other
	// collision on a directory path must still be rejected, including
	// type changes and symlink involvement.
	for _, tc := range []struct {
		name string
		h    []tar.Header
	}{
		{"file then dir", []tar.Header{{Name: "d", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}, {Name: "d", Typeflag: tar.TypeDir, Mode: 0o755}}},
		{"dir then file", []tar.Header{{Name: "d", Typeflag: tar.TypeDir, Mode: 0o755}, {Name: "d", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}}},
		{"dir then symlink", []tar.Header{{Name: "t", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}, {Name: "d", Typeflag: tar.TypeDir, Mode: 0o755}, {Name: "d", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "t"}}},
		{"symlink then dir", []tar.Header{{Name: "t", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}, {Name: "d", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "t"}, {Name: "d", Typeflag: tar.TypeDir, Mode: 0o755}}},
		{"child under file", []tar.Header{{Name: "d", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}, {Name: "d/child", Typeflag: tar.TypeDir, Mode: 0o755}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Extract(context.Background(), bytes.NewReader(tarFixture(t, tc.h)), t.TempDir(), FormatTarGz, Limits{})
			if !errors.Is(err, ErrCollision) {
				t.Fatalf("error = %v, want ErrCollision", err)
			}
		})
	}
}

func TestTarImplicitParentThenExplicitDirectory(t *testing.T) {
	// A directory first created implicitly as a parent of a deeper entry and
	// then listed explicitly as a directory entry is a tolerated no-op: it is
	// already a tracked, real, non-symlink directory.
	data := tarFixture(t, []tar.Header{
		{Name: "app/sub/file", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
		{Name: "app/sub", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "app", Typeflag: tar.TypeDir, Mode: 0o755},
	})
	dest := t.TempDir()
	if err := Extract(context.Background(), bytes.NewReader(data), dest, FormatTarGz, Limits{}); err != nil {
		t.Fatalf("implicit parent then explicit directory error = %v", err)
	}
	for _, d := range []string{"app", "app/sub"} {
		st, err := os.Lstat(filepath.Join(dest, d))
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s stat = %v, err = %v; want real directory", d, st, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dest, "app/sub/file")); err != nil {
		t.Fatalf("file stat error = %v", err)
	}
}

func TestTarDuplicateRegularFileAndSymlinkRejected(t *testing.T) {
	// Only repeated directories are a no-op. Duplicate regular files and
	// duplicate symlinks must still be rejected as collisions.
	for _, tc := range []struct {
		name string
		h    []tar.Header
	}{
		{"duplicate regular file", []tar.Header{
			{Name: "f", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
			{Name: "f", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
		}},
		{"duplicate symlink", []tar.Header{
			{Name: "t", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
			{Name: "l", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "t"},
			{Name: "l", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "t"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Extract(context.Background(), bytes.NewReader(tarFixture(t, tc.h)), t.TempDir(), FormatTarGz, Limits{})
			if !errors.Is(err, ErrCollision) {
				t.Fatalf("error = %v, want ErrCollision", err)
			}
		})
	}
}

func TestTarRepeatedDirectoryRequiresRealNonSymlinkDir(t *testing.T) {
	// The repeated-directory no-op must not weaken security: it only applies
	// when the on-disk path is a real, non-symlink directory created by this
	// extractor. A directory repeated over a path occupied by a symlink or a
	// file must still be a collision.
	for _, tc := range []struct {
		name string
		h    []tar.Header
	}{
		{"dir then symlink then dir", []tar.Header{
			{Name: "t", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
			{Name: "d", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "d", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "t"},
			{Name: "d", Typeflag: tar.TypeDir, Mode: 0o755},
		}},
		{"symlink then repeated dir", []tar.Header{
			{Name: "t", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
			{Name: "d", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "t"},
			{Name: "d", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "d", Typeflag: tar.TypeDir, Mode: 0o755},
		}},
		{"file then repeated dir", []tar.Header{
			{Name: "d", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
			{Name: "d", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "d", Typeflag: tar.TypeDir, Mode: 0o755},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Extract(context.Background(), bytes.NewReader(tarFixture(t, tc.h)), t.TempDir(), FormatTarGz, Limits{})
			if !errors.Is(err, ErrCollision) {
				t.Fatalf("error = %v, want ErrCollision", err)
			}
		})
	}
}

func TestZipRepeatedDirectoryCollision(t *testing.T) {
	// ZIP keeps strict duplicate-path rejection: a directory listed more
	// than once is a collision, not a no-op.
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for i := 0; i < 2; i++ {
		if _, err := zw.Create("d/"); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	err := Extract(context.Background(), bytes.NewReader(out.Bytes()), t.TempDir(), FormatZip, Limits{})
	if !errors.Is(err, ErrCollision) {
		t.Fatalf("error = %v, want ErrCollision", err)
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

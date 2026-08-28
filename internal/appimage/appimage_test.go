package appimage

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, marker byte, machine uint16) string {
	t.Helper()
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F'})
	data[4], data[5], data[6] = 2, 1, 1
	data[8], data[9], data[10] = 'A', 'I', marker
	binary.LittleEndian.PutUint16(data[16:18], 3)
	binary.LittleEndian.PutUint16(data[18:20], machine)
	path := filepath.Join(t.TempDir(), "application.AppImage")
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateType2Architectures(t *testing.T) {
	if err := ValidatePath(fixture(t, 2, 0x3e), "amd64"); err != nil {
		t.Fatalf("valid amd64 AppImage rejected: %v", err)
	}
	if err := ValidatePath(fixture(t, 2, 0xb7), "arm64"); err != nil {
		t.Fatalf("valid arm64 AppImage rejected: %v", err)
	}
}

func TestValidateRejectsMalformedAndType1(t *testing.T) {
	short := filepath.Join(t.TempDir(), "short")
	if err := os.WriteFile(short, []byte("not an AppImage"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePath(short, "amd64"); err == nil {
		t.Fatal("short artifact unexpectedly accepted")
	}
	if err := ValidatePath(fixture(t, 1, 0x3e), "amd64"); err == nil {
		t.Fatal("Type 1 artifact unexpectedly accepted")
	}
	if err := ValidatePath(fixture(t, 2, 0x03), "amd64"); err == nil {
		t.Fatal("wrong architecture unexpectedly accepted")
	}
}

func TestValidateRejectsSymlinkAndDirectory(t *testing.T) {
	target := fixture(t, 2, 0x3e)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePath(link, "amd64"); err == nil {
		t.Fatal("symlink unexpectedly accepted")
	}
	directory := t.TempDir()
	if err := ValidatePath(directory, "amd64"); err == nil {
		t.Fatal("directory unexpectedly accepted")
	}
}

func TestValidateSupportedTarget(t *testing.T) {
	if err := ValidateSupportedTarget(fixture(t, 2, 0x3e)); err != nil {
		t.Fatalf("valid amd64 AppImage rejected: %v", err)
	}
	if err := ValidateSupportedTarget(fixture(t, 2, 0xb7)); err != nil {
		t.Fatalf("valid arm64 AppImage rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		errMsg string
	}{
		{"unsupported machine", "architecture mismatch"},
		{"short file", ""},
		{"non-ELF", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			switch tc.name {
			case "unsupported machine":
				path = fixture(t, 2, 0x03)
			case "short file":
				path = filepath.Join(t.TempDir(), "short")
				if err := os.WriteFile(path, []byte("not an AppImage"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "non-ELF":
				data := make([]byte, 64)
				data[0] = 'x'
				path = filepath.Join(t.TempDir(), "non-elf")
				if err := os.WriteFile(path, data, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			err := ValidateSupportedTarget(path)
			if err == nil {
				t.Fatal("artifact unexpectedly accepted")
			}
			if tc.errMsg != "" && !strings.Contains(err.Error(), tc.errMsg) {
				t.Fatalf("error %q does not contain %q", err, tc.errMsg)
			}
		})
	}
}

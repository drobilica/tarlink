// Package appimage validates AppImage artifacts without executing or mounting
// them. TarLink treats a validated AppImage as an opaque regular file.
package appimage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

var (
	ErrInvalid = errors.New("invalid or unsupported AppImage")
)

// ValidatePath performs the bounded structural checks needed before an
// AppImage is staged. It intentionally does not inspect or extract its
// embedded filesystem.
func ValidatePath(filename, architecture string) error {
	before, err := os.Lstat(filename)
	if err != nil {
		return fmt.Errorf("stat AppImage: %w", err)
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("AppImage: %w (artifact is not a regular file)", ErrInvalid)
	}
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("open AppImage: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat opened AppImage: %w", err)
	}
	if !opened.Mode().IsRegular() {
		return fmt.Errorf("AppImage: %w (opened artifact is not a regular file)", ErrInvalid)
	}
	after, err := os.Lstat(filename)
	if err != nil || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		return fmt.Errorf("AppImage: %w (artifact changed while opening)", ErrInvalid)
	}
	var header [64]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return fmt.Errorf("AppImage header: %w: %v", ErrInvalid, err)
	}
	if string(header[0:4]) != "\x7fELF" {
		return fmt.Errorf("AppImage: %w (not ELF)", ErrInvalid)
	}
	if header[5] != 1 { // AppImages must have a deterministic little-endian ELF header.
		return fmt.Errorf("AppImage: %w (unsupported ELF byte order)", ErrInvalid)
	}
	if header[6] != 1 {
		return fmt.Errorf("AppImage: %w (invalid ELF version)", ErrInvalid)
	}
	if header[4] != 2 {
		return fmt.Errorf("AppImage: %w (only 64-bit ELF is supported)", ErrInvalid)
	}
	if string(header[8:10]) == "AI\x01" {
		return fmt.Errorf("AppImage: %w (type 1 is unsupported)", ErrInvalid)
	}
	// The AppImage specification places the Type-2 magic bytes AI\x02 at
	// offsets 8..10 of the ELF identification header.
	if string(header[8:11]) != "AI\x02" {
		return fmt.Errorf("AppImage: %w (missing type 2 marker)", ErrInvalid)
	}
	fileType := binary.LittleEndian.Uint16(header[16:18])
	if fileType != 2 && fileType != 3 { // ET_EXEC or position-independent ET_DYN.
		return fmt.Errorf("AppImage: %w (ELF is not executable)", ErrInvalid)
	}
	var expected uint16
	switch architecture {
	case "amd64":
		expected = 0x3e
	case "arm64":
		expected = 0xb7
	default:
		return fmt.Errorf("AppImage: %w (unsupported target architecture)", ErrInvalid)
	}
	if machine := binary.LittleEndian.Uint16(header[18:20]); machine != expected {
		return fmt.Errorf("AppImage: %w (ELF architecture mismatch)", ErrInvalid)
	}
	return nil
}

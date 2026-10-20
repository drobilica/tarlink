// Package integrity records and compares local installation baselines. These
// inventories detect drift; they are not signatures or a new trust authority.
package integrity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const Filename = ".tarlink-integrity.json"

// Extraction permits 100,000 entries and 24 GiB; allow the narrow external icon addition.
const maxEntries = 100010
const maxBytes int64 = (24 << 30) + (4 << 20)
const maxInventory = 32 << 20

type Identity struct {
	App         string `json:"app"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
	AppImage    bool   `json:"appimage"`
}
type entry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Target string `json:"target,omitempty"`
}
type inventory struct {
	Schema   int      `json:"schema"`
	Identity Identity `json:"identity"`
	Entries  []entry  `json:"entries"`
}
type Change struct {
	Path   string
	Status string // changed, missing, unexpected
}

// Record is called only on a private, verified, fully materialized tree. A
// publisher-supplied inventory at the reserved path is never accepted.
func Record(ctx context.Context, directory string, identity Identity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat(Filename); !errors.Is(err, os.ErrNotExist) {
		return errors.New("reserved integrity inventory path already exists or is inaccessible")
	}
	entries, err := scan(ctx, root, identity.AppImage)
	if err != nil {
		return err
	}
	data, err := json.Marshal(inventory{Schema: 1, Identity: identity, Entries: entries})
	if err != nil {
		return err
	}
	if len(data) > maxInventory {
		return errors.New("integrity inventory exceeds size limit")
	}
	file, err := root.OpenFile(Filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	return errors.Join(writeErr, syncErr, file.Close())
}

// Verify never records or updates a baseline. Missing inventories are reported
// to the caller, including installations made before inventory recording.
func Verify(ctx context.Context, directory string, identity Identity) ([]Change, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := openRegular(root, Filename)
	if err != nil {
		return nil, fmt.Errorf("baseline unavailable: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx, file}, maxInventory+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if len(data) > maxInventory {
		return nil, errors.New("integrity inventory exceeds size limit")
	}
	var saved inventory
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return nil, fmt.Errorf("invalid integrity inventory: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing integrity inventory data")
	}
	canonical, err := json.Marshal(saved)
	if err != nil || !bytes.Equal(data, canonical) {
		return nil, errors.New("noncanonical or duplicate integrity inventory fields")
	}
	if saved.Schema != 1 || saved.Identity != identity || len(saved.Entries) == 0 || len(saved.Entries) > maxEntries {
		return nil, errors.New("invalid integrity inventory identity, schema, or entry count")
	}
	expected := make(map[string]entry, len(saved.Entries))
	last := ""
	for _, e := range saved.Entries {
		if !fs.ValidPath(e.Path) || len(e.Path) > 4096 || strings.Count(e.Path, "/") >= 64 || e.Path == "." || e.Path == Filename || e.Path <= last || excluded(e.Path, identity.AppImage) || !validEntry(e) {
			return nil, errors.New("invalid integrity inventory entry")
		}
		expected[e.Path] = e
		last = e.Path
	}
	actual, err := scan(ctx, root, identity.AppImage)
	if err != nil {
		return nil, err
	}
	changes := make([]Change, 0)
	for _, e := range actual {
		old, ok := expected[e.Path]
		if !ok {
			changes = append(changes, Change{e.Path, "unexpected"})
		} else if old != e {
			changes = append(changes, Change{e.Path, "changed"})
		}
		delete(expected, e.Path)
	}
	for path := range expected {
		changes = append(changes, Change{path, "missing"})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func validEntry(e entry) bool {
	if e.Mode > 0o7777 || e.Size < 0 || e.Size > maxBytes {
		return false
	}
	switch e.Kind {
	case "file":
		digest, err := hex.DecodeString(e.SHA256)
		return err == nil && len(digest) == sha256.Size && strings.ToLower(e.SHA256) == e.SHA256 && e.Target == ""
	case "directory":
		return e.Size == 0 && e.SHA256 == "" && e.Target == ""
	case "symlink":
		return e.Size == 0 && e.SHA256 == "" && e.Target != ""
	}
	return false
}

func excluded(path string, appimage bool) bool {
	if !appimage {
		return false
	}
	for _, directory := range []string{"appimage.home", "appimage.config"} {
		if path == directory || strings.HasPrefix(path, directory+"/") {
			return true
		}
	}
	return false
}

func scan(ctx context.Context, root *os.Root, appimage bool) ([]entry, error) {
	entries := make([]entry, 0)
	var total int64
	err := fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		if path == Filename {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if excluded(path, appimage) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if len(entries) >= maxEntries || len(path) > 4096 || strings.Count(path, "/") >= 64 {
			return errors.New("payload exceeds integrity traversal limits")
		}
		info, err := root.Lstat(path)
		if err != nil {
			return err
		}
		e := entry{Path: path, Mode: uint32(info.Mode().Perm())}
		if info.Mode()&os.ModeSetuid != 0 {
			e.Mode |= 0o4000
		}
		if info.Mode()&os.ModeSetgid != 0 {
			e.Mode |= 0o2000
		}
		if info.Mode()&os.ModeSticky != 0 {
			e.Mode |= 0o1000
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			e.Kind = "symlink"
			e.Target, err = root.Readlink(path)
		case info.IsDir():
			e.Kind = "directory"
		case info.Mode().IsRegular():
			e.Kind = "file"
			e.Size = info.Size()
			if e.Size > maxBytes-total {
				return errors.New("payload exceeds integrity byte limit")
			}
			total += e.Size
			var file *os.File
			file, err = openRegular(root, path)
			if err == nil {
				h := sha256.New()
				var n int64
				n, err = io.Copy(h, io.LimitReader(contextReader{ctx, file}, e.Size+1))
				after, statErr := file.Stat()
				err = errors.Join(err, statErr, file.Close())
				if err == nil && (n != e.Size || !os.SameFile(info, after) || after.Size() != info.Size() || after.ModTime() != info.ModTime()) {
					err = errors.New("payload changed during verification")
				}
				e.SHA256 = hex.EncodeToString(h.Sum(nil))
			}
		default:
			return fmt.Errorf("unsupported payload file type at %q", path)
		}
		if err != nil {
			return fmt.Errorf("inspect %q: %w", path, err)
		}
		entries = append(entries, e)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, err
}

func openRegular(root *os.Root, path string) (*os.File, error) {
	file, err := root.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("not a regular file")
	}
	if err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
			err = errors.New("hardlinked file")
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

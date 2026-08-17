// Package archive extracts the small set of archive formats accepted by
// tarlink.  It is deliberately conservative: archive names are untrusted
// input and an extraction never follows an archive-created (or pre-existing)
// symlink.
package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ulikunitz/xz"
)

// Format is the declared format of an archive. The declaration is checked
// against the archive's magic bytes; it is not used to guess a format.
type Format string

const (
	FormatTarGz Format = "tar.gz"
	FormatTarXZ Format = "tar.xz"
	FormatZip   Format = "zip"
)

// Limits bounds the amount of work an archive can cause. Zero fields use the
// corresponding secure default. Limits are checked before writing bytes.
type Limits struct {
	MaxEntries      int
	MaxTotalBytes   int64
	MaxFileBytes    int64
	MaxArchiveBytes int64
	MaxPathBytes    int
	MaxDepth        int
}

const (
	defaultMaxEntries      = 100000
	defaultMaxTotalBytes   = int64(24 << 30)
	defaultMaxFileBytes    = int64(8 << 30)
	defaultMaxArchiveBytes = int64(8 << 30)
	defaultMaxPathBytes    = 4096
	defaultMaxDepth        = 64
)

// DefaultLimits returns the secure extraction limits.
func DefaultLimits() Limits {
	return Limits{
		MaxEntries:      defaultMaxEntries,
		MaxTotalBytes:   defaultMaxTotalBytes,
		MaxFileBytes:    defaultMaxFileBytes,
		MaxArchiveBytes: defaultMaxArchiveBytes,
		MaxPathBytes:    defaultMaxPathBytes,
		MaxDepth:        defaultMaxDepth,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxEntries <= 0 {
		l.MaxEntries = d.MaxEntries
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = d.MaxTotalBytes
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = d.MaxFileBytes
	}
	if l.MaxArchiveBytes <= 0 {
		l.MaxArchiveBytes = d.MaxArchiveBytes
	}
	if l.MaxPathBytes <= 0 {
		l.MaxPathBytes = d.MaxPathBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	return l
}

var (
	ErrInvalidFormat = errors.New("invalid or unsupported archive format")
	ErrPath          = errors.New("unsafe archive entry path")
	ErrLimit         = errors.New("archive extraction limit exceeded")
	ErrEntryType     = errors.New("unsupported archive entry type")
	ErrCollision     = errors.New("duplicate archive path or path type collision")
	ErrDestination   = errors.New("destination must be an existing empty directory")
)

// Extract extracts a declared archive format from r into destination.
// destination is caller-owned and must be an existing empty directory. Paths
// created by this call are removed when extraction fails.
func Extract(ctx context.Context, r io.Reader, destination string, declared Format, limits Limits) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return fmt.Errorf("archive: nil source: %w", ErrInvalidFormat)
	}
	return extract(ctx, &contextReader{ctx: ctx, r: r}, destination, declared, limits)
}

// ExtractPath extracts an archive from sourcePath into destination.
func ExtractPath(ctx context.Context, sourcePath, destination string, declared Format, limits Limits) error {
	f, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("archive: open source: %w", err)
	}
	defer f.Close()
	return Extract(ctx, f, destination, declared, limits)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
	}
	n, err := r.r.Read(p)
	if cerr := r.ctx.Err(); cerr != nil {
		return n, cerr
	}
	return n, err
}

type extractor struct {
	ctx      context.Context
	root     string
	limits   Limits
	total    int64
	entries  int
	paths    map[string]entryKind
	symlinks map[string]string
	created  []string
}

type entryKind uint8

const (
	kindDir entryKind = iota + 1
	kindFile
	kindSymlink
)

func extract(ctx context.Context, source io.Reader, destination string, declared Format, limits Limits) error {
	limits = limits.withDefaults()
	source = &boundedArchiveReader{source: source, remaining: limits.MaxArchiveBytes}
	if declared != FormatTarGz && declared != FormatTarXZ && declared != FormatZip {
		return fmt.Errorf("archive: %w: %q", ErrInvalidFormat, declared)
	}
	root, err := filepathAbsClean(destination)
	if err != nil {
		return fmt.Errorf("archive: destination: %w", err)
	}
	x := &extractor{
		ctx: ctx, root: root, limits: limits,
		paths: make(map[string]entryKind), symlinks: make(map[string]string),
	}
	if err := x.prepareRoot(); err != nil {
		return fmt.Errorf("archive: destination: %w", err)
	}
	ok := false
	defer func() {
		// rollback is intentionally best-effort and only considers paths this
		// extractor recorded below the caller-provided root.
		if !ok {
			x.rollback()
		}
	}()

	prefix := make([]byte, 6)
	n, readErr := io.ReadFull(source, prefix)
	if readErr != nil {
		return fmt.Errorf("archive: read magic: %w", readErr)
	}
	prefix = prefix[:n]
	actual := detect(prefix)
	if actual != declared {
		return fmt.Errorf("archive: declared %q does not match content (%q): %w", declared, actual, ErrInvalidFormat)
	}
	stream := io.MultiReader(bytes.NewReader(prefix), source)
	switch declared {
	case FormatTarGz:
		err = x.extractTarGz(stream)
	case FormatTarXZ:
		err = x.extractTarXZ(stream)
	case FormatZip:
		err = x.extractZip(stream)
	}
	if err != nil {
		return fmt.Errorf("archive: extract: %w", err)
	}
	if err := x.validateSymlinks(); err != nil {
		return fmt.Errorf("archive: validate links: %w", err)
	}
	ok = true
	return nil
}

type boundedArchiveReader struct {
	source    io.Reader
	remaining int64
}

func (reader *boundedArchiveReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	maximum := reader.remaining + 1
	if maximum <= 0 {
		maximum = 1
	}
	if int64(len(buffer)) > maximum {
		buffer = buffer[:maximum]
	}
	count, err := reader.source.Read(buffer)
	if int64(count) > reader.remaining {
		reader.remaining = 0
		return 0, ErrLimit
	}
	reader.remaining -= int64(count)
	return count, err
}

func filepathAbsClean(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty destination")
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(a), nil
}

func detect(p []byte) Format {
	if len(p) >= 2 && p[0] == 0x1f && p[1] == 0x8b {
		return FormatTarGz
	}
	if len(p) >= 6 && bytes.Equal(p[:6], []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}) {
		return FormatTarXZ
	}
	if len(p) >= 4 && (bytes.Equal(p[:4], []byte{'P', 'K', 0x03, 0x04}) || bytes.Equal(p[:4], []byte{'P', 'K', 0x05, 0x06}) || bytes.Equal(p[:4], []byte{'P', 'K', 0x07, 0x08})) {
		return FormatZip
	}
	return ""
}

func (x *extractor) prepareRoot() error {
	if err := realPathComponents(x.root); err != nil {
		return err
	}
	st, err := os.Lstat(x.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrDestination
		}
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return ErrDestination
	}
	entries, err := os.ReadDir(x.root)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return ErrDestination
	}
	return nil
}

func realPathComponents(root string) error {
	for p := root; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return ErrDestination
			}
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return ErrDestination
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
	}
}

func (x *extractor) rollback() {
	for i := len(x.created) - 1; i >= 0; i-- {
		p := x.created[i]
		if !underRoot(x.root, p) {
			continue
		}
		st, e := os.Lstat(p)
		if e != nil || st.Mode()&os.ModeSymlink != 0 {
			if e == nil {
				_ = os.Remove(p)
			}
			continue
		}
		_ = os.Remove(p)
	}
}

func underRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func (x *extractor) extractTarGz(r io.Reader) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	err = x.extractTar(gz)
	closeErr := gz.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (x *extractor) extractTarXZ(r io.Reader) error {
	xzReader, err := (xz.ReaderConfig{DictCap: 1 << 30}).NewReader(r)
	if err != nil {
		return err
	}
	return x.extractTar(xzReader)
}

func (x *extractor) extractTar(r io.Reader) error {
	tr := tar.NewReader(&contextReader{ctx: x.ctx, r: r})
	for {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			// Consume the decompressor to validate its checksum/footer. tar
			// reaches its end marker before gzip/xz necessarily reaches EOF.
			_, drainErr := copyContext(x.ctx, io.Discard, r, 1<<20)
			return drainErr
		}
		if err != nil {
			return err
		}
		if err := x.countEntry(); err != nil {
			return err
		}
		if h.Mode < 0 || h.Mode&^07777 != 0 || h.Mode&07000 != 0 {
			return fmt.Errorf("%w: special permission bits", ErrEntryType)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			// archive/tar has already consumed and bounded this POSIX PAX
			// metadata record to 1 MiB. Go deliberately does not apply global
			// records to later entries. It creates no filesystem object, but any
			// metadata name is still subjected to the normal path policy.
			if h.Name != "" {
				if _, err := validatePath(h.Name, x.limits); err != nil {
					return err
				}
			}
			continue
		}
		name, err := validatePath(h.Name, x.limits)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if h.Size != 0 {
				return fmt.Errorf("%w: directory has data", ErrEntryType)
			}
			if err := x.makeDir(name); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if strings.HasSuffix(h.Name, "/") {
				return fmt.Errorf("%w: regular file has directory name", ErrEntryType)
			}
			if h.Size < 0 || h.Size > x.limits.MaxFileBytes || h.Size > x.limits.MaxTotalBytes-x.total {
				return ErrLimit
			}
			exec := h.Mode&0111 != 0
			if err := x.makeFile(name, tr, h.Size, exec); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if h.Size != 0 || strings.HasSuffix(h.Name, "/") {
				return fmt.Errorf("%w: malformed symbolic link", ErrEntryType)
			}
			if err := x.makeSymlink(name, h.Linkname); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: tar type %d", ErrEntryType, h.Typeflag)
		}
	}
}

func (x *extractor) extractZip(r io.Reader) error {
	// ZIP requires a ReaderAt. Keep the spool in the caller-owned staging
	// directory so it cannot be confused with an unrelated system temp file,
	// and so archive entries naturally collide with the reserved name.
	tmp, err := os.CreateTemp(x.root, ".tarlink-archive-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer tmp.Close()
	if _, err := copyContext(x.ctx, tmp, r, x.limits.MaxArchiveBytes); err != nil {
		return err
	}
	st, err := tmp.Stat()
	if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	zr, err := zip.NewReader(tmp, st.Size())
	if err != nil {
		return err
	}
	if len(zr.File) > x.limits.MaxEntries {
		return ErrLimit
	}
	for _, f := range zr.File {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		if err := x.countEntry(); err != nil {
			return err
		}
		name, err := validatePath(f.Name, x.limits)
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode&os.ModeSetuid != 0 || mode&os.ModeSetgid != 0 || mode&os.ModeSticky != 0 {
			return fmt.Errorf("%w: special permission bits", ErrEntryType)
		}
		if mode&os.ModeType != 0 && !mode.IsDir() && mode&os.ModeSymlink == 0 {
			return fmt.Errorf("%w: zip mode %s", ErrEntryType, mode)
		}
		if mode&os.ModeSymlink != 0 {
			if strings.HasSuffix(f.Name, "/") || f.UncompressedSize64 > uint64(x.limits.MaxPathBytes) {
				return fmt.Errorf("%w: malformed symbolic link", ErrEntryType)
			}
			body, err := f.Open()
			if err != nil {
				return err
			}
			var target bytes.Buffer
			_, copyErr := copyContext(x.ctx, &target, body, int64(x.limits.MaxPathBytes))
			closeErr := body.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if err := x.makeSymlink(name, target.String()); err != nil {
				return err
			}
			continue
		}
		isDir := strings.HasSuffix(f.Name, "/") || mode.IsDir()
		if isDir {
			if f.UncompressedSize64 != 0 {
				return fmt.Errorf("%w: directory has data", ErrEntryType)
			}
			if err := x.makeDir(name); err != nil {
				return err
			}
			continue
		}
		if f.UncompressedSize64 > uint64(x.limits.MaxFileBytes) || f.UncompressedSize64 > uint64(x.limits.MaxTotalBytes-x.total) {
			return ErrLimit
		}
		body, err := f.Open()
		if err != nil {
			return err
		}
		err = x.makeFile(name, body, int64(f.UncompressedSize64), mode.Perm()&0111 != 0)
		closeErr := body.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (x *extractor) countEntry() error {
	if x.entries >= x.limits.MaxEntries {
		return ErrLimit
	}
	x.entries++
	return nil
}

func validatePath(name string, limits Limits) (string, error) {
	if name == "" || !utf8.ValidString(name) || strings.IndexByte(name, 0) >= 0 || strings.Contains(name, "\\") {
		return "", ErrPath
	}
	if strings.HasPrefix(name, "/") || (len(name) >= 2 && ((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z')) && name[1] == ':') {
		return "", ErrPath
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return "", ErrPath
		}
	}
	if len(name) > limits.MaxPathBytes {
		return "", ErrLimit
	}
	trailing := strings.HasSuffix(name, "/")
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" {
		return "", ErrPath
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) > limits.MaxDepth {
		return "", ErrLimit
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", ErrPath
		}
	}
	clean := path.Join(parts...)
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", ErrPath
	}
	if trailing {
		return clean, nil
	}
	return clean, nil
}

func (x *extractor) makeDir(name string) error {
	if _, exists := x.paths[name]; exists {
		return ErrCollision
	}
	if err := x.ensureParents(name); err != nil {
		return err
	}
	p := x.path(name)
	if st, err := os.Lstat(p); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return ErrCollision
		}
		return ErrCollision
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(p, 0755); err != nil {
		return err
	}
	x.created = append(x.created, p)
	if err := os.Chmod(p, 0755); err != nil {
		return err
	}
	x.paths[name] = kindDir
	return nil
}

func (x *extractor) makeFile(name string, src io.Reader, expected int64, executable bool) error {
	if _, exists := x.paths[name]; exists {
		return ErrCollision
	}
	if err := x.ensureParents(name); err != nil {
		return err
	}
	p := x.path(name)
	mode := os.FileMode(0644)
	if executable {
		mode = 0755
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	x.created = append(x.created, p)
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	copyLimit := x.limits.MaxFileBytes
	if expected >= 0 && expected < copyLimit {
		copyLimit = expected + 1
	}
	if remaining := x.limits.MaxTotalBytes - x.total; remaining < copyLimit {
		copyLimit = remaining
	}
	fileBytes, copyErr := copyContext(x.ctx, f, src, copyLimit)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if expected >= 0 && fileBytes != expected {
		return io.ErrUnexpectedEOF
	}
	if fileBytes > x.limits.MaxFileBytes || fileBytes > x.limits.MaxTotalBytes-x.total {
		return ErrLimit
	}
	x.total += fileBytes
	x.paths[name] = kindFile
	return nil
}

// makeSymlink implements the deliberately narrow v0.1 link policy: only a
// relative, same-directory, single-component target is accepted. Links are
// created without following them, may never be used as extraction parents,
// and are validated after extraction to terminate at an extracted regular
// file. This supports conventional shared-library link chains without
// accepting traversal, absolute links, directory links, or dangling links.
func (x *extractor) makeSymlink(name, target string) error {
	if _, exists := x.paths[name]; exists {
		return ErrCollision
	}
	validated, err := validatePath(target, x.limits)
	if err != nil || validated != target || path.Base(target) != target {
		return fmt.Errorf("%w: symbolic link target", ErrPath)
	}
	if err := x.ensureParents(name); err != nil {
		return err
	}
	destination := x.path(name)
	if _, err := os.Lstat(destination); err == nil {
		return ErrCollision
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, destination); err != nil {
		return err
	}
	x.created = append(x.created, destination)
	x.paths[name] = kindSymlink
	x.symlinks[name] = target
	return nil
}

func (x *extractor) validateSymlinks() error {
	for name := range x.symlinks {
		current := name
		seen := make(map[string]struct{})
		for {
			if _, exists := seen[current]; exists {
				return fmt.Errorf("%w: symbolic link cycle", ErrEntryType)
			}
			seen[current] = struct{}{}
			target, link := x.symlinks[current]
			if !link {
				if x.paths[current] != kindFile {
					return fmt.Errorf("%w: symbolic link does not end at a regular file", ErrEntryType)
				}
				break
			}
			current = path.Join(path.Dir(current), target)
		}
	}
	return nil
}

func (x *extractor) ensureParents(name string) error {
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		pname := strings.Join(parts[:i], "/")
		if k, ok := x.paths[pname]; ok {
			if k != kindDir {
				return ErrCollision
			}
		}
		p := x.path(pname)
		st, err := os.Lstat(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if err := os.Mkdir(p, 0755); err != nil {
					return err
				}
				x.created = append(x.created, p)
				if err := os.Chmod(p, 0755); err != nil {
					return err
				}
				x.paths[pname] = kindDir
				continue
			}
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return ErrCollision
		}
	}
	return nil
}

func (x *extractor) path(name string) string {
	return x.root + string(os.PathSeparator) + strings.ReplaceAll(name, "/", string(os.PathSeparator))
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader, max int64) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := src.Read(buf)
		if n > 0 {
			if max >= 0 && (total > max-int64(n)) {
				return total, ErrLimit
			}
			wn, werr := dst.Write(buf[:n])
			if werr != nil {
				return total, werr
			}
			if wn != n {
				return total, io.ErrShortWrite
			}
			total += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			return total, err
		}
	}
}

package research

import (
	"archive/zip"
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeMinimalELF writes a minimal little-endian 64-bit ELF file with the
// supplied dynamic metadata. It is sufficient for debug/elf to read DT_NEEDED,
// DT_SONAME, DT_RPATH and DT_RUNPATH without executing the file.
func writeMinimalELF(w io.Writer, fileType elf.Type, machine elf.Machine, needed, soname, rpath, runpath string) error {
	const (
		ehSize  = 64
		phSize  = 56
		dynSize = 16
		shSize  = 64
	)
	phoff := uint64(ehSize)
	dynOffset := uint64(ehSize + 2*phSize)

	// .dynstr string table.
	dynstr := []byte{0}
	dynstrOff := func(s string) uint64 {
		if s == "" {
			return 0
		}
		off := uint64(len(dynstr))
		dynstr = append(dynstr, []byte(s)...)
		dynstr = append(dynstr, 0)
		return off
	}
	neededOff := dynstrOff(needed)
	sonameOff := dynstrOff(soname)
	rpathOff := dynstrOff(rpath)
	runpathOff := dynstrOff(runpath)

	type dynEntry struct {
		tag elf.DynTag
		val uint64
	}
	var entries []dynEntry
	if soname != "" {
		entries = append(entries, dynEntry{elf.DT_SONAME, sonameOff})
	}
	if needed != "" {
		entries = append(entries, dynEntry{elf.DT_NEEDED, neededOff})
	}
	if rpath != "" {
		entries = append(entries, dynEntry{elf.DT_RPATH, rpathOff})
	}
	if runpath != "" {
		entries = append(entries, dynEntry{elf.DT_RUNPATH, runpathOff})
	}
	entries = append(entries, dynEntry{elf.DT_STRTAB, 0}) // patched after sizing
	entries = append(entries, dynEntry{elf.DT_STRSZ, uint64(len(dynstr))})
	entries = append(entries, dynEntry{elf.DT_NULL, 0})

	dyn := make([]byte, 0, len(entries)*dynSize)
	strtabEntryIndex := -1
	for _, e := range entries {
		if e.tag == elf.DT_STRTAB {
			strtabEntryIndex = len(dyn) / dynSize
		}
		var b [dynSize]byte
		binary.LittleEndian.PutUint64(b[0:8], uint64(e.tag))
		binary.LittleEndian.PutUint64(b[8:16], e.val)
		dyn = append(dyn, b[:]...)
	}
	dynstrAddr := dynOffset + uint64(len(dyn))
	binary.LittleEndian.PutUint64(dyn[strtabEntryIndex*dynSize+8:strtabEntryIndex*dynSize+dynSize], dynstrAddr)

	// Section name string table.
	shstrtab := []byte{0, '.', 'd', 'y', 'n', 'a', 'm', 'i', 'c', 0, '.', 'd', 'y', 'n', 's', 't', 'r', 0, '.', 's', 'h', 's', 't', 'r', 't', 'a', 'b', 0}
	shstrOff := func(name string) uint32 {
		for i := 0; i <= len(shstrtab)-len(name)-1; i++ {
			if string(shstrtab[i:i+len(name)+1]) == name+"\x00" {
				return uint32(i)
			}
		}
		return 0
	}

	const (
		idxNull = iota
		idxDynamic
		idxDynstr
		idxShstrtab
		shnum
	)

	shstrtabOffset := dynstrAddr + uint64(len(dynstr))
	shoff := shstrtabOffset + uint64(len(shstrtab))
	fileSize := shoff + shnum*shSize

	var hdr [ehSize]byte
	copy(hdr[0:4], []byte{0x7f, 'E', 'L', 'F'})
	hdr[4] = 2 // 64-bit
	hdr[5] = 1 // little-endian
	hdr[6] = 1 // ELF version
	binary.LittleEndian.PutUint16(hdr[16:18], uint16(fileType))
	binary.LittleEndian.PutUint16(hdr[18:20], uint16(machine))
	binary.LittleEndian.PutUint32(hdr[20:24], 1)
	binary.LittleEndian.PutUint64(hdr[24:32], 0)
	binary.LittleEndian.PutUint64(hdr[32:40], phoff)
	binary.LittleEndian.PutUint64(hdr[40:48], shoff)
	binary.LittleEndian.PutUint32(hdr[48:52], 0)
	binary.LittleEndian.PutUint16(hdr[52:54], ehSize)
	binary.LittleEndian.PutUint16(hdr[54:56], phSize)
	binary.LittleEndian.PutUint16(hdr[56:58], 2)
	binary.LittleEndian.PutUint16(hdr[58:60], shSize)
	binary.LittleEndian.PutUint16(hdr[60:62], shnum)
	binary.LittleEndian.PutUint16(hdr[62:64], idxShstrtab)

	var phLoad [phSize]byte
	binary.LittleEndian.PutUint32(phLoad[0:4], uint32(elf.PT_LOAD))
	binary.LittleEndian.PutUint32(phLoad[4:8], uint32(elf.PF_R|elf.PF_X))
	binary.LittleEndian.PutUint64(phLoad[8:16], 0)
	binary.LittleEndian.PutUint64(phLoad[16:24], 0)
	binary.LittleEndian.PutUint64(phLoad[24:32], 0)
	binary.LittleEndian.PutUint64(phLoad[32:40], fileSize)
	binary.LittleEndian.PutUint64(phLoad[40:48], fileSize)
	binary.LittleEndian.PutUint64(phLoad[48:56], 0x1000)

	var phDyn [phSize]byte
	binary.LittleEndian.PutUint32(phDyn[0:4], uint32(elf.PT_DYNAMIC))
	binary.LittleEndian.PutUint32(phDyn[4:8], uint32(elf.PF_R))
	binary.LittleEndian.PutUint64(phDyn[8:16], dynOffset)
	binary.LittleEndian.PutUint64(phDyn[16:24], dynOffset)
	binary.LittleEndian.PutUint64(phDyn[24:32], dynOffset)
	binary.LittleEndian.PutUint64(phDyn[32:40], uint64(len(dyn)))
	binary.LittleEndian.PutUint64(phDyn[40:48], uint64(len(dyn)))
	binary.LittleEndian.PutUint64(phDyn[48:56], 0x8)

	var buf bytes.Buffer
	buf.Write(hdr[:])
	buf.Write(phLoad[:])
	buf.Write(phDyn[:])
	buf.Write(dyn)
	buf.Write(dynstr)
	buf.Write(shstrtab)

	writeSh := func(name string, shtype elf.SectionType, flags elf.SectionFlag, addr, offset, size uint64, link uint32) {
		var sh [shSize]byte
		binary.LittleEndian.PutUint32(sh[0:4], shstrOff(name))
		binary.LittleEndian.PutUint32(sh[4:8], uint32(shtype))
		binary.LittleEndian.PutUint64(sh[8:16], uint64(flags))
		binary.LittleEndian.PutUint64(sh[16:24], addr)
		binary.LittleEndian.PutUint64(sh[24:32], offset)
		binary.LittleEndian.PutUint64(sh[32:40], size)
		binary.LittleEndian.PutUint32(sh[40:44], link)
		binary.LittleEndian.PutUint32(sh[44:48], 0)
		binary.LittleEndian.PutUint64(sh[48:56], 1)
		binary.LittleEndian.PutUint64(sh[56:64], 0)
		buf.Write(sh[:])
	}
	writeSh("", elf.SHT_NULL, 0, 0, 0, 0, 0)
	writeSh(".dynamic", elf.SHT_DYNAMIC, elf.SHF_ALLOC, dynOffset, dynOffset, uint64(len(dyn)), idxDynstr)
	writeSh(".dynstr", elf.SHT_STRTAB, elf.SHF_ALLOC, dynstrAddr, dynstrAddr, uint64(len(dynstr)), 0)
	writeSh(".shstrtab", elf.SHT_STRTAB, 0, 0, shstrtabOffset, uint64(len(shstrtab)), 0)

	_, err := w.Write(buf.Bytes())
	return err
}

func elfBytes(t *testing.T, fileType elf.Type, machine elf.Machine, needed, soname, rpath, runpath string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := writeMinimalELF(&buf, fileType, machine, needed, soname, rpath, runpath); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipWithELF(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
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
	return buf.Bytes()
}

func inspectZip(t *testing.T, data []byte) Inspection {
	return inspectZipArch(t, data, "")
}

func inspectZipArch(t *testing.T, data []byte, expectedArch string) Inspection {
	t.Helper()
	d := t.TempDir()
	p := filepath.Join(d, "artifact.zip")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Inspect(context.Background(), Artifact{Path: p, Size: int64(len(data))}, "zip", expectedArch)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestInspectELFDependenciesReportsTransitiveExternalSONAMEs(t *testing.T) {
	files := map[string][]byte{
		"game":            elfBytes(t, elf.ET_EXEC, elf.EM_X86_64, "libfoo.so.1", "", "", ""),
		"lib/libfoo.so.1": elfBytes(t, elf.ET_DYN, elf.EM_X86_64, "libmissing.so.1", "libfoo.so.1", "", ""),
	}
	r := inspectZip(t, zipWithELF(t, files))
	if !reflect.DeepEqual(r.Dependencies.ExternalSONAMEs, []string{"libmissing.so.1"}) {
		t.Fatalf("external=%#v", r.Dependencies.ExternalSONAMEs)
	}
}

func TestInspectELFDependenciesArchitectureMismatchBlocks(t *testing.T) {
	data := zipWithELF(t, map[string][]byte{
		"game": elfBytes(t, elf.ET_EXEC, elf.EM_AARCH64, "", "", "", ""),
	})
	r := inspectZipArch(t, data, "amd64")
	if !reflect.DeepEqual(r.Blockers, []string{"UNSUPPORTED_ARCH"}) {
		t.Fatalf("blockers=%#v", r.Blockers)
	}
}

func TestInspectELFDependenciesSuccessAndTransitiveClosure(t *testing.T) {
	files := map[string][]byte{
		"game":            elfBytes(t, elf.ET_EXEC, elf.EM_X86_64, "libfoo.so.1", "", "$ORIGIN/lib", ""),
		"lib/libfoo.so.1": elfBytes(t, elf.ET_DYN, elf.EM_X86_64, "libbar.so.2", "libfoo.so.1", "", ""),
		"lib/libbar.so.2": elfBytes(t, elf.ET_DYN, elf.EM_X86_64, "", "libbar.so.2", "", ""),
	}
	r := inspectZip(t, zipWithELF(t, files))
	if r.Dependencies == nil {
		t.Fatal("missing dependencies")
	}
	if len(r.Dependencies.Files) != 3 {
		t.Fatalf("files=%#v", r.Dependencies.Files)
	}
	if !reflect.DeepEqual(r.Dependencies.BundledSONAMEs, []string{"libbar.so.2", "libfoo.so.1"}) {
		t.Fatalf("bundled=%#v", r.Dependencies.BundledSONAMEs)
	}
	if len(r.Dependencies.ExternalSONAMEs) != 0 {
		t.Fatalf("external=%#v", r.Dependencies.ExternalSONAMEs)
	}
	game := r.Dependencies.Files[0]
	if game.Path != "game" || game.Type != "executable" || game.Architecture != "amd64" {
		t.Fatalf("game=%#v", game)
	}
	if !reflect.DeepEqual(game.RPath, []string{"$ORIGIN/lib"}) {
		t.Fatalf("rpath=%#v", game.RPath)
	}
	if !reflect.DeepEqual(game.ResolvedNeeded, []string{"libfoo.so.1"}) {
		t.Fatalf("resolved=%#v", game.ResolvedNeeded)
	}
	if !reflect.DeepEqual(game.TransitiveDeps, []string{"libbar.so.2", "libfoo.so.1"}) {
		t.Fatalf("transitive=%#v", game.TransitiveDeps)
	}
}

func TestInspectELFDependenciesReportsExternalSONAMEs(t *testing.T) {
	files := map[string][]byte{
		"game": elfBytes(t, elf.ET_EXEC, elf.EM_X86_64, "libmissing.so.1", "", "", ""),
	}
	r := inspectZip(t, zipWithELF(t, files))
	if !reflect.DeepEqual(r.Dependencies.ExternalSONAMEs, []string{"libmissing.so.1"}) {
		t.Fatalf("external=%#v", r.Dependencies.ExternalSONAMEs)
	}
	if len(r.Dependencies.BundledSONAMEs) != 0 {
		t.Fatalf("bundled=%#v", r.Dependencies.BundledSONAMEs)
	}
	game := r.Dependencies.Files[0]
	if !reflect.DeepEqual(game.ResolvedNeeded, []string{}) {
		t.Fatalf("resolved=%#v", game.ResolvedNeeded)
	}
}

func TestInspectELFDependenciesArchitectureDetection(t *testing.T) {
	for _, tc := range []struct {
		machine elf.Machine
		want    string
	}{
		{elf.EM_X86_64, "amd64"},
		{elf.EM_AARCH64, "arm64"},
		{elf.EM_386, "machine-0x3"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			files := map[string][]byte{
				"bin": elfBytes(t, elf.ET_EXEC, tc.machine, "", "", "", ""),
			}
			r := inspectZip(t, zipWithELF(t, files))
			if r.Dependencies.Files[0].Architecture != tc.want {
				t.Fatalf("got %q, want %q", r.Dependencies.Files[0].Architecture, tc.want)
			}
		})
	}
}

func TestInspectELFDependenciesIsDeterministic(t *testing.T) {
	files := map[string][]byte{
		"game":              elfBytes(t, elf.ET_EXEC, elf.EM_X86_64, "libfoo.so.1", "", "", ""),
		"lib/libfoo.so.1":   elfBytes(t, elf.ET_DYN, elf.EM_X86_64, "libbar.so.2", "libfoo.so.1", "", ""),
		"lib/libbar.so.2":   elfBytes(t, elf.ET_DYN, elf.EM_X86_64, "", "libbar.so.2", "", ""),
		"lib/libignored.so": elfBytes(t, elf.ET_DYN, elf.EM_X86_64, "", "libignored.so", "", ""),
	}
	data := zipWithELF(t, files)
	r1 := inspectZip(t, data)
	r2 := inspectZip(t, data)
	b1, _ := json.Marshal(r1.Dependencies)
	b2, _ := json.Marshal(r2.Dependencies)
	if !bytes.Equal(b1, b2) {
		t.Fatalf("non-deterministic output:\n%s\n%s", b1, b2)
	}
}

func TestInspectELFDependenciesSkipsNonELFAndMalformed(t *testing.T) {
	files := map[string][]byte{
		"readme.txt": []byte("not an elf"),
		"truncated":  []byte{0x7f, 'E', 'L', 'F'},
	}
	r := inspectZip(t, zipWithELF(t, files))
	if len(r.Dependencies.Files) != 1 {
		t.Fatalf("files=%#v", r.Dependencies.Files)
	}
	if r.Dependencies.Files[0].Path != "truncated" || r.Dependencies.Files[0].Error == "" {
		t.Fatalf("truncated=%#v", r.Dependencies.Files[0])
	}
}

func TestInspectELFDependenciesDoesNotFollowSymlinks(t *testing.T) {
	d := t.TempDir()
	outside := filepath.Join(d, "outside")
	if err := os.WriteFile(outside, elfBytes(t, elf.ET_EXEC, elf.EM_X86_64, "", "", "", ""), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(d, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "game")); err != nil {
		t.Fatal(err)
	}
	deps, err := inspectELFDependencies(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps.Files) != 0 {
		t.Fatalf("symlink was followed: %#v", deps.Files)
	}
}

func TestInspectELFDependenciesRejectsOversizedFile(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(root, "big")
	f, err := os.OpenFile(big, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0x7f, 'E', 'L', 'F'}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, maxELFFileBytes+1); err != nil {
		t.Fatal(err)
	}
	deps, err := inspectELFDependencies(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps.Files) != 1 || deps.Files[0].Error == "" {
		t.Fatalf("oversized file not rejected: %#v", deps.Files)
	}
}

func TestELFDependenciesStringIsDeterministic(t *testing.T) {
	d := &ELFDependencies{
		Files: []ELFDependencyFile{
			{Path: "b", Type: "shared-object", SONAME: "libb.so"},
			{Path: "a", Type: "executable", Needed: []string{"libb.so"}},
		},
		BundledSONAMEs:  []string{"libb.so"},
		ExternalSONAMEs: []string{"liba.so"},
	}
	s1 := d.String()
	s2 := d.String()
	if s1 != s2 {
		t.Fatalf("non-deterministic string:\n%s\n%s", s1, s2)
	}
}

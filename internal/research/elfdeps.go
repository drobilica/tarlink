package research

import (
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxELFFileBytes int64 = 256 << 20

// ELFDependencyFile is deterministic, execution-free dependency evidence for
// one ELF object discovered inside an inspected archive.
type ELFDependencyFile struct {
	Path               string   `json:"path"`
	Type               string   `json:"type"`
	Architecture       string   `json:"architecture,omitempty"`
	ArchitectureStatus string   `json:"architecture_status,omitempty"`
	SONAME             string   `json:"soname,omitempty"`
	Needed             []string `json:"needed,omitempty"`
	RPath              []string `json:"rpath,omitempty"`
	RunPath            []string `json:"runpath,omitempty"`
	ResolvedNeeded     []string `json:"resolved_needed,omitempty"`
	TransitiveDeps     []string `json:"transitive_deps,omitempty"`
	Error              string   `json:"error,omitempty"`
}

// ELFDependencies aggregates advisory dependency evidence for an extracted
// archive. It never executes files and reports only static ELF metadata.
type ELFDependencies struct {
	Files           []ELFDependencyFile `json:"files"`
	BundledSONAMEs  []string            `json:"bundled_sonames,omitempty"`
	ExternalSONAMEs []string            `json:"external_sonames,omitempty"`
}

// String renders a deterministic human summary of the dependency scan.
func (d *ELFDependencies) String() string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ELF files scanned: %d\n", len(d.Files))
	if len(d.BundledSONAMEs) != 0 {
		fmt.Fprintf(&b, "Bundled sonames: %s\n", strings.Join(d.BundledSONAMEs, ", "))
	}
	if len(d.ExternalSONAMEs) != 0 {
		fmt.Fprintf(&b, "External sonames: %s\n", strings.Join(d.ExternalSONAMEs, ", "))
	}
	for _, f := range d.Files {
		if f.Error != "" {
			fmt.Fprintf(&b, "%s: error: %s\n", f.Path, f.Error)
			continue
		}
		fmt.Fprintf(&b, "%s: %s %s", f.Path, f.Type, f.Architecture)
		if f.SONAME != "" {
			fmt.Fprintf(&b, " soname=%s", f.SONAME)
		}
		if len(f.Needed) != 0 {
			fmt.Fprintf(&b, " needed=%s", strings.Join(f.Needed, ","))
		}
		if len(f.RPath) != 0 {
			fmt.Fprintf(&b, " rpath=%s", strings.Join(f.RPath, ":"))
		}
		if len(f.RunPath) != 0 {
			fmt.Fprintf(&b, " runpath=%s", strings.Join(f.RunPath, ":"))
		}
		if len(f.TransitiveDeps) != 0 {
			fmt.Fprintf(&b, " transitive=%s", strings.Join(f.TransitiveDeps, ","))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// inspectELFDependencies scans root for regular ELF files and returns
// deterministic advisory dependency evidence. It never follows symlinks or
// executes files.
func inspectELFDependencies(root string, expectedArch ...string) (*ELFDependencies, error) {
	if root == "" {
		return nil, errors.New("elf dependency root is empty")
	}
	var files []ELFDependencyFile
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry == nil || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxELFFileBytes {
			files = append(files, ELFDependencyFile{Path: rel, Error: "ELF file exceeds size limit"})
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			files = append(files, ELFDependencyFile{Path: rel, Error: fmt.Sprintf("open: %v", err)})
			return nil
		}
		var magic [4]byte
		if _, err := io.ReadFull(file, magic[:]); err != nil {
			_ = file.Close()
			return nil
		}
		if string(magic[:]) != "\x7fELF" {
			_ = file.Close()
			return nil
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			_ = file.Close()
			files = append(files, ELFDependencyFile{Path: rel, Error: fmt.Sprintf("seek: %v", err)})
			return nil
		}
		parsed, err := elf.NewFile(file)
		if err != nil {
			_ = file.Close()
			files = append(files, ELFDependencyFile{Path: rel, Error: fmt.Sprintf("parse ELF: %v", err)})
			return nil
		}
		want := ""
		if len(expectedArch) != 0 {
			want = expectedArch[0]
		}
		files = append(files, describeELF(rel, parsed, want))
		_ = file.Close()
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return buildELFReport(files), nil
}

func describeELF(path string, f *elf.File, expectedArch string) ELFDependencyFile {
	result := ELFDependencyFile{
		Path:         path,
		Type:         elfTypeString(f.Type),
		Architecture: elfMachineString(f.Machine),
	}
	if result.Architecture == "amd64" || result.Architecture == "arm64" {
		result.ArchitectureStatus = "supported"
	} else {
		result.ArchitectureStatus = "unsupported"
	}
	if expectedArch != "" && result.Architecture != expectedArch {
		result.ArchitectureStatus = "mismatch"
	}
	needed, _ := f.DynString(elf.DT_NEEDED)
	soname, _ := f.DynString(elf.DT_SONAME)
	if len(soname) > 0 {
		result.SONAME = soname[0]
	}
	rpath, _ := f.DynString(elf.DT_RPATH)
	runpath, _ := f.DynString(elf.DT_RUNPATH)
	result.Needed = sortedUniqueStrings(needed)
	result.RPath = splitPathList(rpath)
	result.RunPath = splitPathList(runpath)
	return result
}

func buildELFReport(files []ELFDependencyFile) *ELFDependencies {
	bundled := make(map[string]bool)
	for _, f := range files {
		if f.SONAME != "" {
			bundled[f.SONAME] = true
		}
	}
	external := make(map[string]bool)
	for i := range files {
		resolved := make(map[string]bool)
		transitive := make(map[string]bool)
		walkDependencies(files[i].Needed, bundled, files, resolved, transitive, external, map[string]bool{})
		files[i].ResolvedNeeded = sortedBoolKeys(resolved)
		files[i].TransitiveDeps = sortedBoolKeys(transitive)
	}
	return &ELFDependencies{
		Files:           files,
		BundledSONAMEs:  sortedBoolKeys(bundled),
		ExternalSONAMEs: sortedBoolKeys(external),
	}
}

func walkDependencies(needed []string, bundled map[string]bool, files []ELFDependencyFile, resolved, transitive, external, seen map[string]bool) {
	for _, soname := range needed {
		if !bundled[soname] {
			external[soname] = true
			continue
		}
		resolved[soname] = true
		transitive[soname] = true
		collectTransitive(soname, bundled, files, transitive, external, seen)
	}
}

func collectTransitive(soname string, bundled map[string]bool, files []ELFDependencyFile, transitive, external, seen map[string]bool) {
	if seen[soname] {
		return
	}
	seen[soname] = true
	for _, f := range files {
		if f.SONAME != soname {
			continue
		}
		for _, needed := range f.Needed {
			if bundled[needed] {
				transitive[needed] = true
				collectTransitive(needed, bundled, files, transitive, external, seen)
			} else {
				external[needed] = true
			}
		}
	}
}

func elfTypeString(t elf.Type) string {
	switch t {
	case elf.ET_EXEC:
		return "executable"
	case elf.ET_DYN:
		return "shared-object"
	default:
		return "unknown"
	}
}

func elfMachineString(m elf.Machine) string {
	switch m {
	case elf.EM_X86_64:
		return "amd64"
	case elf.EM_AARCH64:
		return "arm64"
	default:
		return fmt.Sprintf("machine-0x%x", uint16(m))
	}
}

func splitPathList(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0)
	for _, v := range values {
		for _, part := range strings.Split(v, ":") {
			part = strings.TrimSpace(part)
			if part != "" && !seen[part] {
				seen[part] = true
				result = append(result, part)
			}
		}
	}
	sort.Strings(result)
	return result
}

func sortedBoolKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedUniqueStrings(s []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(s))
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	sort.Strings(result)
	return result
}

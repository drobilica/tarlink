package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
	"github.com/drobilica/tarlink/internal/research"
)

type RegistryIconOptions struct {
	Root string
	App  string
	Fix  bool
}

type RegistryIconResult struct {
	App       string   `json:"app"`
	Status    string   `json:"status"`
	Manifests []string `json:"manifests"`
	URL       string   `json:"url,omitempty"`
	SHA256    string   `json:"sha256,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type RegistryIconReport struct {
	Results []RegistryIconResult `json:"results"`
	Missing int                  `json:"missing"`
	Fixed   int                  `json:"fixed"`
}

const maxIconCandidates = 8

var registryIconPaths = []string{
	"icon.png", "misc/logo/icon.png", "assets/graphics/icons/icon.png",
	"assets/icon.png", "resources/icon.png", "logo.png",
}

// RegistryIcons audits icon declarations entirely from the validated local
// registry. Network access and manifest writes occur only when Fix is true.
func (core *Core) RegistryIcons(ctx context.Context, options RegistryIconOptions) (RegistryIconReport, error) {
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return RegistryIconReport{}, err
	}
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		return RegistryIconReport{}, err
	}
	ids := make([]string, 0, len(catalog.Variants))
	for id := range catalog.Variants {
		if options.App == "" || options.App == id {
			ids = append(ids, id)
		}
	}
	if options.App != "" && len(ids) == 0 {
		return RegistryIconReport{}, fmt.Errorf("application %q is not in the registry", options.App)
	}
	sort.Strings(ids)
	report := RegistryIconReport{Results: make([]RegistryIconResult, 0, len(ids))}
	for _, id := range ids {
		variants := catalog.Variants[id]
		platforms := make([]manifest.Platform, 0, len(variants))
		for platform := range variants {
			platforms = append(platforms, platform)
		}
		sort.Slice(platforms, func(i, j int) bool {
			return platforms[i].OS+"/"+platforms[i].Arch < platforms[j].OS+"/"+platforms[j].Arch
		})
		item := RegistryIconResult{App: id}
		missing := false
		declared := false
		for _, platform := range platforms {
			m := variants[platform]
			item.Manifests = append(item.Manifests, filepath.ToSlash(filepath.Join("apps", id, platform.OS+"-"+platform.Arch+".yaml")))
			if m.Desktop.Enabled {
				missing = missing || m.Desktop.Icon.IsZero()
				declared = declared || !m.Desktop.Icon.IsZero()
			}
		}
		if !missing {
			item.Status = "present"
			report.Results = append(report.Results, item)
			continue
		}
		item.Status = "missing"
		report.Missing++
		if options.Fix {
			if declared {
				item.Error = "platform manifests do not share one missing icon state"
				report.Results = append(report.Results, item)
				continue
			}
			icon, fixErr := core.fixRegistryIcon(ctx, root, variants, item.Manifests)
			if fixErr != nil {
				item.Error = fixErr.Error()
			} else {
				item.Status, item.URL, item.SHA256 = "fixed", icon.URL, icon.SHA256
				report.Fixed++
				report.Missing--
			}
		}
		report.Results = append(report.Results, item)
	}
	return report, nil
}

type fixedRegistryIcon struct{ URL, SHA256 string }

func (core *Core) fixRegistryIcon(ctx context.Context, root string, variants map[manifest.Platform]*manifest.Manifest, paths []string) (fixedRegistryIcon, error) {
	var repository, tag string
	for _, item := range variants {
		repo, releaseTag, err := githubReleaseIdentity(item.Release.URL)
		if err != nil {
			return fixedRegistryIcon{}, err
		}
		if repository == "" {
			repository, tag = repo, releaseTag
		} else if repository != repo || tag != releaseTag {
			return fixedRegistryIcon{}, errors.New("platform releases do not share one GitHub repository tag")
		}
	}
	client := &research.Client{}
	if core.syncer != nil && core.syncer.Client != nil {
		client.HTTP = core.syncer.Client.HTTP
	}
	type candidate struct {
		file  research.RepositoryFile
		data  []byte
		score int
	}
	valid := make([]candidate, 0, maxIconCandidates)
	for _, candidatePath := range registryIconPaths {
		file, discoverErr := client.DiscoverRepositoryFile(ctx, repository, tag, candidatePath)
		if discoverErr != nil {
			var apiErr *research.APIError
			if errors.As(discoverErr, &apiErr) && apiErr.Kind == research.APIErrorNotFound {
				continue
			}
			return fixedRegistryIcon{}, discoverErr
		}
		data, fetchErr := client.FetchRepositoryFile(ctx, file)
		if fetchErr != nil {
			continue
		}
		if _, sizeErr := manifest.IconSizeFromPNG(data); sizeErr != nil {
			continue
		}
		valid = append(valid, candidate{file: file, data: data, score: iconPathScore(file.Path)})
		if len(valid) >= maxIconCandidates {
			break
		}
	}
	if len(valid) == 0 {
		files, discoverErr := client.DiscoverRepositoryIconCandidates(ctx, repository, tag)
		if discoverErr != nil {
			return fixedRegistryIcon{}, discoverErr
		}
		sort.Slice(files, func(i, j int) bool {
			si, sj := fallbackTreeScore(files[i].Path), fallbackTreeScore(files[j].Path)
			if si != sj {
				return si > sj
			}
			return files[i].Path < files[j].Path
		})
		// The tree is only a shortlist. Download and validate these bounded
		// candidates before ranking them; the tree itself is untrusted metadata.
		for _, file := range files {
			if len(valid) >= maxIconCandidates {
				break
			}
			data, fetchErr := client.FetchRepositoryFile(ctx, file)
			if fetchErr != nil {
				continue
			}
			size, sizeErr := manifest.IconSizeFromPNG(data)
			if sizeErr != nil {
				continue
			}
			valid = append(valid, candidate{file: file, data: data, score: fallbackIconScore(file.Path, size)})
		}
	}
	if len(valid) == 0 {
		return fixedRegistryIcon{}, errors.New("no valid PNG icon candidate found")
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].score != valid[j].score {
			return valid[i].score > valid[j].score
		}
		return valid[i].file.Path < valid[j].file.Path
	})
	if len(valid) > 1 && valid[0].score == valid[1].score {
		return fixedRegistryIcon{}, fmt.Errorf("ambiguous icon candidates: %s, %s", valid[0].file.Path, valid[1].file.Path)
	}
	file, data := valid[0].file, valid[0].data
	digest := sha256.Sum256(data)
	icon := fixedRegistryIcon{URL: file.URL, SHA256: hex.EncodeToString(digest[:])}
	type replacement struct {
		path, original string
		data           []byte
	}
	replacements := make([]replacement, 0, len(paths))
	for _, relative := range paths {
		path := filepath.Join(root, filepath.FromSlash(relative))
		original, err := os.ReadFile(path)
		if err != nil {
			return fixedRegistryIcon{}, err
		}
		updated, err := addRemoteIcon(original, icon)
		if err != nil {
			return fixedRegistryIcon{}, err
		}
		replacements = append(replacements, replacement{path: path, original: string(original), data: updated})
	}
	for i, replacement := range replacements {
		if err := replaceManifest(replacement.path, replacement.data); err != nil {
			for rollback := i - 1; rollback >= 0; rollback-- {
				_ = replaceManifest(replacements[rollback].path, []byte(replacements[rollback].original))
			}
			return fixedRegistryIcon{}, err
		}
	}
	return icon, nil
}

func githubReleaseIdentity(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return "", "", errors.New("release is not a GitHub HTTPS release asset")
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) < 6 || parts[2] != "releases" || parts[3] != "download" {
		return "", "", errors.New("release is not a GitHub release asset")
	}
	tag, err := url.PathUnescape(parts[4])
	if err != nil || tag == "" {
		return "", "", errors.New("GitHub release tag is invalid")
	}
	return parts[0] + "/" + parts[1], tag, nil
}

func iconPathScore(value string) int {
	switch value {
	case "icon.png":
		return 100
	case "misc/logo/icon.png", "assets/graphics/icons/icon.png":
		return 98
	case "assets/icon.png", "resources/icon.png":
		return 95
	case "logo.png":
		return 90
	default:
		return 0
	}
}

func fallbackIconScore(value string, size int) int {
	parts := strings.Split(value, "/")
	base := strings.ToLower(parts[len(parts)-1])
	category := 0
	if base == "icon.png" {
		category = 3
	} else {
		for _, part := range parts[:len(parts)-1] {
			if strings.EqualFold(part, "icon") || strings.EqualFold(part, "icons") {
				category = 2
				break
			}
		}
		if category == 0 && base == "logo.png" {
			category = 1
		}
	}
	return category*100 + iconDimensionScore(size)
}

func fallbackTreeScore(value string) int {
	if !strings.EqualFold(filepath.Ext(value), ".png") {
		return 0
	}
	parts := strings.Split(value, "/")
	base := strings.ToLower(parts[len(parts)-1])
	category := 0
	if base == "icon.png" {
		category = 3
	} else {
		for _, part := range parts[:len(parts)-1] {
			if strings.EqualFold(part, "icon") || strings.EqualFold(part, "icons") {
				category = 2
				break
			}
		}
		if category == 0 && base == "logo.png" {
			category = 1
		}
	}
	if category == 0 {
		return 0
	}
	dimension := 0
	if strings.HasSuffix(base, ".png") {
		if n, err := strconv.Atoi(strings.TrimSuffix(base, ".png")); err == nil {
			dimension = iconDimensionScore(n)
		}
	}
	return category*100 + dimension
}

func iconDimensionScore(size int) int {
	switch size {
	case 512:
		return 4
	case 256:
		return 3
	case 128:
		return 2
	default:
		return 1
	}
}

func addRemoteIcon(data []byte, icon fixedRegistryIcon) ([]byte, error) {
	parsed, err := manifest.ParseBytes(data)
	if err != nil {
		return nil, err
	}
	if !parsed.Desktop.Icon.IsZero() {
		return nil, errors.New("manifest icon is no longer missing")
	}
	lines := strings.SplitAfter(string(data), "\n")
	desktopLine := -1
	indent := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
		if trimmed == "desktop:" {
			desktopLine = i
			indent = len(line) - len(strings.TrimLeft(line, " "))
			break
		}
	}
	if desktopLine < 0 {
		return nil, errors.New("manifest desktop mapping is missing")
	}
	insert := len(lines)
	for i := desktopLine + 1; i < len(lines); i++ {
		line := strings.TrimSuffix(lines[i], "\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		level := len(line) - len(strings.TrimLeft(line, " "))
		if level <= indent {
			insert = i
			break
		}
	}
	addition := strings.Repeat(" ", indent+2) + "icon:\n" + strings.Repeat(" ", indent+4) + "url: " + icon.URL + "\n" + strings.Repeat(" ", indent+4) + "sha256: " + icon.SHA256 + "\n"
	lines = append(lines[:insert], append([]string{addition}, lines[insert:]...)...)
	output := []byte(strings.Join(lines, ""))
	if _, err := manifest.ParseBytes(output); err != nil {
		return nil, fmt.Errorf("validate updated manifest: %w", err)
	}
	return output, nil
}

func replaceManifest(path string, data []byte) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("manifest is not a regular file")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tarlink-icon-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(info.Mode().Perm()); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Close()
	} else {
		_ = temporary.Close()
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

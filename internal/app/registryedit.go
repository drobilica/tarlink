package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/locking"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
	"go.yaml.in/yaml/v3"
)

// RegistryEditingService changes an explicitly selected local registry tree.
// It never publishes metadata, changes Git state, or installs applications.
type RegistryEditingService interface {
	EditRegistry(context.Context, RegistryEditOptions) (RegistryEditReport, error)
}

type RegistryEditOptions struct {
	Root    string
	Add     []byte
	ID      string
	Enabled *bool
	DryRun  bool
}

type RegistryEditReport struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Path          string `json:"path"`
	Action        string `json:"action"`
	Changed       bool   `json:"changed"`
	Written       bool   `json:"written"`
	DryRun        bool   `json:"dry_run"`
}

func (m *Maintainer) EditRegistry(ctx context.Context, options RegistryEditOptions) (RegistryEditReport, error) {
	report, err := editRegistry(ctx, options, syncRegistryDirectory)
	return report, classify("registry edit", err)
}

func editRegistry(ctx context.Context, options RegistryEditOptions, syncDirectory func(string) error) (RegistryEditReport, error) {
	report := RegistryEditReport{SchemaVersion: 1, DryRun: options.DryRun}
	if options.Root == "" || (len(options.Add) != 0) == (options.Enabled != nil) || len(options.Add) != 0 && options.ID != "" {
		return report, &Error{Code: CodeInvalidArguments, Err: errors.New("select a registry and exactly one addition or enabled-state change")}
	}
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return report, err
	}
	if err := filesystem.CheckOwnedDirectoryWithin(filepath.VolumeName(root)+string(filepath.Separator), root); err != nil {
		return report, err
	}
	// Lock the existing root inode: no lock file is added to the data-only tree.
	lock, err := locking.AcquireDirectoryWithTimeout(ctx, root, locking.DefaultTimeout)
	if err != nil {
		return report, err
	}
	defer lock.Release()
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		return report, err
	}
	var previous, updated []byte
	if len(options.Add) != 0 {
		document, err := manifest.ParseBytes(options.Add)
		if err != nil {
			return report, err
		}
		report.ID, report.Action, updated = document.ID, "add", options.Add
		if _, exists := catalog.Variants[report.ID]; exists {
			return report, &Error{Code: CodeConflict, Err: fmt.Errorf("application %q already exists; additions never overwrite manifests", report.ID)}
		}
	} else {
		if !manifest.ValidID(options.ID) {
			return report, &Error{Code: CodeInvalidArguments, Err: errors.New("invalid application ID")}
		}
		report.ID, report.Action = options.ID, "disable"
		if *options.Enabled {
			report.Action = "enable"
		}
		if _, exists := catalog.Variants[report.ID]; !exists {
			return report, fmt.Errorf("application %q is not in the registry", report.ID)
		}
		previous, err = readRegistryManifest(filepath.Join(root, "apps", report.ID, "manifest.yaml"))
		if err != nil {
			return report, err
		}
		updated, err = setRegistryEnabled(previous, *options.Enabled)
		if err != nil {
			return report, err
		}
	}
	report.Path = filepath.Join(root, "apps", report.ID, "manifest.yaml")
	report.Changed = !bytes.Equal(previous, updated)
	if !report.Changed {
		return report, nil
	}
	stageParent := root
	if options.DryRun {
		stageParent = ""
	}
	stage, err := os.MkdirTemp(stageParent, ".tarlink-registry-edit-*")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(stage)
	for id := range catalog.Variants {
		if err := copyRegistryManifest(root, stage, "apps", id); err != nil {
			return report, err
		}
	}
	for id := range catalog.Runtimes {
		if err := copyRegistryManifest(root, stage, "runtimes", id); err != nil {
			return report, err
		}
	}
	stagedPath := filepath.Join(stage, "apps", report.ID, "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(stagedPath), 0755); err != nil {
		return report, err
	}
	mode := os.FileMode(0644)
	if len(previous) != 0 {
		info, err := os.Lstat(report.Path)
		if err != nil {
			return report, err
		}
		mode = info.Mode().Perm()
	}
	if err := writeRegistryManifest(stagedPath, updated, mode); err != nil {
		return report, err
	}
	if _, err := registry.ValidateTree(stage); err != nil {
		return report, fmt.Errorf("validate proposed registry: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if options.DryRun {
		return report, nil
	}
	// Revalidate after staging, and never replace a concurrently edited target.
	current, err := registry.ValidateTree(root)
	if err != nil {
		return report, err
	}
	if current.Revision != catalog.Revision {
		return report, errors.New("registry changed while preparing the edit; retry with the current tree")
	}
	if err := filesystem.CheckOwnedDirectoryWithin(root, filepath.Join(root, "apps")); err != nil {
		return report, err
	}
	if len(previous) == 0 {
		destination := filepath.Dir(report.Path)
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return report, errors.New("application destination already exists or cannot be inspected")
		}
		if err := syncDirectory(filepath.Dir(stagedPath)); err != nil {
			return report, err
		}
		err = os.Rename(filepath.Dir(stagedPath), destination)
	} else {
		if err := filesystem.CheckOwnedDirectoryWithin(root, filepath.Dir(report.Path)); err != nil {
			return report, err
		}
		latest, err := readRegistryManifest(report.Path)
		if err != nil {
			return report, err
		}
		if !bytes.Equal(latest, previous) {
			return report, errors.New("manifest changed while preparing the edit; retry with the current file")
		}
		err = os.Rename(stagedPath, report.Path)
	}
	if err != nil {
		return report, err
	}
	report.Written = true
	parent := filepath.Dir(report.Path)
	if len(previous) == 0 {
		parent = filepath.Dir(parent)
	}
	if err := syncDirectory(parent); err != nil {
		return report, fmt.Errorf("edit published but directory flush failed: %w", err)
	}
	return report, nil
}

func readRegistryManifest(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("registry manifest must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, manifest.MaxManifestBytes+1))
	if err == nil && len(data) > manifest.MaxManifestBytes {
		err = errors.New("registry manifest exceeds size limit")
	}
	return data, err
}

func copyRegistryManifest(root, stage, kind, id string) error {
	data, err := readRegistryManifest(filepath.Join(root, kind, id, "manifest.yaml"))
	if err != nil {
		return err
	}
	path := filepath.Join(stage, kind, id, "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func writeRegistryManifest(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func syncRegistryDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// Change just the boolean token, preserving all release metadata and comments.
func setRegistryEnabled(data []byte, enabled bool) ([]byte, error) {
	document, err := manifest.ParseBytes(data)
	if err != nil {
		return nil, err
	}
	current := document.Enabled == nil || *document.Enabled
	if current == enabled {
		return data, nil
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, err
	}
	mapping := node.Content[0]
	value := "false"
	if enabled {
		value = "true"
	}
	lines := strings.SplitAfter(string(data), "\n")
	offset := func(n *yaml.Node) int {
		result := 0
		for _, line := range lines[:n.Line-1] {
			result += len(line)
		}
		return result + len(string([]rune(lines[n.Line-1])[:n.Column-1]))
	}
	var updated []byte
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "enabled" {
			n := mapping.Content[i+1]
			start := offset(n)
			updated = append(append(append([]byte{}, data[:start]...), value...), data[start+len(n.Value):]...)
			break
		}
	}
	if updated == nil {
		start := offset(mapping)
		addition := "enabled: " + value + "\n"
		if mapping.Style&yaml.FlowStyle != 0 {
			start++ // after the opening brace
			addition = "enabled: " + value + ", "
		}
		updated = append(append(append([]byte{}, data[:start]...), addition...), data[start:]...)
	}
	if _, err := manifest.ParseBytes(updated); err != nil {
		return nil, fmt.Errorf("validate enabled-state edit: %w", err)
	}
	return updated, nil
}

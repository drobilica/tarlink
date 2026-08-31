package app

// The lock file is deliberately an installed-state snapshot.  It contains no
// artifact URLs or checksums and therefore cannot become an alternate package
// authority; replay always resolves through the validated registry catalog.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/state"
	"go.yaml.in/yaml/v3"
)

const (
	LockSchema      = 1
	MaxLockBytes    = 1 << 20
	MaxLockEntries  = 4096
	LockPlatformAMD = "linux/amd64"
	LockPlatformARM = "linux/arm64"
)

type LockEntry struct {
	ID          string `yaml:"id"`
	Channel     string `yaml:"channel"`
	Version     string `yaml:"version"`
	Fingerprint string `yaml:"fingerprint"`
}

type Lockfile struct {
	Schema       int         `yaml:"schema"`
	Platform     string      `yaml:"platform"`
	Applications []LockEntry `yaml:"applications"`
}

func (l Lockfile) Validate() error {
	if l.Schema != LockSchema {
		return fmt.Errorf("unsupported lock schema %d", l.Schema)
	}
	if l.Platform != LockPlatformAMD && l.Platform != LockPlatformARM {
		return fmt.Errorf("unsupported lock platform %q", l.Platform)
	}
	if len(l.Applications) > MaxLockEntries {
		return errors.New("lock file has too many applications")
	}
	seen := make(map[string]bool, len(l.Applications))
	last := ""
	for _, e := range l.Applications {
		if err := filesystem.ValidateID(e.ID); err != nil {
			return fmt.Errorf("lock application %q: %w", e.ID, err)
		}
		if last != "" && e.ID <= last {
			return fmt.Errorf("lock applications must be sorted by id")
		}
		last = e.ID
		if seen[e.ID] {
			return fmt.Errorf("duplicate lock application %q", e.ID)
		}
		seen[e.ID] = true
		if err := state.ValidateChannel(e.Channel); err != nil {
			return fmt.Errorf("lock application %s channel: %w", e.ID, err)
		}
		if err := filesystem.ValidateVersion(e.Version); err != nil {
			return fmt.Errorf("lock application %s version: %w", e.ID, err)
		}
		if !validLockFingerprint(e.Fingerprint) {
			return fmt.Errorf("lock application %s fingerprint is invalid", e.ID)
		}
	}
	return nil
}

func validLockFingerprint(v string) bool {
	if len(v) != 71 || !strings.HasPrefix(v, "sha256:") {
		return false
	}
	for _, r := range v[7:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func ParseLock(data []byte) (Lockfile, error) {
	if len(data) == 0 || len(data) > MaxLockBytes {
		return Lockfile{}, errors.New("lock file is empty or too large")
	}
	var node yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&node); err != nil {
		return Lockfile{}, fmt.Errorf("invalid lock YAML: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return Lockfile{}, errors.New("lock file must contain exactly one YAML document")
	}
	if node.Kind != yaml.DocumentNode || len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return Lockfile{}, errors.New("lock file must be a mapping")
	}
	if hasLockAlias(&node) {
		return Lockfile{}, errors.New("lock file aliases are not allowed")
	}
	allowed := map[string]bool{"schema": true, "platform": true, "applications": true}
	seen := map[string]bool{}
	root := node.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		k := root.Content[i]
		if k.Kind != yaml.ScalarNode || !allowed[k.Value] || seen[k.Value] {
			return Lockfile{}, fmt.Errorf("invalid or duplicate lock field %q", k.Value)
		}
		seen[k.Value] = true
	}
	if len(seen) != 3 {
		return Lockfile{}, errors.New("lock file is missing required fields")
	}
	if !lockScalar(mappingValue(root, "schema"), "!!int") || !lockScalar(mappingValue(root, "platform"), "!!str") {
		return Lockfile{}, errors.New("lock file fields have invalid types")
	}
	apps := mappingValue(root, "applications")
	if apps == nil || apps.Kind != yaml.SequenceNode {
		return Lockfile{}, errors.New("lock applications must be a sequence")
	}
	for _, entry := range apps.Content {
		if entry.Kind != yaml.MappingNode {
			return Lockfile{}, errors.New("lock application must be a mapping")
		}
		fields := map[string]bool{"id": true, "channel": true, "version": true, "fingerprint": true}
		entrySeen := map[string]bool{}
		for i := 0; i < len(entry.Content); i += 2 {
			k := entry.Content[i]
			if k.Kind != yaml.ScalarNode || !fields[k.Value] || entrySeen[k.Value] {
				return Lockfile{}, fmt.Errorf("invalid or duplicate lock application field %q", k.Value)
			}
			entrySeen[k.Value] = true
		}
		if len(entrySeen) != 4 {
			return Lockfile{}, errors.New("lock application is missing required fields")
		}
		for _, field := range []string{"id", "channel", "version", "fingerprint"} {
			if !lockScalar(mappingValue(entry, field), "!!str") {
				return Lockfile{}, fmt.Errorf("lock application field %q must be a string", field)
			}
		}
	}
	var lock Lockfile
	if err := yaml.Unmarshal(data, &lock); err != nil {
		return Lockfile{}, fmt.Errorf("invalid lock file: %w", err)
	}
	if err := lock.Validate(); err != nil {
		return Lockfile{}, err
	}
	return lock, nil
}

func lockScalar(node *yaml.Node, tag string) bool {
	return node != nil && node.Kind == yaml.ScalarNode && node.Tag == tag
}

func mappingValue(node *yaml.Node, name string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == name {
			return node.Content[i+1]
		}
	}
	return nil
}

func hasLockAlias(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.AliasNode {
		return true
	}
	for _, child := range node.Content {
		if hasLockAlias(child) {
			return true
		}
	}
	return false
}

func (core *Core) WriteLock(ctx context.Context, output string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	states, err := core.installedStates()
	if err != nil {
		return err
	}
	goos, goarch := core.platform()
	platform := goos + "/" + goarch
	lock := Lockfile{Schema: LockSchema, Platform: platform, Applications: make([]LockEntry, 0, len(states))}
	for _, s := range states {
		lock.Applications = append(lock.Applications, LockEntry{ID: s.App, Channel: s.Channel, Version: s.Current, Fingerprint: s.CurrentFingerprint})
	}
	sort.Slice(lock.Applications, func(i, j int) bool { return lock.Applications[i].ID < lock.Applications[j].ID })
	if err := lock.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(lock)
	if err != nil {
		return err
	}
	if output == "" {
		output = "tarlink.lock"
	}
	return atomicWriteLock(output, data)
}

func atomicWriteLock(output string, data []byte) error {
	dir := filepath.Dir(output)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tarlink-lock-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmpName, output); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// InstallLock applies exact entries from a lock through the official catalog.
// Existing state is updated with Explicit=true, which lets the installer
// converge pinned applications while retaining their pinned bit.
func (core *Core) InstallLock(ctx context.Context, input string, forcePath bool, sink ProgressSink) (BatchResult, error) {
	file, err := os.Open(input)
	if err != nil {
		return BatchResult{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxLockBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return BatchResult{}, readErr
	}
	if closeErr != nil {
		return BatchResult{}, closeErr
	}
	lock, err := ParseLock(data)
	if err != nil {
		return BatchResult{}, err
	}
	goos, goarch := core.platform()
	expected := goos + "/" + goarch
	if lock.Platform != expected {
		return BatchResult{}, fmt.Errorf("lock platform %q does not match host %q", lock.Platform, expected)
	}
	result := BatchResult{Failed: map[string]string{}, FailureCodes: map[string]ErrorCode{}}
	for i, entry := range lock.Applications {
		if err := ctx.Err(); err != nil {
			result.Canceled = true
			return result, err
		}
		item, err := core.resolveExactLock(ctx, entry)
		if err != nil {
			result.Failed[entry.ID] = err.Error()
			result.FailureCodes[entry.ID] = CodeOf(err)
			result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: entry.ID, Status: "failed", Reason: err.Error(), Code: CodeOf(err)})
			continue
		}
		if !forcePath {
			if conflicts := core.checkItemPath(item); len(conflicts) > 0 {
				failure := &Error{Code: CodeConflict, Op: "install " + entry.ID, Err: fmt.Errorf("installation preflight failed: %s", formatPathConflicts(conflicts))}
				result.Failed[entry.ID] = failure.Error()
				result.FailureCodes[entry.ID] = failure.Code
				result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: entry.ID, Status: "failed", Reason: failure.Error(), Code: failure.Code})
				continue
			}
		}
		progress := func(value Progress) {
			value.Item, value.Total = i+1, len(lock.Applications)
			if sink != nil {
				sink(value)
			}
		}
		installed, stateErr := state.LoadForApp(core.layout, entry.ID)
		var outcome install.Outcome
		if stateErr == nil && installed.Current == entry.Version && installed.CurrentFingerprint == entry.Fingerprint && installed.Channel == entry.Channel {
			result.Skipped = append(result.Skipped, entry.ID)
			result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: entry.ID, Status: "skipped", Reason: "already matches lock", Code: CodeNoUpdate})
			continue
		}
		if stateErr == nil {
			outcome, err = core.installer.UpdateWithOptionsSubject(ctx, item, install.Options{Channel: entry.Channel, Explicit: true}, core.progress(progress, entry.ID))
		} else if os.IsNotExist(stateErr) {
			outcome, err = core.installer.InstallWithOptionsSubject(ctx, item, install.Options{Channel: entry.Channel, Explicit: true}, core.progress(progress, entry.ID))
		} else {
			err = stateErr
		}
		if err != nil {
			classified := classify("install "+entry.ID, err)
			result.Failed[entry.ID] = classified.Error()
			result.FailureCodes[entry.ID] = CodeOf(classified)
			result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: entry.ID, Status: "failed", Reason: classified.Error(), Code: CodeOf(classified)})
			continue
		}
		value := Result{AppID: entry.ID, Version: outcome.State.Current, Fingerprint: outcome.State.CurrentFingerprint, Previous: outcome.State.Previous, PreviousFingerprint: outcome.State.PreviousFingerprint, Channel: outcome.State.Channel, Pinned: outcome.State.Pinned, Warnings: outcome.Warnings}
		result.Completed = append(result.Completed, value)
		result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: entry.ID, Status: "completed", Result: &value})
	}
	return result, nil
}

func (core *Core) resolveExactLock(ctx context.Context, entry LockEntry) (*manifest.Manifest, error) {
	catalog, err := core.catalog(ctx, nil)
	if err != nil {
		return nil, err
	}
	goos, goarch := core.platform()
	item, err := catalog.ReleaseForPlatform(entry.ID, goos, goarch, entry.Version)
	if err != nil {
		return nil, err
	}
	if item.Release.Channel != entry.Channel {
		return nil, fmt.Errorf("locked channel %q does not resolve version %q", entry.Channel, entry.Version)
	}
	fp, err := item.ResolvedPackageFingerprint()
	if err != nil {
		return nil, err
	}
	if fp != entry.Fingerprint {
		return nil, fmt.Errorf("locked fingerprint does not match catalog")
	}
	return item, nil
}

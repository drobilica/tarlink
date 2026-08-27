package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/drobilica/tarlink/internal/appimage"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/state"
)

// DoctorCheck is one read-only doctor observation.
type DoctorCheck struct {
	Label  string
	Status string // ok, warning, or error
	Detail string
}

type DoctorApplication struct {
	ID     string
	Checks []DoctorCheck
}

// DoctorReport contains the complete integrity audit. A report is returned
// even when individual installations are damaged; only inability to perform
// the audit itself is returned as an error.
type DoctorReport struct {
	Global       []DoctorCheck
	Applications []DoctorApplication
	Errors       int
	Warnings     int
}

// Doctor audits TarLink state without acquiring lifecycle locks or changing
// any filesystem object. It never executes an installed application.
func (core *Core) Doctor(ctx context.Context) (DoctorReport, error) {
	var report DoctorReport
	check := func(target *[]DoctorCheck, label, status, detail string) {
		*target = append(*target, DoctorCheck{Label: label, Status: status, Detail: detail})
		if status == "error" {
			report.Errors++
		}
		if status == "warning" {
			report.Warnings++
		}
	}
	stateRootHealthy := false
	for _, item := range []struct{ label, directory string }{{"apps", core.layout.Apps}, {"states", core.layout.States}} {
		label, directory := item.label, item.directory
		info, err := os.Lstat(directory)
		switch {
		case errors.Is(err, os.ErrNotExist):
			check(&report.Global, label, "ok", "not created")
		case err != nil:
			return report, fmt.Errorf("inspect %s directory: %w", label, err)
		case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
			check(&report.Global, label, "error", "not a real directory")
		default:
			check(&report.Global, label, "ok", "available")
			if label == "states" {
				stateRootHealthy = true
			}
		}
	}
	binRequired := false
	hasState := false
	if stateRootHealthy {
		if entries, readErr := os.ReadDir(core.layout.States); readErr == nil {
			for _, entry := range entries {
				if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				hasState = true
				value, loadErr := state.LoadForApp(core.layout, entry.Name()[:len(entry.Name())-5])
				if loadErr != nil {
					continue
				}
				for _, executable := range value.Executables {
					if executable.WantsBinLink() {
						binRequired = true
						break
					}
				}
				if binRequired {
					break
				}
			}
		}
	}
	binInfo, err := os.Lstat(core.layout.Bin)
	if errors.Is(err, os.ErrNotExist) {
		status := "warning"
		if !binRequired && hasState {
			status = "ok"
		}
		check(&report.Global, "~/.local/bin", status, "directory is missing")
	} else if err != nil {
		return report, fmt.Errorf("inspect executable directory: %w", err)
	} else if binInfo.Mode()&os.ModeSymlink != 0 || !binInfo.IsDir() {
		check(&report.Global, "~/.local/bin", "error", "not a real directory")
	} else {
		check(&report.Global, "~/.local/bin", "ok", "available")
	}

	if !stateRootHealthy {
		return report, nil
	}
	entries, err := os.ReadDir(core.layout.States)
	if errors.Is(err, os.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return report, fmt.Errorf("list state directory: %w", err)
	}
	for _, entry := range entries {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return report, ctx.Err()
			default:
			}
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			if len(entry.Name()) >= 7 && entry.Name()[:7] == ".state-" {
				continue
			}
			check(&report.Global, "state entry "+entry.Name(), "error", "unexpected state entry")
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		value, loadErr := state.LoadForApp(core.layout, id)
		if loadErr != nil {
			check(&report.Global, "state "+id, "error", loadErr.Error())
			continue
		}
		application := DoctorApplication{ID: id}
		add := func(label, status, detail string) {
			application.Checks = append(application.Checks, DoctorCheck{Label: label, Status: status, Detail: detail})
			if status == "error" {
				report.Errors++
			}
			if status == "warning" {
				report.Warnings++
			}
		}
		validState := true
		if value.App != id {
			add("state", "error", "application ID does not match filename")
			validState = false
		}
		if validateErr := value.ValidateForLayout(core.layout); validateErr != nil {
			add("state", "error", validateErr.Error())
			validState = false
		} else {
			add("state", "ok", "readable and internally consistent")
		}
		if validState {
			core.auditApplication(value, add)
		}
		report.Applications = append(report.Applications, application)
	}
	sort.Slice(report.Applications, func(i, j int) bool { return report.Applications[i].ID < report.Applications[j].ID })
	return report, nil
}

func (core *Core) auditApplication(value state.State, add func(string, string, string)) {
	appRoot := filepath.Join(core.layout.Apps, value.App)
	root, err := os.Lstat(appRoot)
	if err != nil {
		add("payload", "error", err.Error())
		return
	}
	if root.Mode()&os.ModeSymlink != 0 || !root.IsDir() {
		add("payload", "error", "application root is not a real directory")
		return
	}
	current := filepath.Join(appRoot, "current")
	currentTarget, linkErr := os.Readlink(current)
	currentPath, currentPathErr := core.layout.PackagePath(value.App, value.Current, value.CurrentFingerprint)
	expectedCurrent, expectedCurrentErr := filepath.Rel(appRoot, currentPath)
	if linkErr != nil || currentPathErr != nil || expectedCurrentErr != nil || currentTarget != expectedCurrent {
		add("payload", "error", fmt.Sprintf("current link does not point to %s", expectedCurrent))
	} else {
		add("payload", "ok", "current payload is active")
	}
	for _, packageRef := range []struct{ version, fingerprint string }{{value.Current, value.CurrentFingerprint}, {value.Previous, value.PreviousFingerprint}} {
		if packageRef.version == "" {
			continue
		}
		path, pathErr := core.layout.PackagePath(value.App, packageRef.version, packageRef.fingerprint)
		info, statErr := os.Lstat(path)
		if pathErr != nil || statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			add("payload "+packageRef.version, "error", "version payload is missing or not a real directory")
			continue
		}
		add("payload "+packageRef.version, "ok", "present")
	}
	if value.Artifact == "appimage" {
		arch := core.goarch
		if arch == "" {
			arch = runtime.GOARCH
		}
		if err := appimage.ValidatePath(filepath.Join(appRoot, "current", "appimage"), arch); err != nil {
			add("AppImage", "error", err.Error())
		} else {
			add("AppImage", "ok", "opaque artifact is valid")
		}
	}
	for _, executable := range value.Executables {
		target := filepath.Join(appRoot, "current", filepath.FromSlash(executable.Path))
		info, targetErr := os.Lstat(target)
		if targetErr != nil {
			add("executable: "+executable.Name, "error", "target is missing")
		} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			add("executable: "+executable.Name, "error", "target is not a regular executable file")
		} else {
			add("executable: "+executable.Name, "ok", "target is present")
		}
	}
	spec := integration.Spec{ID: value.App, ApplicationRoot: appRoot, LocalBinDirectory: core.layout.Bin, DesktopDirectory: core.layout.Desktop, IconDirectory: core.layout.Icons, DesktopEnabled: value.DesktopEnabled, DesktopSHA256: value.Integration.DesktopSHA256, Icon: value.Integration.IconSource, IconSHA256: value.Integration.IconSHA256, IconSize: value.Integration.IconSize}
	for _, executable := range value.Executables {
		spec.Executables = append(spec.Executables, integration.ExecutableSpec{Name: executable.Name, Path: executable.Path, CreateBinLink: executable.CreateBinLink})
	}
	if err := integration.ValidateOwned(spec); err != nil {
		add("integration", "error", err.Error())
	} else {
		add("integration", "ok", "executables and desktop integration are owned")
	}
	for _, executable := range value.Executables {
		if !executable.WantsBinLink() {
			continue
		}
		conflicts := integration.CheckPath(integration.Spec{ID: value.App, Executables: []integration.ExecutableSpec{{Name: executable.Name}}, LocalBinDirectory: core.layout.Bin}, os.Getenv("PATH"))
		for _, conflict := range conflicts {
			add("PATH "+executable.Name, "warning", conflict.Type+" ("+conflict.Directory+")")
		}
	}
}

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/state"
)

// Launch is an already-resolved local execution closure. It is deliberately
// data-only: the command-line frontend performs the final exec so lifecycle
// and registry code never executes an external program.
type Launch struct {
	Program string
	Args    []string
	Env     []string
	Dir     string
}

// PrepareRun reads only local state and immutable deployments. It never opens
// the registry or constructs a network client request.
func (core *Core) PrepareRun(appID string, arguments []string) (Launch, error) {
	if err := filesystem.ValidateID(appID); err != nil {
		return Launch{}, err
	}
	installed, err := state.LoadForApp(core.layout, appID)
	if err != nil {
		return Launch{}, err
	}
	if err := installed.ValidateForLayout(core.layout); err != nil {
		return Launch{}, err
	}
	packageRoot, err := core.layout.PackagePath(appID, installed.Current, installed.CurrentFingerprint)
	if err != nil {
		return Launch{}, err
	}
	if err := filesystem.CheckOwnedDirectoryWithin(core.layout.Home, packageRoot); err != nil {
		return Launch{}, err
	}
	if len(installed.Executables) == 0 {
		return Launch{}, errors.New("installed application has no executable")
	}
	executable := filepath.Join(packageRoot, filepath.FromSlash(installed.Executables[0].Path))
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0111 == 0 {
		return Launch{}, errors.New("installed application executable is missing or unsafe")
	}
	if installed.Runtime == nil {
		return Launch{Program: executable, Args: append([]string{executable}, arguments...), Env: os.Environ(), Dir: packageRoot}, nil
	}
	fingerprint, err := installed.Runtime.Fingerprint()
	if err != nil {
		return Launch{}, err
	}
	runtimeRoot, err := core.layout.RuntimePath(installed.Runtime.ID, installed.Runtime.Version, fingerprint)
	if err != nil {
		return Launch{}, err
	}
	if err := filesystem.CheckOwnedDirectoryWithin(core.layout.Home, runtimeRoot); err != nil {
		return Launch{}, fmt.Errorf("runtime deployment: %w", err)
	}
	entryPoint := filepath.Join(runtimeRoot, "_v2-entry-point")
	entryInfo, err := os.Lstat(entryPoint)
	if err != nil || !entryInfo.Mode().IsRegular() || entryInfo.Mode()&os.ModeSymlink != 0 || entryInfo.Mode()&0111 == 0 {
		return Launch{}, errors.New("runtime deployment is missing the Valve v2 entry point")
	}
	variableDir := filepath.Join(core.layout.StateHome, "tarlink", "runtime-vars", fingerprint[len("sha256:"):])
	if err := filesystem.SecureMkdirAll(variableDir, 0700); err != nil {
		return Launch{}, err
	}
	environment := cleanValveEnvironment(os.Environ())
	environment = append(environment, "PRESSURE_VESSEL_VARIABLE_DIR="+variableDir)
	return Launch{Program: entryPoint, Args: append([]string{entryPoint, "--verb=waitforexitandrun", "--", executable}, arguments...), Env: environment, Dir: packageRoot}, nil
}

func cleanValveEnvironment(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.SplitN(value, "=", 2)[0]
		switch key {
		case "LD_PRELOAD", "STEAM_RUNTIME", "PRESSURE_VESSEL_PREFIX", "PRESSURE_VESSEL_VARIABLE_DIR":
			continue
		}
		result = append(result, value)
	}
	return result
}

package app

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/registrycheck"
)

// RegistryCheckOptions selects the registry tree and optional artifact audit
// scope for the released registry maintenance command.
type RegistryCheckOptions struct {
	Root         string
	App          string
	OldRoot      string
	AllArtifacts bool
}

// RegistryCheckResult reports lifecycle materialization performed after full
// structural validation.
type RegistryCheckResult struct {
	Materialized int `json:"materialized"`
}

// RegistryCheckService is optional so presentation clients that do not expose
// registry maintenance remain compatible with the core Service boundary.
type RegistryCheckService interface {
	CheckRegistry(context.Context, RegistryCheckOptions) (RegistryCheckResult, error)
}

// CheckRegistry validates the complete registry tree and, when selected,
// materializes artifacts through TarLink's production install/uninstall
// lifecycle. It never executes an application binary.
func (core *Core) CheckRegistry(ctx context.Context, options RegistryCheckOptions) (RegistryCheckResult, error) {
	if options.Root == "" {
		return RegistryCheckResult{}, classify("registry check", errors.New("registry path is required"))
	}
	selected := 0
	if options.App != "" {
		selected++
	}
	if options.OldRoot != "" {
		selected++
	}
	if options.AllArtifacts {
		selected++
	}
	if selected > 1 {
		return RegistryCheckResult{}, classify("registry check", errors.New("registry check selectors are mutually exclusive"))
	}
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return RegistryCheckResult{}, classify("registry check", err)
	}
	oldRoot := options.OldRoot
	if oldRoot != "" {
		oldRoot, err = filepath.Abs(oldRoot)
		if err != nil {
			return RegistryCheckResult{}, classify("registry check", err)
		}
	}
	selection, err := registrycheck.Select(root, options.App, options.AllArtifacts, oldRoot)
	if err != nil {
		return RegistryCheckResult{}, classify("registry check", err)
	}
	client := download.NewClient()
	if core != nil && core.syncer != nil && core.syncer.Client != nil {
		client = core.syncer.Client
	}
	for _, item := range selection.Items {
		if err := registrycheck.MaterializeWithClient(ctx, item, client); err != nil {
			return RegistryCheckResult{}, classify("registry check", err)
		}
	}
	return RegistryCheckResult{Materialized: len(selection.Items)}, nil
}

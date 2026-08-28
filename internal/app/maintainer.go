package app

import (
	"context"
	"path/filepath"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/registry"
)

// Maintainer is the composition root for registry-maintainer tooling:
// registry validation, repository research, artifact inspection, candidate
// onboarding, ledger analysis, and icon maintenance. It deliberately holds no
// installer, upgrader, registry syncer, lifecycle state, or host platform
// resolution, so maintainer commands never require the Linux application
// runtime.
type Maintainer struct {
	layout filesystem.Layout
	client *download.Client
}

// NewMaintainer composes the maintainer dependency graph. client may be nil;
// research clients then fall back to their default HTTP transport.
func NewMaintainer(layout filesystem.Layout, client *download.Client) *Maintainer {
	return &Maintainer{layout: layout, client: client}
}

func (m *Maintainer) ValidateRegistry(_ context.Context, root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return classify("registry validate", err)
	}
	if _, err := registry.ValidateTree(absolute); err != nil {
		return classify("registry validate", err)
	}
	return nil
}

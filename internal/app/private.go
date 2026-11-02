package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/privatecatalog"
	"github.com/drobilica/tarlink/internal/state"
)

// PrivateService is opt-in; ordinary Service operations keep their official authority.
type PrivateService interface {
	EnrollPrivate(context.Context, int64) error
	PrivateOperation(context.Context, string, string, ProgressSink) (Result, error)
}

func (core *Core) EnrollPrivate(ctx context.Context, repositoryID int64) error {
	if repositoryID != privatecatalog.RepositoryID {
		return errors.New("only the approved private Recit repository can be enrolled")
	}
	catalog, err := core.catalog(ctx, nil)
	if err != nil {
		return err
	}
	if _, exists := catalog.Variants["recit"]; exists {
		return errors.New("private recit conflicts with an official application ID")
	}
	client := privatecatalog.New(core.installer.Client.HTTP, core.installer.Client.Offline)
	return client.Enroll(ctx, core.layout, repositoryID)
}
func (core *Core) PrivateOperation(ctx context.Context, operation, selection string, sink ProgressSink) (Result, error) {
	selector, err := ParseSelector(selection)
	if err != nil {
		return Result{}, err
	}
	if selector.App != "recit" {
		return Result{}, errors.New("private authority is scoped to recit")
	}
	if operation != "install" && operation != "update" && operation != "repair" {
		return Result{}, errors.New("unknown private lifecycle operation")
	}
	version := selector.Target
	if operation == "install" {
		catalog, err := core.catalog(ctx, nil)
		if err != nil {
			return Result{}, err
		}
		if _, exists := catalog.Variants["recit"]; exists {
			return Result{}, errors.New("private recit conflicts with an official application ID")
		}
	}
	if operation == "repair" {
		if version != "" {
			return Result{}, errors.New("private repair accepts only the installed identity")
		}
		installed, err := state.LoadForApp(core.layout, "recit")
		if err != nil {
			return Result{}, err
		}
		if installed.Authority != manifest.PrivateRecitAuthority {
			return Result{}, errors.New("installed application does not belong to private Recit authority")
		}
		version = installed.Current
	}
	private := privatecatalog.New(core.installer.Client.HTTP, core.installer.Client.Offline)
	item, err := private.Resolve(ctx, core.layout, version)
	if err != nil {
		return Result{}, err
	}
	if err = core.checkManifestPlatform(item); err != nil {
		return Result{}, err
	}
	downloader := *core.installer.Client
	httpClient := *downloader.HTTP
	httpClient.Transport = private.AssetTransport(item)
	httpClient.Jar = nil
	downloader.HTTP = &httpClient
	downloader.RedactErrors = true
	downloader.Sources = nil
	downloader.SourceConfigError = nil
	downloader.ArtifactCache = filepath.Join(core.layout.Cache, "private-recit", "artifacts")
	manager := install.New(core.layout, &downloader)
	options := install.Options{Channel: item.Release.Channel, Explicit: selector.Target != ""}
	var outcome install.Outcome
	switch operation {
	case "install":
		outcome, err = manager.InstallWithOptionsSubject(ctx, item, options, core.progress(sink, item.ID))
	case "update":
		outcome, err = manager.UpdateWithOptionsSubject(ctx, item, options, core.progress(sink, item.ID))
	case "repair":
		outcome, err = manager.RepairSubject(ctx, item, core.progress(sink, item.ID))
	}
	if err != nil {
		return Result{}, classify(fmt.Sprintf("private %s recit", operation), err)
	}
	return Result{AppID: item.ID, Version: outcome.State.Current, Fingerprint: outcome.State.CurrentFingerprint, Previous: outcome.State.Previous, PreviousFingerprint: outcome.State.PreviousFingerprint, Channel: outcome.State.Channel, Pinned: outcome.State.Pinned, Warnings: outcome.Warnings}, nil
}

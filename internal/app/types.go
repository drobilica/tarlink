package app

import (
	"context"
	"errors"

	"github.com/drobilica/tarlink/internal/integration"
)

// ProgressStage is a stable, renderer-independent lifecycle stage.
type ProgressStage string

const (
	ProgressResolving   ProgressStage = "resolving"
	ProgressDownloading ProgressStage = "downloading"
	ProgressVerifying   ProgressStage = "verifying"
	ProgressExtracting  ProgressStage = "extracting"
	ProgressInstalling  ProgressStage = "installing"
	ProgressIntegrating ProgressStage = "integrating"
	ProgressActivating  ProgressStage = "activating"
	ProgressCleaning    ProgressStage = "cleaning"
	ProgressComplete    ProgressStage = "complete"
	ProgressUpgrading   ProgressStage = "upgrading"
)

type Progress struct {
	Stage      ProgressStage `json:"stage"`
	AppID      string        `json:"app_id,omitempty"`
	BytesDone  int64         `json:"bytes_done,omitempty"`
	BytesTotal int64         `json:"bytes_total,omitempty"`
}

type ProgressSink func(Progress)

type Application struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Summary          string   `json:"summary"`
	Homepage         string   `json:"homepage"`
	Categories       []string `json:"categories"`
	Requirements     []string `json:"requirements,omitempty"`
	RegistryVersion  string   `json:"registry_version"`
	InstalledVersion string   `json:"installed_version,omitempty"`
	PreviousVersion  string   `json:"previous_version,omitempty"`
	UpdateAvailable  bool     `json:"update_available"`
}

type Result struct {
	AppID    string   `json:"app_id"`
	Version  string   `json:"version,omitempty"`
	Previous string   `json:"previous,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type UpdateAllResult struct {
	Updated      []Result             `json:"updated"`
	Skipped      []string             `json:"skipped"`
	Failed       map[string]string    `json:"failed"`
	FailureCodes map[string]ErrorCode `json:"failure_codes,omitempty"`
}

type Version struct {
	Version string `json:"version"`
	Status  string `json:"status"`
}

type TarLinkVersion struct {
	Current          string `json:"current"`
	Latest           string `json:"latest,omitempty"`
	UpgradeAvailable bool   `json:"upgrade_available"`
}

// Service is the UI-independent application API used by the CLI and TUI.
type Service interface {
	Install(context.Context, string, ProgressSink) (Result, error)
	Update(context.Context, string, ProgressSink) (Result, error)
	UpdateAll(context.Context, ProgressSink) (UpdateAllResult, error)
	Uninstall(context.Context, string, ProgressSink) error
	UninstallAll(context.Context, ProgressSink) error
	Rollback(context.Context, string, ProgressSink) (Result, error)
	List(context.Context) ([]Application, error)
	Info(context.Context, string) (Application, error)
	Search(context.Context, string) ([]Application, error)
	Versions(context.Context, string) ([]Version, error)
	SyncRegistry(context.Context, ProgressSink) error
	ValidateRegistry(context.Context, string) error
	CheckTarLinkVersion(context.Context) (TarLinkVersion, error)
	CheckTarLinkVersionFresh(context.Context) (TarLinkVersion, error)
	UpgradeTarLink(context.Context, ProgressSink) (TarLinkVersion, error)
	CheckInstallPath(string) ([]integration.PathConflict, error)
}

// PathConflict is an alias for integration.PathConflict, exposed through the
// service API to keep the UI-independent boundary stable.
type PathConflict = integration.PathConflict

type ErrorCode string

const (
	CodeInvalidArguments    ErrorCode = "invalid_arguments"
	CodeUnsupportedPlatform ErrorCode = "unsupported_platform"
	CodeRegistry            ErrorCode = "registry_error"
	CodeNetwork             ErrorCode = "network_failure"
	CodeChecksum            ErrorCode = "checksum_mismatch"
	CodeArchive             ErrorCode = "archive_failure"
	CodeNotFound            ErrorCode = "application_not_found"
	CodeAlreadyInstalled    ErrorCode = "already_installed"
	CodeNotInstalled        ErrorCode = "not_installed"
	CodeNoUpdate            ErrorCode = "no_update_available"
	CodeLockConflict        ErrorCode = "lock_conflict"
	CodeStateCorrupt        ErrorCode = "state_corruption"
	CodePermission          ErrorCode = "filesystem_permission"
	CodeConflict            ErrorCode = "filesystem_conflict"
	CodeRoot                ErrorCode = "root_execution_refused"
)

type Error struct {
	Code ErrorCode
	Op   string
	Err  error
}

func (e *Error) Error() string {
	if e.Op == "" {
		return e.Err.Error()
	}
	return e.Op + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

func CodeOf(err error) ErrorCode {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

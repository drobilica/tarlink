package app

import (
	"context"
	"errors"
	"strings"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/freshness"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/research"
)

// Selector is the presentation-independent form of app, app@channel and
// app@version. Resolution of whether Target names a channel or an exact
// version belongs to the registry catalog.
type Selector struct{ App, Target string }

func ParseSelector(value string) (Selector, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "@") || strings.Count(value, "@") > 1 {
		return Selector{}, errors.New("invalid application selector")
	}
	parts := strings.SplitN(value, "@", 2)
	if err := filesystem.ValidateID(parts[0]); err != nil {
		return Selector{}, err
	}
	selector := Selector{App: parts[0]}
	if len(parts) == 2 {
		if parts[1] == "" || strings.ContainsAny(parts[1], "/\\") {
			return Selector{}, errors.New("invalid selector target")
		}
		selector.Target = parts[1]
	}
	return selector, nil
}

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
	Item       int           `json:"item,omitempty"`
	Total      int           `json:"total,omitempty"`
	BytesDone  int64         `json:"bytes_done,omitempty"`
	BytesTotal int64         `json:"bytes_total,omitempty"`
}

type BatchTarget struct {
	AppID   string `json:"app_id"`
	Name    string `json:"name"`
	Channel string `json:"channel"`
	Version string `json:"version"`
}

type BatchResult struct {
	Completed []Result          `json:"completed"`
	Failed    map[string]string `json:"failed"`
	Canceled  bool              `json:"canceled,omitempty"`
}

// BatchService is optional on the presentation service boundary so existing
// integrations can continue to provide the single-application API.
type BatchService interface {
	ResolveInstallBatch(context.Context, []string) ([]BatchTarget, error)
	InstallBatch(context.Context, []string, ProgressSink) (BatchResult, error)
	UninstallBatch(context.Context, []string, ProgressSink) (BatchResult, error)
}

type ProgressSink func(Progress)

type Application struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Summary          string            `json:"summary"`
	Homepage         string            `json:"homepage"`
	Categories       []string          `json:"categories"`
	Requirements     []string          `json:"requirements,omitempty"`
	RegistryVersion  string            `json:"registry_version"`
	InstalledVersion string            `json:"installed_version,omitempty"`
	PreviousVersion  string            `json:"previous_version,omitempty"`
	InstalledChannel string            `json:"installed_channel,omitempty"`
	Pinned           bool              `json:"pinned"`
	UpdateAvailable  bool              `json:"update_available"`
	DefaultChannel   string            `json:"default_channel,omitempty"`
	ChannelHeads     map[string]string `json:"channel_heads,omitempty"`
	ApprovedReleases []Version         `json:"approved_releases,omitempty"`
}

type Result struct {
	AppID    string   `json:"app_id"`
	Version  string   `json:"version,omitempty"`
	Previous string   `json:"previous,omitempty"`
	Channel  string   `json:"channel,omitempty"`
	Pinned   bool     `json:"pinned"`
	Warnings []string `json:"warnings,omitempty"`
}

type UpdateAllResult struct {
	Updated      []Result             `json:"updated"`
	Skipped      []string             `json:"skipped"`
	Pinned       []string             `json:"pinned,omitempty"`
	Failed       map[string]string    `json:"failed"`
	FailureCodes map[string]ErrorCode `json:"failure_codes,omitempty"`
}

type Version struct {
	Version string `json:"version"`
	Status  string `json:"status"`
	Channel string `json:"channel,omitempty"`
	Pinned  bool   `json:"pinned,omitempty"`
	Current bool   `json:"current,omitempty"`
	Default bool   `json:"default,omitempty"`
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
	ListAvailable(context.Context) ([]Application, error)
	Info(context.Context, string) (Application, error)
	Search(context.Context, string) ([]Application, error)
	Versions(context.Context, string) ([]Version, error)
	SyncRegistry(context.Context, ProgressSink) error
	ValidateRegistry(context.Context, string) error
	CheckTarLinkVersion(context.Context) (TarLinkVersion, error)
	CheckTarLinkVersionFresh(context.Context) (TarLinkVersion, error)
	UpgradeTarLink(context.Context, ProgressSink) (TarLinkVersion, error)
	CheckInstallPath(string) ([]integration.PathConflict, error)
	Doctor(context.Context) (DoctorReport, error)
}

// FreshnessService exposes advisory upstream-release checks used by registry
// maintenance commands. It is deliberately separate from Service: neither the
// operational CLI nor the TUI require this maintainer-only capability.
type FreshnessService interface {
	Freshness(context.Context, string) (freshness.Report, error)
}

// ResearchService exposes advisory repository provenance and inspection. Its
// results never enter the trusted registry or installation path.
type ResearchService interface {
	Research(context.Context, ResearchOptions) (ResearchResult, error)
}

// CandidateService exposes the read-only candidate ledger and its change
// analysis to registry maintainers.
type CandidateService interface {
	CandidateLedger() (research.CandidateLedger, error)
	CandidateChanges(context.Context) (research.CandidateChanges, error)
}

// BlockerService exposes read-only capability and blocker analysis for
// registry-maintenance planning.
type BlockerService interface {
	Blockers(string) ([]research.BlockerSummary, error)
	CapabilityPreflight(string) ([]research.CapabilityResult, error)
}

// RegistryIconService exposes the explicit maintainer workflow for auditing
// and repairing missing registry icons.
type RegistryIconService interface {
	RegistryIcons(context.Context, RegistryIconOptions) (RegistryIconReport, error)
}

// PathConflict is an alias for integration.PathConflict, exposed through the
// service API to keep the UI-independent boundary stable.
type PathConflict = integration.PathConflict

type UninstallConflict struct {
	AppID string
	Path  string
}

type UninstallConflictError struct {
	Conflict UninstallConflict
	Err      error
}

func (e *UninstallConflictError) Error() string { return e.Err.Error() }
func (e *UninstallConflictError) Unwrap() error { return e.Err }

type UninstallRecoveryService interface {
	RemoveUninstallConflict(context.Context, string, string) error
}

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
	CodePinned              ErrorCode = "application_pinned"
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

package install

import (
	"context"
	"errors"
	"fmt"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/manifest"
	taruntime "github.com/drobilica/tarlink/internal/runtime"
)

// PreflightOffline verifies the complete selected closure into the disposable
// cache before any application or runtime mutation. Online calls are a no-op.
// Materialization still re-verifies bytes through the normal acquisition path.
func (manager *Manager) PreflightOffline(ctx context.Context, items []*manifest.Manifest) error {
	if manager.Client == nil || !manager.Client.Offline {
		return nil
	}
	manager.ensureArtifactCache()
	type resource struct {
		role    string
		request download.VerifiedRequest
	}
	checked := map[string]error{}
	var failures []error
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item == nil {
			return errors.New("offline preflight manifest is nil")
		}
		if _, err := item.ResolvedPackageFingerprint(); err != nil {
			return err
		}
		requests := []resource{
			{"application", download.VerifiedRequest{URL: item.Release.URL, Algorithm: item.Release.Verification.Algorithm, Digest: item.Release.Verification.Digest, MaxBytes: download.DefaultMaxArtifactBytes}},
		}
		if item.Runtime != nil {
			requests = append(requests, resource{"runtime", download.VerifiedRequest{URL: item.Runtime.Artifact.URL, Algorithm: "sha256", Digest: item.Runtime.Artifact.Verification.Digest, MaxBytes: taruntime.MaxArtifactBytes}})
		}
		if item.Desktop.Icon.URL != "" {
			requests = append(requests, resource{"desktop icon", download.VerifiedRequest{URL: item.Desktop.Icon.URL, Algorithm: "sha256", Digest: item.Desktop.Icon.SHA256, MaxBytes: maxRemoteIconBytes}})
		}
		for _, resource := range requests {
			request := resource.request
			key := fmt.Sprintf("%s:%s:%d", request.Algorithm, request.Digest, request.MaxBytes)
			err, done := checked[key]
			if !done {
				verified, acquireErr := manager.Client.AcquireVerified(ctx, request)
				err = acquireErr
				if verified != nil {
					err = errors.Join(err, verified.Close())
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				checked[key] = err
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("%s %s %s:%s: %w", item.ID, resource.role, request.Algorithm, request.Digest, err))
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("offline artifact preflight failed before application changes: %w", errors.Join(failures...))
	}
	return nil
}

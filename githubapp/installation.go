package githubapp

import (
	"context"
	"fmt"
	"net/http"
)

// InstallationFailure identifies a confirmed outcome of CheckInstallation.
// Network errors, rate limits, generic forbidden responses and identity
// mismatches are not installation lifecycle facts.
type InstallationFailure string

const (
	// AppAuthenticationRejected means the supplied JWT was rejected. It may
	// have expired; callers must not infer that the signing key was revoked.
	AppAuthenticationRejected InstallationFailure = "app_authentication_rejected"
	// InstallationSuspended comes from the installation's suspended_at field.
	InstallationSuspended InstallationFailure = "installation_suspended"
	// InstallationDeleted means the exact installation was absent after the
	// same JWT authenticated as the expected App. Use this only for an
	// installation previously known to belong to that App.
	InstallationDeleted InstallationFailure = "installation_deleted"
)

// InstallationError reports a confirmed outcome without including the provider
// response in its message. Unwrap preserves the underlying StatusError, whose
// body may contain private data and must not be exposed to end users.
type InstallationError struct {
	Kind  InstallationFailure
	cause error
}

func (e *InstallationError) Error() string { return string(e.Kind) }
func (e *InstallationError) Unwrap() error { return e.cause }

// GetInstallation reads one exact installation with an App JWT. A raw 404
// does not establish deletion; CheckInstallation also verifies App identity.
func (c *Client) GetInstallation(ctx context.Context, appJWT string, installationID int64) (*Installation, error) {
	if installationID <= 0 {
		return nil, fmt.Errorf("installation ID must be positive")
	}
	var installation Installation
	path := fmt.Sprintf("/app/installations/%d", installationID)
	if err := c.do(ctx, http.MethodGet, path, appJWT, nil, &installation); err != nil {
		return nil, fmt.Errorf("getting installation: %w", err)
	}
	if installation.ID != installationID || installation.AppID <= 0 {
		return nil, fmt.Errorf("installation response identity mismatch")
	}
	return &installation, nil
}

// CheckInstallation confirms the expected App first, then reads the exact
// installation. It never mints tokens, retries or changes caller state. Callers
// supply a fresh JWT and retain ownership of credential refresh and admission.
// Each error returns no installation. Only InstallationError supplies a
// lifecycle classification; every other error leaves availability unknown.
func (c *Client) CheckInstallation(ctx context.Context, appJWT string, appID, installationID int64) (*Installation, error) {
	if appID <= 0 || installationID <= 0 {
		return nil, fmt.Errorf("App and installation IDs must be positive")
	}
	app, err := c.GetApp(ctx, appJWT)
	if err != nil {
		return nil, appAuthenticationError(err)
	}
	if app.ID != appID {
		return nil, fmt.Errorf("authenticated App identity mismatch")
	}
	installation, err := c.GetInstallation(ctx, appJWT, installationID)
	if err != nil {
		if IsStatus(err, http.StatusNotFound) {
			return nil, &InstallationError{Kind: InstallationDeleted, cause: err}
		}
		return nil, appAuthenticationError(err)
	}
	if installation.AppID != appID {
		return nil, fmt.Errorf("installation App identity mismatch")
	}
	if installation.SuspendedAt != nil {
		return nil, &InstallationError{Kind: InstallationSuspended}
	}
	return installation, nil
}

func appAuthenticationError(err error) error {
	if IsStatus(err, http.StatusUnauthorized) {
		return &InstallationError{Kind: AppAuthenticationRejected, cause: err}
	}
	return err
}

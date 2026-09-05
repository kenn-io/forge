package githubapp

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"go.kenn.io/forge/platform"
)

// GetInstallation establishes the expected App identity before interpreting an
// installation's absence. A repository 404 or a JWT for another App is never
// evidence that this installation was deleted.
func (c *Client) GetInstallation(ctx context.Context, jwt string, expectedAppID, installationID int64, meter *platform.Meter) (*Installation, error) {
	if expectedAppID <= 0 || installationID <= 0 {
		return nil, c.installationError(platform.ErrCodeInvalidArgument, expectedAppID, installationID)
	}
	app, err := c.GetApp(ctx, jwt, meter)
	if err != nil {
		if IsStatus(err, http.StatusUnauthorized) {
			return nil, c.installationError(platform.ErrCodeCredentialRejected, expectedAppID, installationID)
		}
		return nil, err
	}
	if app.ID != expectedAppID {
		return nil, c.installationError(platform.ErrCodeProviderContract, expectedAppID, installationID)
	}
	var installation Installation
	err = c.do(ctx, http.MethodGet, fmt.Sprintf("/app/installations/%d", installationID), jwt, nil, &installation, meter)
	if err != nil {
		if IsStatus(err, http.StatusNotFound) {
			return nil, c.installationError(platform.ErrCodeInstallationDeleted, expectedAppID, installationID)
		}
		return nil, err
	}
	if installation.ID != installationID || installation.AppID != expectedAppID {
		return nil, c.installationError(platform.ErrCodeProviderContract, expectedAppID, installationID)
	}
	if installation.SuspendedAt != nil {
		return &installation, c.installationError(platform.ErrCodeInstallationSuspended, expectedAppID, installationID)
	}
	return &installation, nil
}

func (c *Client) installationError(code platform.PlatformErrorCode, appID, installationID int64) error {
	return &platform.Error{
		Code: code, Provider: platform.KindGitHub, PlatformHost: c.host,
		Details: map[string]string{
			"credential_scope": "app", "app_id": strconv.FormatInt(appID, 10),
			"installation_id": strconv.FormatInt(installationID, 10),
		},
	}
}

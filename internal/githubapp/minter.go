package githubapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/platform"
)

// MintInstallationToken signs an app JWT with the key at keyPath and
// exchanges it for an installation access token on host. This is the
// bridge the tokenauth github_app source kind calls; the returned
// expiry drives the source's refresh cache.
func MintInstallationToken(
	ctx context.Context,
	host string,
	appID int64,
	keyPath string,
	installationID int64,
) (string, time.Time, error) {
	return mintInstallationToken(ctx, githubapp.APIBaseForHost(host), appID, keyPath, installationID)
}

func mintInstallationToken(
	ctx context.Context,
	apiBase string,
	appID int64,
	keyPath string,
	installationID int64,
) (string, time.Time, error) {
	key, err := LoadPrivateKey(keyPath)
	if err != nil {
		return "", time.Time{}, err
	}
	appJWT, err := githubapp.SignAppJWT(appID, key, time.Now())
	if err != nil {
		return "", time.Time{}, err
	}
	token, err := githubapp.NewClientWithBase(apiBase).CreateInstallationToken(ctx, appJWT, installationID, nil)
	if err != nil {
		if status, ok := errors.AsType[*githubapp.StatusError](err); ok {
			var code platform.PlatformErrorCode
			switch {
			case status.StatusCode == http.StatusTooManyRequests || status.StatusCode == http.StatusForbidden && (strings.TrimSpace(status.Header.Get("Retry-After")) != "" || strings.TrimSpace(status.Header.Get("X-RateLimit-Remaining")) == "0"):
				code = platform.ErrCodeRateLimited
			case status.StatusCode == http.StatusUnauthorized || status.StatusCode == http.StatusForbidden:
				code = platform.ErrCodePermissionDenied
			case status.StatusCode == http.StatusNotFound:
				code = platform.ErrCodeNotFound
			}
			if code != "" {
				err = &platform.Error{Code: code, Provider: platform.KindGitHub, Err: err}
			}
		}
		return "", time.Time{}, err
	}
	return token.Token, token.ExpiresAt, nil
}

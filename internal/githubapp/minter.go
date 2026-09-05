package githubapp

import (
	"context"
	"net/http"
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
	return mintInstallationToken(ctx, host, githubapp.APIBaseForHost(host), appID, keyPath, installationID)
}

func mintInstallationToken(
	ctx context.Context,
	host string,
	apiBase string,
	appID int64,
	keyPath string,
	installationID int64,
) (string, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	meter, err := platform.NewMeter(ctx, platform.Budget{MaxRecords: 1, MaxNodes: 1, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20})
	if err != nil {
		return "", time.Time{}, err
	}
	key, err := LoadPrivateKey(keyPath)
	if err != nil {
		return "", time.Time{}, err
	}
	appJWT, err := githubapp.SignAppJWT(appID, key, time.Now())
	if err != nil {
		return "", time.Time{}, err
	}
	client := githubapp.NewClient(host, &http.Client{}, githubapp.WithAPIBase(apiBase))
	token, err := client.CreateInstallationToken(ctx, appJWT, installationID, githubapp.TokenScope{AllRepositories: true}, meter)
	if err != nil {
		return "", time.Time{}, err
	}
	return token.Token, token.ExpiresAt, nil
}

package gitea

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/platform/gitealike/gitealiketest"
)

func TestGiteaLikeAdapterContract(t *testing.T) {
	capabilities := gitealiketest.BaseCapabilities()
	capabilities.ReadReviewThreads = true

	gitealiketest.Run(t, gitealiketest.Adapter{
		Kind:         platform.KindGitea,
		Host:         "gitea.test",
		Token:        "gitea-token",
		Capabilities: capabilities,
		NewClient: func(
			t *testing.T,
			baseURL string,
			token string,
			options gitealiketest.ClientOptions,
		) gitealiketest.TestClient {
			clientOptions := []ClientOption{
				WithBaseURLForTesting(baseURL),
				WithServerVersionForTesting(testGiteaServerVersion),
			}
			if options.ForegroundTimeout > 0 {
				clientOptions = append(clientOptions, WithForegroundTimeoutForTesting(options.ForegroundTimeout))
			}
			if options.RateTracker != nil {
				clientOptions = append(clientOptions, WithRateTracker(options.RateTracker))
			}
			if options.SyncBudget != nil {
				clientOptions = append(clientOptions, WithSyncBudget(options.SyncBudget))
			}
			client, err := NewClient("gitea.test", testTokenSource(token), clientOptions...)
			require.NoError(t, err)
			return gitealiketest.TestClient{
				Client: client,
				LookupRepository: func(ctx context.Context, owner, name string) (string, error) {
					repository, err := client.transport.getRepositoryRaw(ctx, owner, name)
					if err != nil || repository == nil {
						return "", err
					}
					return repository.Name, nil
				},
			}
		},
	})
}

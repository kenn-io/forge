package platform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type testPageReadProvider struct {
	testProvider
	issues Page[Issue]
}

func (p testPageReadProvider) ListIssuesPage(
	context.Context,
	RepoRef,
	ItemPageQuery,
) (Page[Issue], error) {
	return p.issues, nil
}

func TestIssueInventoryRejectsItemsFromAnotherRepository(t *testing.T) {
	ref := RepoRef{
		Platform: KindGitHub, Host: "github.com", Owner: "octo-org",
		Name: "widgets", RepoPath: "octo-org/widgets",
	}
	foreign := ref
	foreign.Owner, foreign.RepoPath = "other", "other/widgets"
	provider := testPageReadProvider{
		testProvider: testProvider{
			kind: KindGitHub,
			host: "github.com",
			caps: Capabilities{Archive: ArchiveCapabilities{HistoricalIssues: true}},
		},
		issues: Page[Issue]{
			Items:     []Issue{{Repo: foreign, Number: 7}},
			Exhausted: true,
		},
	}
	registry, err := NewRegistry(provider)
	require.NoError(t, err)
	reader, err := registry.IssuePageReader(provider.kind, provider.host)
	require.NoError(t, err)

	_, err = reader.ListIssuesPage(t.Context(), ref, ItemPageQuery{
		Order: ItemOrderCreated,
	})
	require.ErrorIs(t, err, ErrProviderContract)
}

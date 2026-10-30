package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoListETagsReplaceOnlyTheirRepositoryLists(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	cachedAt := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(d.UpsertHTTPEtag(ctx, "github", "github.com", "Acme", "Widget", "pull", 7, `"item"`))
	require.NoError(d.ReplaceRepoListETags(ctx, "github", "github.com", "Acme", "Widget", []RepoListETag{
		{Resource: "pulls?per_page=100&state=open", ETag: `"page"`, CachedAt: cachedAt},
		{Resource: "issues?per_page=100&state=open", ETag: `"top"`, Detector: true, CachedAt: cachedAt},
	}))
	require.NoError(d.ReplaceRepoListETags(ctx, "github", "github.com", "acme", "other", []RepoListETag{
		{Resource: "pulls?per_page=100&state=open", ETag: `"other"`, CachedAt: cachedAt},
	}))

	// Replacing one repository's lists leaves its item validators and other
	// repositories alone.
	require.NoError(d.ReplaceRepoListETags(ctx, "github", "github.com", "acme", "widget", []RepoListETag{
		{Resource: "pulls?per_page=100&state=open", ETag: `"page-2"`, CachedAt: cachedAt},
	}))
	saved, err := d.ListRepoListETags(ctx, "github")
	require.NoError(err)
	require.Len(saved, 2)
	byName := map[string]RepoListETag{}
	for _, etag := range saved {
		byName[etag.Name] = etag
	}
	assert.Equal(`"page-2"`, byName["widget"].ETag)
	assert.Equal("pulls?per_page=100&state=open", byName["widget"].Resource)
	assert.False(byName["widget"].Detector)
	assert.True(cachedAt.Equal(byName["widget"].CachedAt), "saved time %v", byName["widget"].CachedAt)
	assert.Equal(`"other"`, byName["other"].ETag)

	item, err := d.GetHTTPEtag(ctx, "github", "github.com", "acme", "widget", "pull", 7)
	require.NoError(err)
	assert.Equal(`"item"`, item)
}

func TestRepoListETagsForRepoReadsOnlyThatRepository(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	cachedAt := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"widget", "other"} {
		require.NoError(d.ReplaceRepoListETags(ctx, "github", "github.com", "acme", name, []RepoListETag{
			{Resource: "pulls?per_page=100&state=open", ETag: `"` + name + `"`, CachedAt: cachedAt},
		}))
	}
	require.NoError(d.UpsertHTTPEtag(ctx, "github", "github.com", "acme", "widget", "pull", 7, `"item"`))

	saved, err := d.ListRepoListETagsForRepo(ctx, "github", "github.com", "Acme", "Widget")
	require.NoError(err)
	require.Len(saved, 1)
	assert.Equal(t, `"widget"`, saved[0].ETag)
}

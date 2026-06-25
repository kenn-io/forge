package stacks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	realdb "go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
)

func TestSyncCompletedHookUsesProviderQualifiedRepoIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	_, err := d.UpsertRepo(ctx, realdb.RepoIdentity{
		Platform:     "github",
		PlatformHost: "code.example.com",
		Owner:        "org",
		Name:         "repo",
	})
	require.NoError(err)
	gitlabRepoID, err := d.UpsertRepo(ctx, realdb.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "code.example.com",
		Owner:        "org",
		Name:         "repo",
	})
	require.NoError(err)
	require.NoError(d.UpdateRepoProviderMetadata(ctx, gitlabRepoID, realdb.RepoProviderMetadata{
		CloneURL:      "https://code.example.com/org/repo.git",
		DefaultBranch: "main",
	}))

	now := time.Now().UTC()
	for i, pr := range []struct {
		number     int
		head, base string
	}{
		{100, "feature/base", "main"},
		{101, "feature/tip", "feature/base"},
	} {
		_, err := d.UpsertMergeRequest(ctx, &realdb.MergeRequest{
			RepoID: gitlabRepoID, PlatformID: int64(i + 1),
			Number: pr.number, Title: "MR", Author: "a", State: "open",
			HeadBranch: pr.head, BaseBranch: pr.base,
			HeadRepoCloneURL: "https://code.example.com/org/repo.git",
			CreatedAt:        now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}

	SyncCompletedHook(ctx, d, nil)([]ghclient.RepoSyncResult{{
		Platform:     platform.KindGitLab,
		PlatformHost: "code.example.com",
		Owner:        "org",
		Name:         "repo",
	}})

	stacks, members, err := d.ListStacksWithMembers(ctx, "")
	require.NoError(err)
	require.Len(stacks, 1)
	assert.Equal(gitlabRepoID, stacks[0].RepoID)
	assert.Len(members[stacks[0].ID], 2)
}

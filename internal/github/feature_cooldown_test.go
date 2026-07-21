package github

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/platform"
)

func TestDisabledIssueScopeUsesDailyBackgroundProbeAndManualBypass(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repo := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	client := &partialFailureMock{}
	disabledCause := errors.New("repository issues disabled")
	client.listOpenIssuesErr = platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
		disabledCause,
	)
	var issueListCalls atomic.Int32
	client.listOpenIssuesFn = func(context.Context, string, string) ([]*gh.Issue, error) {
		issueListCalls.Add(1)
		return nil, client.listOpenIssuesErr
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	syncer.now = func() time.Time { return now }

	syncer.RunOnce(t.Context())
	syncer.RunOnce(t.Context())
	assert.Equal(int32(1), issueListCalls.Load())
	_, failed := syncer.failedRepos.Load(repoFailKey(repo))
	assert.False(failed)

	require.NoError(syncer.SyncRepoOnProvider(
		t.Context(), platform.KindGitHub, "github.com", "acme", "widget",
	))
	assert.Equal(int32(2), issueListCalls.Load())

	now = now.Add(24*time.Hour - time.Second)
	syncer.RunOnce(t.Context())
	assert.Equal(int32(2), issueListCalls.Load())

	now = now.Add(time.Second)
	syncer.RunOnce(t.Context())
	assert.Equal(int32(3), issueListCalls.Load())

	client.listOpenIssuesErr = nil
	require.NoError(syncer.SyncRepoOnProvider(
		t.Context(), platform.KindGitHub, "github.com", "acme", "widget",
	))
	assert.Equal(int32(4), issueListCalls.Load())
	client.listOpenIssuesErr = platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
		disabledCause,
	)
	syncer.RunOnce(t.Context())
	assert.Equal(int32(5), issueListCalls.Load(), "successful manual probe must clear the cooldown")
}

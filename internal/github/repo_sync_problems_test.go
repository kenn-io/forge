package github

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func missingRepositoryClient(missing string, gone *atomic.Bool) *mockClient {
	return &mockClient{
		getRepositoryFn: func(_ context.Context, owner, name string) (*gh.Repository, error) {
			if name == missing && gone.Load() {
				return nil, &gh.ErrorResponse{
					Response: &http.Response{StatusCode: http.StatusNotFound},
					Message:  "Not Found",
				}
			}
			id := testRepoID(owner, name)
			return &gh.Repository{ID: &id, Name: &name, Owner: &gh.User{Login: &owner}}, nil
		},
	}
}

func TestRepoSyncFailureStaysInStatusUntilTheRepositorySyncs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	healthy := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	missing := RepoRef{Owner: "acme", Name: "gone", PlatformHost: "github.com"}
	var gone atomic.Bool
	gone.Store(true)
	syncer := NewSyncer(
		map[string]Client{"github.com": missingRepositoryClient(missing.Name, &gone)},
		openTestDB(t), nil, []RepoRef{healthy, missing}, time.Minute, nil, nil,
	)

	syncer.RunOnce(t.Context())
	problems := syncer.Status().RepoErrors
	require.Len(problems, 1)
	assert.Equal("acme/gone", problems[0].Repository)
	assert.Equal("github.com", problems[0].Host)
	assert.Contains(problems[0].Error, "404")
	assert.False(problems[0].Since.IsZero())

	// A later pass that never reaches the failing repository must not hide it,
	// neither while it runs nor once it reports its own clean result.
	var published []*SyncStatus
	syncer.onStatusChange = func(status *SyncStatus) { published = append(published, status) }
	syncer.runOnce(t.Context(), false, nil, []RepoRef{healthy})
	require.NotEmpty(published)
	for _, status := range published {
		assert.Equal(problems, status.RepoErrors)
	}
	assert.Empty(syncer.Status().LastError, "the scoped pass saw no failure itself")

	gone.Store(false)
	syncer.RunOnce(t.Context())
	assert.Empty(syncer.Status().RepoErrors)
}

func TestDirectRepoSyncClearsABackgroundFailure(t *testing.T) {
	missing := RepoRef{Owner: "acme", Name: "gone", PlatformHost: "github.com"}
	var gone atomic.Bool
	gone.Store(true)
	syncer := NewSyncer(
		map[string]Client{"github.com": missingRepositoryClient(missing.Name, &gone)},
		openTestDB(t), nil, []RepoRef{missing}, time.Minute, nil, nil,
	)
	syncer.RunOnce(t.Context())
	require.Len(t, syncer.Status().RepoErrors, 1)

	// With automatic sync paused, a manual refresh is the only sync left.
	gone.Store(false)
	require.NoError(t, syncer.SyncRepoOnProvider(
		t.Context(), platform.KindGitHub, "github.com", missing.Owner, missing.Name,
	))
	assert.Empty(t, syncer.Status().RepoErrors)
}

func TestRepoSyncFailureClearsWhenTheRepositoryIsNoLongerTracked(t *testing.T) {
	healthy := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	missing := RepoRef{Owner: "acme", Name: "gone", PlatformHost: "github.com"}
	var gone atomic.Bool
	gone.Store(true)
	syncer := NewSyncer(
		map[string]Client{"github.com": missingRepositoryClient(missing.Name, &gone)},
		openTestDB(t), nil, []RepoRef{healthy, missing}, time.Minute, nil, nil,
	)
	syncer.RunOnce(t.Context())
	require.Len(t, syncer.Status().RepoErrors, 1)

	syncer.SetRepos([]RepoRef{healthy})
	assert.Empty(t, syncer.Status().RepoErrors)
}

func TestSyncReportsTheDetailPhaseAfterEveryRepositoryIsIndexed(t *testing.T) {
	repos := []RepoRef{
		{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
		{Owner: "acme", Name: "gadget", PlatformHost: "github.com"},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}},
		openTestDB(t), nil, repos, time.Minute, nil, nil,
	)
	var published []*SyncStatus
	syncer.onStatusChange = func(status *SyncStatus) { published = append(published, status) }

	syncer.RunOnce(t.Context())

	require.GreaterOrEqual(t, len(published), 2)
	terminal := published[len(published)-1]
	assert.False(t, terminal.Running)
	lastRunning := published[len(published)-2]
	assert.True(t, lastRunning.Running)
	assert.Equal(t, detailPhaseProgress, lastRunning.Progress,
		"the bar must not stay on the last index count while details refresh")
}

func TestRepoSyncFailureFollowsARenameResolvedDuringThePass(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	configured := RepoRef{Owner: "acme", Name: "old", PlatformHost: "github.com"}
	var renamed atomic.Bool
	client := &mockClient{
		getRepositoryFn: func(_ context.Context, owner, name string) (*gh.Repository, error) {
			if !renamed.Load() {
				return nil, errors.New("connection reset by peer")
			}
			id := testRepoID(owner, "new")
			current := "new"
			return &gh.Repository{ID: &id, Name: &current, Owner: &gh.User{Login: &owner}}, nil
		},
		listOpenPRsFn: func(context.Context, string, string) ([]*gh.PullRequest, error) {
			return nil, errors.New("pull request listing unavailable")
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, openTestDB(t), nil,
		[]RepoRef{configured}, time.Minute, nil, nil,
	)

	syncer.RunOnce(t.Context())
	require.Len(syncer.Status().RepoErrors, 1)
	since := syncer.Status().RepoErrors[0].Since

	// The provider now reports the configured route under a new name, and
	// the pass still fails after adopting it.
	renamed.Store(true)
	syncer.RunOnce(t.Context())
	problems := syncer.Status().RepoErrors
	require.Len(problems, 1)
	assert.Equal("acme/new", problems[0].Repository)
	assert.Contains(problems[0].Error, "pull request listing unavailable")
	assert.Equal(since, problems[0].Since, "the failure keeps the time it started")
}

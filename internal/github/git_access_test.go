package github

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

type failingGitTokenSource struct {
	err error
}

func (s failingGitTokenSource) Token(context.Context) (string, error) { return "", s.err }

func (failingGitTokenSource) Invalidate(string) {}

func (failingGitTokenSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{Key: tokenauth.Key{Platform: "github", Host: "github.com"}}
}

func TestCloneCredentialFailureIsReportedInStatusUntilAccessReturns(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	source := failingGitTokenSource{err: fmt.Errorf("route: %w", ErrMissingWriteIdentity)}
	syncer := NewSyncer(
		nil, openTestDB(t), gitclone.New(t.TempDir(), gitclone.HostSources{"github.com": source}),
		[]RepoRef{repo}, time.Minute, nil, nil,
	)
	var published []*SyncStatus
	syncer.onStatusChange = func(status *SyncStatus) { published = append(published, status) }

	err := syncer.ensureClone(t.Context(), repo)
	require.ErrorIs(err, ErrMissingWriteIdentity)
	assert.True(isReportedGitAccessFailure(err), "the first failure is logged by the recorder")
	problems := syncer.Status().GitAccess
	require.Len(problems, 1)
	assert.Equal("acme/widget", problems[0].Repository)
	assert.Equal("github.com", problems[0].Host)
	assert.Equal(GitAccessCredentialUnavailable, problems[0].Reason)
	since := problems[0].Since
	assert.False(since.IsZero())
	require.Len(published, 1)

	err = syncer.ensureClone(t.Context(), repo)
	assert.True(isReportedGitAccessFailure(err))
	assert.Len(published, 1, "a repeated failure publishes nothing new")
	assert.Equal(since, syncer.Status().GitAccess[0].Since)

	syncer.publishStatusLocked(&SyncStatus{})
	assert.Len(syncer.Status().GitAccess, 1, "a new sync status keeps the outstanding problem")

	require.NoError(syncer.recordGitAccess(repo, nil))
	assert.Empty(syncer.Status().GitAccess)
}

func TestGitAccessFailureReasonClassifiesOnlyCredentialFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want GitAccessReason
	}{
		{name: "success", err: nil, want: ""},
		{name: "missing token", err: fmt.Errorf("resolve: %w", tokenauth.ErrMissingToken), want: GitAccessCredentialUnavailable},
		{name: "missing route", err: fmt.Errorf("resolve: %w", &MissingRouteError{Host: "github.com", Owner: "acme", Name: "widget"}), want: GitAccessCredentialUnavailable},
		{name: "required credential", err: fmt.Errorf("clone: %w", gitclone.ErrCredentialUnavailable), want: GitAccessCredentialUnavailable},
		{name: "rejected token", err: fmt.Errorf("git clone --bare: fatal: Authentication failed for 'https://github.com/acme/widget.git/'"), want: GitAccessAuthenticationFailed},
		{name: "network", err: fmt.Errorf("git fetch: Could not resolve host: github.com"), want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, gitAccessFailureReason(tc.err))
		})
	}
}

func TestGitAccessStatusFollowsTrackedRepositories(t *testing.T) {
	tests := []struct {
		name       string
		unresolved bool
		update     func(*Syncer, RepoRef)
		wantRepo   string
	}{
		{
			name:   "removed repository",
			update: func(s *Syncer, _ RepoRef) { s.SetRepos(nil) },
		},
		{
			name:       "removed unresolved repository",
			unresolved: true,
			update:     func(s *Syncer, _ RepoRef) { s.SetRepos(nil) },
		},
		{
			name: "renamed by config reload",
			update: func(s *Syncer, repo RepoRef) {
				repo.Name = "renamed"
				s.SetRepos([]RepoRef{repo})
			},
			wantRepo: "acme/renamed",
		},
		{
			name: "renamed by provider observation",
			update: func(s *Syncer, repo RepoRef) {
				renamed := repo
				renamed.Name = "renamed"
				s.publishResolvedRepository(repo, renamed, true)
			},
			wantRepo: "acme/renamed",
		},
		{
			name: "route reused by another repository",
			update: func(s *Syncer, repo RepoRef) {
				repo.Key = platform.RepositoryIDKey(2)
				s.SetRepos([]RepoRef{repo})
			},
		},
		{
			name:       "unresolved repository gains identity",
			unresolved: true,
			update: func(s *Syncer, repo RepoRef) {
				resolved := repo
				resolved.Key = platform.RepositoryIDKey(1)
				s.publishResolvedRepository(repo, resolved, true)
			},
			wantRepo: "acme/widget",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			repo := RepoRef{Owner: "acme", Name: "widget", Key: platform.RepositoryIDKey(1)}
			if tt.unresolved {
				repo.Key = platform.RepositoryKey{}
			}
			source := failingGitTokenSource{err: ErrMissingWriteIdentity}
			clones := gitclone.New(t.TempDir(), gitclone.HostSources{"github.com": source})
			t.Cleanup(clones.Wait)
			syncer := NewSyncer(nil, nil, clones, []RepoRef{repo}, time.Minute, nil, nil)
			var published *SyncStatus
			syncer.onStatusChange = func(status *SyncStatus) { published = status }
			require.ErrorIs(syncer.ensureClone(t.Context(), repo), ErrMissingWriteIdentity)
			require.Len(syncer.Status().GitAccess, 1)
			since := syncer.Status().GitAccess[0].Since

			tt.update(syncer, repo)
			if tt.wantRepo == "" {
				assert.Empty(syncer.Status().GitAccess)
				assert.Empty(published.GitAccess, "subscribers must see the warning disappear")
				return
			}
			require.Len(syncer.Status().GitAccess, 1)
			assert.Equal(tt.wantRepo, syncer.Status().GitAccess[0].Repository)
			assert.Equal(since, syncer.Status().GitAccess[0].Since)
			assert.Equal(tt.wantRepo, published.GitAccess[0].Repository)
			require.NoError(syncer.recordGitAccess(syncer.TrackedRepos()[0], nil))
			assert.Empty(syncer.Status().GitAccess, "success at the current route clears the warning")
		})
	}
}

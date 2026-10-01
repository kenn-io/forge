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

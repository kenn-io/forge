package github

import (
	"context"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestSyncFailedCooldownBetweenFullPasses(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	client := &partialFailureMock{}
	client.listOpenIssuesErr = platform.ErrPermissionDenied
	reads := 0
	client.listOpenIssuesFn = func(context.Context, string, string) ([]*gh.Issue, error) {
		reads++
		return nil, client.listOpenIssuesErr
	}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	require.Equal(t, [][2]string{{"issues", "auth"}}, events)

	client.listOpenIssuesErr = platform.RepositoryFeatureDisabled(platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues, nil)
	require.NoError(t, s.SyncRepoOnProvider(t.Context(), platform.KindGitHub, "github.com", "acme", "widget"))
	client.listOpenIssuesErr = platform.ErrPermissionDenied
	s.runOnce(t.Context(), true, nil, nil)
	require.Equal(t, 3, reads)
	assert.Equal(t, [][2]string{{"issues", "auth"}, {"issues", "auth"}}, events)
}

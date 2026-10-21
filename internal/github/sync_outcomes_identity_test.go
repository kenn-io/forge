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

func TestSyncFailedIdentityResolutionKeepsIssueOutage(t *testing.T) {
	assert := assert.New(t)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	client := &partialFailureMock{}
	client.listOpenIssuesErr = platform.ErrPermissionDenied
	client.getRepositoryFn = func(_ context.Context, owner, name string) (*gh.Repository, error) {
		assert.Equal("acme", owner)
		assert.Equal("widget", name)
		return nil, platform.ErrPermissionDenied
	}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	require.Equal(t, [][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}, events)
	client.getRepositoryFn = func(_ context.Context, owner, name string) (*gh.Repository, error) {
		return &gh.Repository{ID: new(int64(42)), Owner: &gh.User{Login: new(owner)}, Name: new(name)}, nil
	}
	s.RunOnce(t.Context())
	assert.Equal(platform.RepositoryIDKey(42), s.TrackedRepos()[0].Key)
	assert.Equal([][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}, events, "resolving identity must not report the uninterrupted issues outage again")
}

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

func TestSyncFailedUnresolvedRenameKeepsOutage(t *testing.T) {
	assert := assert.New(t)
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	client := &partialFailureMock{}
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
		assert.Equal("acme", owner)
		assert.Equal("widget", name)
		return &gh.Repository{ID: new(int64(42)), Name: new("renamed"), Owner: &gh.User{Login: new("acme")}}, nil
	}
	issueReads := 0
	client.listOpenIssuesFn = func(_ context.Context, owner, name string) ([]*gh.Issue, error) {
		assert.Equal("acme", owner)
		assert.Equal("renamed", name)
		issueReads++
		return nil, platform.ErrPermissionDenied
	}
	s.RunOnce(t.Context())
	assert.Equal(1, issueReads)
	assert.Equal([][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}, events)
}

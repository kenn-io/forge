package e2etest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestGetPRDetailIncludesDiscussionID(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	srv, database := setupTestServer(t)

	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)

	mrID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         7,
		URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:          "Discussion test",
		Author:         "author",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	discussionID := "disc-abc123"
	platformID := int64(101)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &platformID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "needs fix",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "note-101",
		DiscussionID:   &discussionID,
		PositionJSON:   `{"new_path":"main.go","new_line":42}`,
		Resolvable:     true,
		Resolved:       false,
	}}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pulls/gitlab/acme/widget/7", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)

	var result struct {
		Events []struct {
			DiscussionID *string `json:"DiscussionID"`
			PositionJSON string  `json:"PositionJSON"`
			Resolvable   bool    `json:"Resolvable"`
			Resolved     bool    `json:"Resolved"`
		} `json:"events"`
	}
	err = json.NewDecoder(rr.Body).Decode(&result)
	require.NoError(err)

	require.Len(result.Events, 1)
	assert.NotNil(result.Events[0].DiscussionID)
	assert.Equal("disc-abc123", *result.Events[0].DiscussionID)
	assert.JSONEq(`{"new_path":"main.go","new_line":42}`, result.Events[0].PositionJSON)
	assert.True(result.Events[0].Resolvable)
	assert.False(result.Events[0].Resolved)
}

type gitLabDiscussionProvider struct {
	ref platform.RepoRef
}

func (p *gitLabDiscussionProvider) Platform() platform.Kind {
	return platform.KindGitLab
}

func (p *gitLabDiscussionProvider) Host() string {
	return p.ref.Host
}

func (p *gitLabDiscussionProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		DiscussionReply:   true,
		DiscussionResolve: true,
	}
}

func (p *gitLabDiscussionProvider) GetRepository(_ context.Context, _ platform.RepoRef) (platform.Repository, error) {
	return platform.Repository{
		Ref:                p.ref,
		PlatformID:         p.ref.PlatformID,
		PlatformExternalID: p.ref.PlatformExternalID,
		DefaultBranch:      p.ref.DefaultBranch,
		WebURL:             p.ref.WebURL,
		CloneURL:           p.ref.CloneURL,
	}, nil
}

func (p *gitLabDiscussionProvider) ListRepositories(context.Context, string, platform.RepositoryListOptions) ([]platform.Repository, error) {
	return nil, nil
}

func (p *gitLabDiscussionProvider) ListOpenMergeRequests(context.Context, platform.RepoRef) ([]platform.MergeRequest, error) {
	return nil, nil
}

func (p *gitLabDiscussionProvider) GetMergeRequest(context.Context, platform.RepoRef, int) (platform.MergeRequest, error) {
	return platform.MergeRequest{}, nil
}

func (p *gitLabDiscussionProvider) ListMergeRequestEvents(context.Context, platform.RepoRef, int) ([]platform.MergeRequestEvent, error) {
	return nil, nil
}

func (p *gitLabDiscussionProvider) ListOpenIssues(context.Context, platform.RepoRef) ([]platform.Issue, error) {
	return nil, nil
}

func (p *gitLabDiscussionProvider) GetIssue(context.Context, platform.RepoRef, int) (platform.Issue, error) {
	return platform.Issue{}, nil
}

func (p *gitLabDiscussionProvider) ListIssueEvents(context.Context, platform.RepoRef, int) ([]platform.IssueEvent, error) {
	return nil, nil
}

func (p *gitLabDiscussionProvider) ListReleases(context.Context, platform.RepoRef) ([]platform.Release, error) {
	return nil, nil
}

func (p *gitLabDiscussionProvider) ListTags(context.Context, platform.RepoRef) ([]platform.Tag, error) {
	return nil, nil
}

func (p *gitLabDiscussionProvider) ListCIChecks(context.Context, platform.RepoRef, string) ([]platform.CICheck, error) {
	return nil, nil
}

func TestGitLabRepoCapabilitiesIncludeDiscussions(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.com",
		Owner:              "acme",
		Name:               "widget",
		RepoPath:           "acme/widget",
		PlatformID:         1234,
		PlatformExternalID: "gid://gitlab/Project/1234",
		WebURL:             "https://gitlab.com/acme/widget",
		CloneURL:           "https://gitlab.com/acme/widget.git",
		DefaultBranch:      "main",
	}

	provider := &gitLabDiscussionProvider{ref: ref}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "gitlab.com",
		RepoPath:           "acme/widget",
		PlatformRepoID:     1234,
		PlatformExternalID: "gid://gitlab/Project/1234",
		WebURL:             "https://gitlab.com/acme/widget",
		CloneURL:           "https://gitlab.com/acme/widget.git",
		DefaultBranch:      "main",
	}

	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	syncer.RunOnce(ctx)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repo/gitlab/acme/widget", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)

	var result struct {
		Capabilities struct {
			DiscussionReply   bool `json:"discussion_reply"`
			DiscussionResolve bool `json:"discussion_resolve"`
		} `json:"capabilities"`
	}
	err = json.NewDecoder(rr.Body).Decode(&result)
	require.NoError(err)

	assert.True(result.Capabilities.DiscussionReply)
	assert.True(result.Capabilities.DiscussionResolve)
}

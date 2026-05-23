package e2etest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

	// Track mutation calls for test assertions
	replyToDiscussionCalls []replyToDiscussionCall
	resolveDiscussionCalls []resolveDiscussionCall
}

type replyToDiscussionCall struct {
	Ref          platform.RepoRef
	Number       int
	DiscussionID string
	Body         string
}

type resolveDiscussionCall struct {
	Ref          platform.RepoRef
	Number       int
	DiscussionID string
	Resolved     bool
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

func (p *gitLabDiscussionProvider) ReplyToDiscussion(
	_ context.Context,
	ref platform.RepoRef,
	number int,
	discussionID string,
	body string,
) (platform.MergeRequestEvent, error) {
	p.replyToDiscussionCalls = append(p.replyToDiscussionCalls, replyToDiscussionCall{
		Ref:          ref,
		Number:       number,
		DiscussionID: discussionID,
		Body:         body,
	})
	return platform.MergeRequestEvent{
		Repo:               ref,
		PlatformID:         99999,
		PlatformExternalID: "99999",
		MergeRequestNumber: number,
		EventType:          "issue_comment",
		Author:             "test-user",
		Body:               body,
		CreatedAt:          time.Now().UTC(),
		DedupeKey:          "reply-" + discussionID,
		DiscussionID:       discussionID,
	}, nil
}

func (p *gitLabDiscussionProvider) ResolveDiscussion(
	_ context.Context,
	ref platform.RepoRef,
	number int,
	discussionID string,
	resolved bool,
) error {
	p.resolveDiscussionCalls = append(p.resolveDiscussionCalls, resolveDiscussionCall{
		Ref:          ref,
		Number:       number,
		DiscussionID: discussionID,
		Resolved:     resolved,
	})
	return nil
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

func TestReplyToDiscussionE2E(t *testing.T) {
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

	// Create an MR to reply to
	dbRepo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)
	require.NotNil(dbRepo)

	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         dbRepo.ID,
		PlatformID:     1001,
		Number:         7,
		URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:          "Test MR",
		Author:         "author",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	// Valid 40-char hex discussion ID
	discussionID := "abc123def456789012345678901234567890abcd"
	body := `{"body":"This is my reply"}`
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/discussions/"+discussionID+"/reply",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusCreated, rr.Code, "response: %s", rr.Body.String())

	// Verify the provider was called with correct arguments
	require.Len(provider.replyToDiscussionCalls, 1)
	call := provider.replyToDiscussionCalls[0]
	assert.Equal(7, call.Number)
	assert.Equal(discussionID, call.DiscussionID)
	assert.Equal("This is my reply", call.Body)
	assert.Equal("acme", call.Ref.Owner)
	assert.Equal("widget", call.Ref.Name)

	// Verify the reply event was persisted
	var result struct {
		Author       string  `json:"Author"`
		Body         string  `json:"Body"`
		DiscussionID *string `json:"DiscussionID"`
	}
	err = json.NewDecoder(rr.Body).Decode(&result)
	require.NoError(err)
	assert.Equal("test-user", result.Author)
	assert.Equal("This is my reply", result.Body)
	require.NotNil(result.DiscussionID)
	assert.Equal(discussionID, *result.DiscussionID)
}

func TestReplyToDiscussionRejectsInvalidDiscussionID(t *testing.T) {
	require := require.New(t)
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

	dbRepo2, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)
	require.NotNil(dbRepo2)

	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         dbRepo2.ID,
		PlatformID:     1001,
		Number:         7,
		URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:          "Test MR",
		Author:         "author",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	// Test various invalid discussion IDs (URL-safe but invalid for GitLab)
	invalidIDs := []string{
		"..-..-..-..-etc-passwd---------",          // path traversal attempt (40 chars)
		"abc-2F-123--------------------------",     // would-be encoded slash (40 chars)
		"short",                                    // too short
		"abc123def456789012345678901234",           // 31 chars, not 40
		"ABCDEF1234567890123456789012345678901234", // uppercase not allowed
		"xyz-invalid-chars-1234567890123456789012", // non-hex chars
	}

	for _, invalidID := range invalidIDs {
		body := `{"body":"test"}`
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/pulls/gitlab/acme/widget/7/discussions/"+invalidID+"/reply",
			strings.NewReader(body),
		)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		// Should not succeed - either 400 (validation) or 500 (internal error from invalid format)
		require.NotEqual(http.StatusCreated, rr.Code, "should reject invalid discussion ID: %s", invalidID)
	}

	// Verify provider was never called with invalid IDs
	require.Empty(provider.replyToDiscussionCalls)
}

func TestResolveDiscussionE2E(t *testing.T) {
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

	dbRepo3, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)
	require.NotNil(dbRepo3)

	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         dbRepo3.ID,
		PlatformID:     1001,
		Number:         7,
		URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:          "Test MR",
		Author:         "author",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	// Valid 40-char hex discussion ID
	discussionID := "abc123def456789012345678901234567890abcd"
	body := `{"resolved":true}`
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/discussions/"+discussionID+"/resolve",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	// Verify the provider was called with correct arguments
	require.Len(provider.resolveDiscussionCalls, 1)
	call := provider.resolveDiscussionCalls[0]
	assert.Equal(7, call.Number)
	assert.Equal(discussionID, call.DiscussionID)
	assert.True(call.Resolved)
	assert.Equal("acme", call.Ref.Owner)
	assert.Equal("widget", call.Ref.Name)
}

func TestDiscussionEndpointsRequireCapability(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()

	// Use the default test server which has GitHub provider (no discussion capabilities)
	srv, database := setupTestServer(t)

	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)

	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         7,
		URL:            "https://github.com/acme/widget/pull/7",
		Title:          "Test PR",
		Author:         "author",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	discussionID := "abc123def456789012345678901234567890abcd"

	// Reply should fail for GitHub (no discussion capability)
	body := `{"body":"test"}`
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/discussions/"+discussionID+"/reply",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusConflict, rr.Code)

	var errResp struct {
		Code string `json:"code"`
	}
	err = json.NewDecoder(rr.Body).Decode(&errResp)
	require.NoError(err)
	require.Equal("unsupportedCapability", errResp.Code)

	// Resolve should also fail for GitHub
	body = `{"resolved":true}`
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/discussions/"+discussionID+"/resolve",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusConflict, rr.Code)
}

func TestDiscussionEndpointsRejectNonExistentMR(t *testing.T) {
	require := require.New(t)
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

	// Note: We do NOT create an MR in the database, so MR #999 does not exist locally.
	discussionID := "abc123def456789012345678901234567890abcd"

	// Reply should fail with 404 before calling provider
	body := `{"body":"test reply"}`
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/999/discussions/"+discussionID+"/reply",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusNotFound, rr.Code)
	require.Empty(provider.replyToDiscussionCalls, "provider should not be called for non-existent MR")

	// Resolve should also fail with 404 before calling provider
	body = `{"resolved":true}`
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/999/discussions/"+discussionID+"/resolve",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusNotFound, rr.Code)
	require.Empty(provider.resolveDiscussionCalls, "provider should not be called for non-existent MR")
}

func TestResolveDiscussionUpdatesLocalState(t *testing.T) {
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

	dbRepo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)
	require.NotNil(dbRepo)

	mrID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         dbRepo.ID,
		PlatformID:     1001,
		Number:         7,
		URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:          "Test MR",
		Author:         "author",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	// Create a discussion event that is NOT resolved
	discussionID := "abc123def456789012345678901234567890abcd"
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
		Resolvable:     true,
		Resolved:       false,
	}}))

	// Verify initial state
	events, err := database.ListMREvents(ctx, mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.False(events[0].Resolved)

	// Resolve the discussion
	body := `{"resolved":true}`
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/discussions/"+discussionID+"/resolve",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	// Verify the local state was updated
	events, err = database.ListMREvents(ctx, mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.True(events[0].Resolved, "local event should be marked as resolved")

	// Now unresolve it
	body = `{"resolved":false}`
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/discussions/"+discussionID+"/resolve",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	// Verify the local state was updated back to unresolved
	events, err = database.ListMREvents(ctx, mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.False(events[0].Resolved, "local event should be marked as unresolved")
}

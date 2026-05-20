package server

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/db"
	ghclient "github.com/wesm/middleman/internal/github"
	"github.com/wesm/middleman/internal/ratelimit"
	"github.com/wesm/middleman/internal/testutil/dbtest"
)

func TestDeriveOperationAvailability(t *testing.T) {
	allCaps := providerCapabilitiesResponse{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReleases:      true,
		ReadCI:            true,
		ReadLabels:        true,
		CommentMutation:   true,
		StateMutation:     true,
		MergeMutation:     true,
		ReviewMutation:    true,
		WorkflowApproval:  true,
		ReadyForReview:    true,
		IssueMutation:     true,
		LabelMutation:     true,
	}
	mergePR := operationDescriptor{
		name:                 operationMergePR,
		requiredCapabilities: []string{capabilityMergeMutation},
	}
	addLabel := operationDescriptor{
		name:                 operationAddLabel,
		requiredCapabilities: []string{capabilityReadLabels, capabilityLabelMutation},
	}
	resetAt := time.Date(2026, 5, 19, 14, 35, 0, 0, time.UTC)
	limitedRate := rateLimitAvailability{
		limited: true,
		reason:  "github.com rate-limited; retry at 14:35",
		retryAt: resetAt.UTC().Format(time.RFC3339),
	}
	repoCanMerge := db.Repo{ViewerCanMerge: true}
	repoCannotMerge := db.Repo{ViewerCanMerge: false}

	tests := []struct {
		name     string
		op       operationDescriptor
		caps     providerCapabilitiesResponse
		repo     db.Repo
		rate     rateLimitAvailability
		expected OperationAvailability
	}{
		{
			name:     "healthy merge_pr is available",
			op:       mergePR,
			caps:     allCaps,
			repo:     repoCanMerge,
			expected: OperationAvailability{Available: true},
		},
		{
			name: "missing required capability surfaces unsupported_capability",
			op:   mergePR,
			caps: func() providerCapabilitiesResponse {
				c := allCaps
				c.MergeMutation = false
				return c
			}(),
			repo: repoCanMerge,
			expected: OperationAvailability{
				Code:               availabilityCodeUnsupportedCapability,
				UnavailableReason:  "Provider does not support merge_mutation",
				RequiredCapability: capabilityMergeMutation,
			},
		},
		{
			name: "first missing capability wins for multi-cap operations",
			op:   addLabel,
			caps: func() providerCapabilitiesResponse {
				c := allCaps
				c.ReadLabels = false
				c.LabelMutation = false
				return c
			}(),
			repo: repoCanMerge,
			expected: OperationAvailability{
				Code:               availabilityCodeUnsupportedCapability,
				UnavailableReason:  "Provider does not support read_labels",
				RequiredCapability: capabilityReadLabels,
			},
		},
		{
			name: "viewer without merge permission cannot merge even when capability exists",
			op:   mergePR,
			caps: allCaps,
			repo: repoCannotMerge,
			expected: OperationAvailability{
				Code:              availabilityCodeViewerCannotMerge,
				UnavailableReason: "You do not have permission to merge in this repository",
			},
		},
		{
			name:     "viewer_can_merge gate is scoped to merge_pr only",
			op:       operationDescriptor{name: operationClosePR, requiredCapabilities: []string{capabilityStateMutation}},
			caps:     allCaps,
			repo:     repoCannotMerge,
			expected: OperationAvailability{Available: true},
		},
		{
			name: "rate-limited host blocks even when capability and permission exist",
			op:   mergePR,
			caps: allCaps,
			repo: repoCanMerge,
			rate: limitedRate,
			expected: OperationAvailability{
				Code:              availabilityCodeRateLimited,
				UnavailableReason: "github.com rate-limited; retry at 14:35",
				RetryAt:           resetAt.UTC().Format(time.RFC3339),
			},
		},
		{
			name: "unsupported capability takes precedence over rate limit",
			op:   mergePR,
			caps: func() providerCapabilitiesResponse {
				c := allCaps
				c.MergeMutation = false
				return c
			}(),
			repo: repoCanMerge,
			rate: limitedRate,
			expected: OperationAvailability{
				Code:               availabilityCodeUnsupportedCapability,
				UnavailableReason:  "Provider does not support merge_mutation",
				RequiredCapability: capabilityMergeMutation,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveOperationAvailability(tc.op, tc.caps, tc.repo, tc.rate)
			require.Equal(t, tc.expected, got)
		})
	}
}

func TestFormatRateLimit(t *testing.T) {
	assert := Assert.New(t)

	resetAt := time.Date(2026, 5, 19, 14, 35, 0, 0, time.UTC)
	got := formatRateLimit("github.com", &resetAt)
	assert.True(got.limited)
	assert.Equal("github.com rate-limited; retry at 14:35", got.reason)
	assert.Equal(resetAt.UTC().Format(time.RFC3339), got.retryAt)

	unknown := formatRateLimit("ghe.example.com", nil)
	assert.True(unknown.limited)
	assert.Equal("ghe.example.com rate-limited", unknown.reason)
	assert.Empty(unknown.retryAt)
}

func TestRepoOperationsWireShape(t *testing.T) {
	// The set of operation field names on RepoOperations is a wire
	// contract. Renaming a json tag here breaks any frontend pinned
	// to an older schema, so the test enumerates the full set as a
	// guard against accidental renames.
	require := require.New(t)
	fields := reflect.VisibleFields(reflect.TypeFor[RepoOperations]())
	tags := make([]string, 0, len(fields))
	for _, f := range fields {
		tag := f.Tag.Get("json")
		require.NotEmpty(tag, "field %s missing json tag", f.Name)
		tags = append(tags, tag)
	}
	require.Equal([]string{
		"merge_pr",
		"close_pr",
		"reopen_pr",
		"mark_ready_for_review",
		"submit_review",
		"add_comment",
		"add_label",
		"remove_label",
		"close_issue",
		"reopen_issue",
		"approve_workflow",
	}, tags)
}

// newServerWithRateTracker builds a Server whose syncer is wired
// with a single REST rate tracker for github.com so tests can flip
// the host into the paused state by calling UpdateFromRate.
func newServerWithRateTracker(t *testing.T) (*Server, *db.DB, *ratelimit.RateTracker) {
	t.Helper()
	database := dbtest.Open(t)
	rt := ghclient.NewRateTracker(database, "github.com", "rest")
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		map[string]*ghclient.RateTracker{ratelimit.RateBucketKey("github", "github.com"): rt},
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, database, rt
}

func TestAPIRepoResponseIncludesOperationsHealthy(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, database, _ := newServerWithRateTracker(t)
	_, err := database.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	// Schema default is viewer_can_merge=1, so no extra update is
	// needed; the fresh row already satisfies the merge permission.

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/repo/github/acme/widget", nil)
	require.Equal(http.StatusOK, rr.Code)

	var resp repoResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))

	merge := resp.Operations.MergePR
	assert.True(merge.Available)
	assert.Empty(merge.Code)
	assert.Empty(merge.UnavailableReason)
}

func TestAPIRepoResponseIncludesOperationsRateLimited(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, database, rt := newServerWithRateTracker(t)
	_, err := database.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	// Schema default already grants viewer merge permission.

	resetAt := time.Now().UTC().Add(30 * time.Minute)
	rt.UpdateFromRate(ratelimit.Rate{Limit: 5000, Remaining: 0, Reset: resetAt})

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/repo/github/acme/widget", nil)
	require.Equal(http.StatusOK, rr.Code)

	var resp repoResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))

	merge := resp.Operations.MergePR
	assert.False(merge.Available)
	assert.Equal(availabilityCodeRateLimited, merge.Code)
	assert.Contains(merge.UnavailableReason, "rate-limited")
	assert.NotEmpty(merge.RetryAt)
}

func TestAPIRepoResponseIncludesOperationsGraphQLPauseDoesNotBlockREST(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	// Mutations in middleman are REST-backed, so a paused GraphQL
	// tracker must leave merge_pr available; this guards against
	// the earlier behavior that treated either tracker's pause as
	// blocking every operation.
	database := dbtest.Open(t)
	restRT := ghclient.NewRateTracker(database, "github.com", "rest")
	gqlRT := ghclient.NewRateTracker(database, "github.com", "graphql")
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		map[string]*ghclient.RateTracker{ratelimit.RateBucketKey("github", "github.com"): restRT},
		nil,
	)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcher("fake-token", "github.com", gqlRT, nil),
	})
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	_, err := database.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)

	// Pause GraphQL by reporting zero remaining requests with a
	// future reset; leave REST untouched.
	resetAt := time.Now().UTC().Add(30 * time.Minute)
	gqlRT.UpdateFromRate(ratelimit.Rate{Limit: 5000, Remaining: 0, Reset: resetAt})

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/repo/github/acme/widget", nil)
	require.Equal(http.StatusOK, rr.Code)

	var resp repoResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))

	merge := resp.Operations.MergePR
	assert.True(merge.Available, "merge_pr is REST-backed; GraphQL pause must not block it")
	assert.Empty(merge.Code)
}

func TestAPIRepoResponseIncludesOperationsViewerCannotMerge(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, database := setupTestServer(t)
	repoID, err := database.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	// Schema defaults viewer_can_merge to 1; flip to false so the
	// merge gate (not the capability gate) decides this case.
	require.NoError(database.UpdateRepoViewerCanMerge(t.Context(), repoID, false))

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/repo/github/acme/widget", nil)
	require.Equal(http.StatusOK, rr.Code)

	var resp repoResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))

	merge := resp.Operations.MergePR
	assert.False(merge.Available)
	assert.Equal(availabilityCodeViewerCannotMerge, merge.Code)
	assert.Empty(merge.RequiredCapability)

	// Other operations remain available because viewer_can_merge only
	// gates merge_pr.
	assert.True(resp.Operations.ClosePR.Available)
}

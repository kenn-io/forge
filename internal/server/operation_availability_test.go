package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/ratelimit"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/tokenauth"
)

func TestDeriveOperationAvailability(t *testing.T) {
	allCaps := providerCapabilitiesResponse{
		ReadRepositories:    true,
		ReadMergeRequests:   true,
		ReadIssues:          true,
		ReadComments:        true,
		ReadReleases:        true,
		ReadCI:              true,
		ReadLabels:          true,
		CommentMutation:     true,
		StateMutation:       true,
		MergeMutation:       true,
		ReviewMutation:      true,
		ReviewDraftMutation: true,
		WorkflowApproval:    true,
		ReadyForReview:      true,
		DraftMutation:       true,
		IssueMutation:       true,
		LabelMutation:       true,
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
		name      string
		op        operationDescriptor
		caps      providerCapabilitiesResponse
		repo      db.Repo
		rate      rateLimitAvailability
		writeCred writeCredentialGate
		expected  OperationAvailability
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
			name: "review draft operation uses draft capability without requiring submitted reviews",
			op:   descReviewDraft,
			caps: func() providerCapabilitiesResponse {
				c := allCaps
				c.ReviewMutation = false
				c.ReviewDraftMutation = true
				return c
			}(),
			repo:     repoCanMerge,
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
		{
			name: "missing write credential blocks every mutation",
			op:   mergePR,
			caps: allCaps,
			repo: repoCanMerge,
			writeCred: writeCredentialGate{
				code:   availabilityCodeMissingWriteCredential,
				reason: "No user credential for writes on github.com",
			},
			expected: OperationAvailability{
				Code:              availabilityCodeMissingWriteCredential,
				UnavailableReason: "No user credential for writes on github.com",
			},
		},
		{
			name: "write credential gate takes precedence over viewer and rate gates",
			op:   mergePR,
			caps: allCaps,
			repo: repoCannotMerge,
			rate: limitedRate,
			writeCred: writeCredentialGate{
				code:   availabilityCodeWriteCredentialError,
				reason: "Resolving the write credential for github.com failed",
			},
			expected: OperationAvailability{
				Code:              availabilityCodeWriteCredentialError,
				UnavailableReason: "Resolving the write credential for github.com failed",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveOperationAvailability(tc.op, tc.caps, tc.repo, tc.rate, tc.writeCred)
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
		"mark_draft",
		"submit_review",
		"review_draft",
		"add_comment",
		"edit_comment",
		"add_label",
		"remove_label",
		"set_assignees",
		"set_reviewers",
		"create_issue",
		"close_issue",
		"reopen_issue",
		"approve_workflow",
		"update_content",
		"reply_review_thread",
		"resolve_review_thread",
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
		"github.com": ghclient.NewGraphQLFetcher(
			testTokenSource("fake-token"), "github.com", gqlRT, nil,
		),
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

func TestAPIRepoResponseOperationsGateOnWriteTrackerWhenSplit(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	// With a GitHub App handling sync reads, mutations ride the user's
	// PAT and must gate on the write credential's budget: an exhausted
	// app tracker must not disable PAT-backed writes, and an exhausted
	// write tracker must disable them even while reads still flow.
	database := dbtest.Open(t)
	restRT := ghclient.NewRateTracker(database, "github.com", "rest")
	writeRT := ghclient.NewRateTracker(database, "github.com", "rest_write")
	writeGQLRT := ghclient.NewRateTracker(database, "github.com", "graphql_write")
	key := ratelimit.RateBucketKey("github", "github.com")
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		map[string]*ghclient.RateTracker{key: restRT},
		nil,
	)
	syncer.SetWriteRateTrackers(map[string]*ghclient.RateTracker{key: writeRT})
	syncer.SetWriteGQLRateTrackers(map[string]*ghclient.RateTracker{key: writeGQLRT})
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	_, err := database.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)

	// App (sync) budget exhausted, PAT healthy: writes stay available.
	resetAt := time.Now().UTC().Add(30 * time.Minute)
	restRT.UpdateFromRate(ratelimit.Rate{Limit: 5000, Remaining: 0, Reset: resetAt})

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/repo/github/acme/widget", nil)
	require.Equal(http.StatusOK, rr.Code)
	var resp repoResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.Operations.MergePR.Available,
		"app budget exhaustion must not disable PAT-backed writes")

	// PAT REST budget exhausted: REST writes gate, but the GraphQL
	// mutation (ready-for-review) follows its own write bucket.
	writeRT.UpdateFromRate(ratelimit.Rate{Limit: 5000, Remaining: 0, Reset: resetAt})

	rr = doJSON(t, srv, http.MethodGet, "/api/v1/repo/github/acme/widget", nil)
	require.Equal(http.StatusOK, rr.Code)
	resp = repoResponse{}
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	merge := resp.Operations.MergePR
	assert.False(merge.Available, "PAT exhaustion must disable writes")
	assert.Equal(availabilityCodeRateLimited, merge.Code)
	assert.True(resp.Operations.MarkReadyForReview.Available,
		"REST write exhaustion must not gate the GraphQL-backed mutation")
	assert.True(resp.Operations.MarkDraft.Available,
		"REST write exhaustion must not gate GraphQL-backed draft conversion")

	// PAT GraphQL budget exhausted: only the GraphQL mutation gates.
	writeRT.UpdateFromRate(ratelimit.Rate{Limit: 5000, Remaining: 4000, Reset: resetAt})
	writeGQLRT.UpdateFromRate(ratelimit.Rate{Limit: 5000, Remaining: 0, Reset: resetAt})

	rr = doJSON(t, srv, http.MethodGet, "/api/v1/repo/github/acme/widget", nil)
	require.Equal(http.StatusOK, rr.Code)
	resp = repoResponse{}
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.Operations.MergePR.Available)
	rfr := resp.Operations.MarkReadyForReview
	assert.False(rfr.Available, "write GraphQL exhaustion must gate ready-for-review")
	assert.Equal(availabilityCodeRateLimited, rfr.Code)
	draft := resp.Operations.MarkDraft
	assert.False(draft.Available, "write GraphQL exhaustion must gate draft conversion")
	assert.Equal(availabilityCodeRateLimited, draft.Code)
}

func TestAPIRepoResponseOperationsRequireWriteCredentialWhenSplit(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	// A host can sync with only the GitHub App configured, but
	// mutations skip the app candidate so they stay attributed to the
	// user. With no PAT or gh credential behind the app, every write
	// operation must report missing_write_credential instead of
	// offering an action that would fail auth at request time.
	t.Setenv("SPLIT_WRITE_CRED_PAT", "")
	t.Setenv("SPLIT_WRITE_CRED_PAT_NEW", "user-pat")
	srv, set := newSplitTestServer(t, tokenauth.Candidate{
		Kind: tokenauth.SourceKindEnv, EnvName: "SPLIT_WRITE_CRED_PAT",
	})

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/repo/github/acme/widget", nil)
	require.Equal(http.StatusOK, rr.Code)
	var resp repoResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	merge := resp.Operations.MergePR
	assert.False(merge.Available, "app-only host has no credential for mutations")
	assert.Equal(availabilityCodeMissingWriteCredential, merge.Code)
	assert.Contains(merge.UnavailableReason, "github.com")
	comment := resp.Operations.AddComment
	assert.False(comment.Available, "every operation is a mutation; all must gate")
	assert.Equal(availabilityCodeMissingWriteCredential, comment.Code)

	// A config reload that re-points the chain at a resolvable
	// credential must take effect immediately: the probe cache is
	// keyed by the canonical chain, not just the host, so the stale
	// missing-credential verdict cannot outlive the chain it probed.
	set.Upsert(splitTestDescriptor(tokenauth.Candidate{
		Kind: tokenauth.SourceKindEnv, EnvName: "SPLIT_WRITE_CRED_PAT_NEW",
	}))
	rr = doJSON(t, srv, http.MethodGet, "/api/v1/repo/github/acme/widget", nil)
	require.Equal(http.StatusOK, rr.Code)
	resp = repoResponse{}
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.Operations.MergePR.Available,
		"a re-pointed chain must bypass the cached verdict without waiting out the TTL")
	assert.True(resp.Operations.AddComment.Available)
}

func TestAPIRepoResponseOperationsDistinguishWriteCredentialErrors(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	// A resolver failure (unreadable token file, broken gh helper) is
	// not a missing credential: the UI must not tell the user to
	// configure a PAT when the real problem is a broken helper.
	srv, _ := newSplitTestServer(t, tokenauth.Candidate{
		Kind:     tokenauth.SourceKindFile,
		FilePath: filepath.Join(t.TempDir(), "does-not-exist.token"),
	})

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/repo/github/acme/widget", nil)
	require.Equal(http.StatusOK, rr.Code)
	var resp repoResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	merge := resp.Operations.MergePR
	assert.False(merge.Available)
	assert.Equal(availabilityCodeWriteCredentialError, merge.Code,
		"a resolver failure must not masquerade as a missing credential")
	assert.Contains(merge.UnavailableReason, "github.com")
	assert.NotContains(merge.UnavailableReason, "does-not-exist.token",
		"the reason must stay redacted; no raw resolver error in the wire response")
}

// splitTestDescriptor builds the github.com chain of a split host:
// an installed GitHub App candidate followed by the given user write
// candidate.
func splitTestDescriptor(writeCandidate tokenauth.Candidate) tokenauth.Descriptor {
	return tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: "github.com"},
		Candidates: []tokenauth.Candidate{
			{
				Kind:           tokenauth.SourceKindGitHubApp,
				Host:           "github.com",
				FilePath:       "/keys/app.pem",
				AppID:          7,
				InstallationID: 11,
			},
			writeCandidate,
		},
	}
}

// newSplitTestServer builds a Server whose github.com token chain is
// a split chain (installed app + the given write candidate) so tests
// can exercise mutation availability gating.
func newSplitTestServer(
	t *testing.T, writeCandidate tokenauth.Candidate,
) (*Server, *tokenauth.SourceSet) {
	t.Helper()
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubApp: func(context.Context, tokenauth.Candidate) (string, time.Time, error) {
			return "ghs_probe", time.Now().Add(time.Hour), nil
		},
	})
	set.Upsert(splitTestDescriptor(writeCandidate))
	srv := New(database, syncer, nil, "/", nil, ServerOptions{TokenSources: set})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	_, err := database.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(t, err)
	return srv, set
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

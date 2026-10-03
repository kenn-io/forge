package e2etest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/forgejo"
)

// TestForgejoSetPRStateStampsMergeableStateObservedAt exercises the
// github-state route's state-edit success path against a non-GitHub
// provider: the mergeable state observation time must be stamped from the
// mutation request time whenever the edit response reports a concrete
// mergeable value, while review decision and CI status — which this
// response cannot represent — stay carried from the stored row untouched.
// The server here has no injectable clock (unlike internal/server's own
// package tests, which can set srv.now directly), so this test cannot use a
// literal expected time or an advancing clock. Instead it brackets the
// specific outbound provider call: the fake handler records the wall-clock
// time it was hit, and the stored observed time must not be after that —
// a capture point moved to after the provider call returns would always be
// later than the moment the fake received the request, so this still
// catches a before/after regression at this call site.
func TestForgejoSetPRStateStampsMergeableStateObservedAt(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	const htmlURL = "https://codeberg.org/acme/widget/pulls/7"
	// The seeded row's updated_at is time.Now() at seed time; the edit
	// response must report a strictly newer updated_at or the snapshot's
	// monotonic guard silently rejects the commit.
	providerUpdatedAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	var handlerMu sync.Mutex
	var handlerHitAt time.Time
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/repos/acme/widget/pulls/7":
			handlerMu.Lock()
			handlerHitAt = time.Now().UTC()
			handlerMu.Unlock()
			_, _ = fmt.Fprintf(w, `{
				"id": 1001, "number": 7, "state": "closed", "title": "Label target PR",
				"html_url": %q,
				"user": {"id": 1, "login": "author"},
				"head": {"ref": "feature", "sha": "head-sha", "repo": {"id": 1, "name": "widget", "full_name": "acme/widget"}},
				"base": {"ref": "main", "sha": "base-sha", "repo": {"id": 1, "name": "widget", "full_name": "acme/widget"}},
				"mergeable": true,
				"created_at": %q,
				"updated_at": %q,
				"closed_at": %q
			}`, htmlURL, providerUpdatedAt, providerUpdatedAt, providerUpdatedAt)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fake.Close)

	client, err := forgejo.NewClient(
		platform.DefaultForgejoHost,
		staticTokenSource("token"),
		forgejo.WithBaseURLForTesting(fake.URL), forgejo.WithTransport(http.DefaultTransport),
	)
	require.NoError(err)

	database := dbtest.Open(t)
	repoID := seedProviderRepo(t, database, platform.KindForgejo, platform.DefaultForgejoHost)
	reviewObservedAt := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	ciObservedAt := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	now := time.Now().UTC()
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:                   repoID,
		PlatformID:               1001,
		Number:                   7,
		Title:                    "Label target PR",
		Author:                   "author",
		State:                    "open",
		PlatformHeadSHA:          "head-sha",
		ReviewDecision:           "approved",
		ReviewDecisionObservedAt: &reviewObservedAt,
		CIStatus:                 "success",
		CIObservedAt:             &ciObservedAt,
		CreatedAt:                now,
		UpdatedAt:                now,
		LastActivityAt:           now,
	})
	require.NoError(err)

	srv := newLabelTestServer(t, database, client, platform.KindForgejo, platform.DefaultForgejoHost)

	requestSentAt := time.Now().UTC()
	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/forgejo/acme/widget/7/github-state",
		map[string]any{"state": "closed"},
	)
	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	handlerMu.Lock()
	hitAt := handlerHitAt
	handlerMu.Unlock()
	require.False(hitAt.IsZero(), "fake provider was never called")

	pr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal(db.MergeRequestStateClosed, pr.State)
	assert.Equal("clean", pr.MergeableState)
	if assert.NotNil(pr.MergeableStateObservedAt) {
		assert.False(pr.MergeableStateObservedAt.Before(requestSentAt),
			"observed time must not precede the mutation request")
		assert.False(pr.MergeableStateObservedAt.After(hitAt),
			"observed time must be captured before the outbound provider call, not after it")
	}
	// Forgejo's edit response cannot represent review decision or CI
	// state; closing preserves both values and their observation times
	// carried from the stored row.
	assert.Equal("approved", pr.ReviewDecision)
	if assert.NotNil(pr.ReviewDecisionObservedAt) {
		assert.True(pr.ReviewDecisionObservedAt.Equal(reviewObservedAt))
	}
	assert.Equal("success", pr.CIStatus)
	if assert.NotNil(pr.CIObservedAt) {
		assert.True(pr.CIObservedAt.Equal(ciObservedAt))
	}
}

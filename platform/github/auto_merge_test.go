package github

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
)

func newAutoMergeTestClient(t *testing.T, mutationResponse string) (*Client, *[]graphQLRequest) {
	t.Helper()
	var requests []graphQLRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req graphQLRequest
		if err := json.UnmarshalRead(r.Body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests = append(requests, req)
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_node"}}}}`))
			return
		}
		_, _ = w.Write([]byte(mutationResponse))
	}))
	t.Cleanup(srv.Close)
	return &Client{
		now:             time.Now,
		graphQLContext:  func(ctx context.Context) context.Context { return ctx },
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL,
	}, &requests
}

func TestEnablePullRequestAutoMergeSendsMergeChoices(t *testing.T) {
	require := require.New(t)
	c, requests := newAutoMergeTestClient(t,
		`{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"autoMergeRequest":{"enabledAt":"2026-11-04T12:00:00Z"}}}}}`)

	err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "Title", "Body", "squash", "head-sha")
	require.NoError(err)

	require.Len(*requests, 2)
	assert.Equal(t, map[string]any{
		"pullRequestId":   "PR_node",
		"mergeMethod":     "SQUASH",
		"commitHeadline":  "Title",
		"commitBody":      "Body",
		"expectedHeadOid": "head-sha",
	}, (*requests)[1].Variables)
}

func TestEnablePullRequestAutoMergeOmitsDefaultMessage(t *testing.T) {
	require := require.New(t)
	c, requests := newAutoMergeTestClient(t,
		`{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"autoMergeRequest":{"enabledAt":"2026-11-04T12:00:00Z"}}}}}`)

	require.NoError(c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "", "", "merge", ""))

	require.Len(*requests, 2)
	vars := (*requests)[1].Variables
	assert.Equal(t, "MERGE", vars["mergeMethod"])
	assert.Nil(t, vars["commitHeadline"])
	assert.Nil(t, vars["commitBody"])
	assert.Nil(t, vars["expectedHeadOid"])
}

func TestEnablePullRequestAutoMergeReportsRefusal(t *testing.T) {
	c, _ := newAutoMergeTestClient(t,
		`{"data":{"enablePullRequestAutoMerge":null},"errors":[{"type":"UNPROCESSABLE","message":"Pull request Auto merge is not allowed for this repository"}]}`)

	err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "", "", "merge", "head-sha")
	require.ErrorContains(t, err, "Auto merge is not allowed for this repository")
	require.ErrorIs(t, err, &platform.Error{Code: platform.ErrCodeConflict}, "GitHub answered; auto-merge is off")
}

func TestEnablePullRequestAutoMergeLostAnswerIsNotARefusal(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_node"}}}}`))
			return
		}
		http.Error(w, "upstream timeout", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		now:             time.Now,
		graphQLContext:  func(ctx context.Context) context.Context { return ctx },
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL,
	}

	err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "", "", "merge", "head-sha")
	require.Error(t, err)
	require.NotErrorIs(t, err, &platform.Error{Code: platform.ErrCodeConflict},
		"GitHub may have enabled auto-merge before the response was lost")
}

func TestAdaptPRCarriesAutoMergeRequest(t *testing.T) {
	enabledAt := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	off, err := NormalizePullRequest(platform.RepoRef{}, AdaptPR(&GraphQLPR{Number: 7, State: "OPEN"}))
	require.NoError(t, err)
	assert.False(t, off.AutoMergeEnabled)

	on, err := NormalizePullRequest(platform.RepoRef{}, AdaptPR(&GraphQLPR{
		Number: 7, State: "OPEN",
		AutoMergeRequest: &struct{ EnabledAt *time.Time }{EnabledAt: &enabledAt},
	}))
	require.NoError(t, err)
	assert.True(t, on.AutoMergeEnabled)
}

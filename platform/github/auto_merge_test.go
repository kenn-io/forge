package github

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

	enabledAt, err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "Title", "Body", "squash", "head-sha")
	require.NoError(err)
	assert.Equal(t, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC), enabledAt.UTC(), "GitHub's own enable time")

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

	_, err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "", "", "merge", "")
	require.NoError(err)

	require.Len(*requests, 2)
	vars := (*requests)[1].Variables
	assert.Equal(t, "MERGE", vars["mergeMethod"])
	assert.Nil(t, vars["commitHeadline"])
	assert.Nil(t, vars["commitBody"])
	assert.Nil(t, vars["expectedHeadOid"])
}

func TestEnablePullRequestAutoMergeReportsRefusalAsConflict(t *testing.T) {
	// The error GitHub returned for a merged pull request, observed live.
	c, _ := newAutoMergeTestClient(t,
		`{"data":{"enablePullRequestAutoMerge":null},"errors":[{"type":"UNPROCESSABLE","path":["enablePullRequestAutoMerge"],"message":"Pull request Pull request is already merged"}]}`)

	_, err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "", "", "merge", "head-sha")
	require.ErrorContains(t, err, "Pull request is already merged")
	require.ErrorIs(t, err, &platform.Error{Code: platform.ErrCodeConflict}, "GitHub's refusal reaches the user as a conflict")
}

func TestEnablePullRequestAutoMergeExecutionErrorLeavesOutcomeUnknown(t *testing.T) {
	// GraphQL can return a null field with an error after the mutation ran,
	// for example when resolving its payload fails.
	c, _ := newAutoMergeTestClient(t,
		`{"data":{"enablePullRequestAutoMerge":null},"errors":[{"path":["enablePullRequestAutoMerge","pullRequest"],"message":"Something went wrong while executing your query."}]}`)

	_, err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "", "", "merge", "head-sha")
	require.Error(t, err)
	require.NotErrorIs(t, err, &platform.Error{Code: platform.ErrCodeConflict},
		"an execution error does not prove GitHub left auto-merge off")
}

func TestEnablePullRequestAutoMergeAcceptsAutoMergeAlreadyOn(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_node","autoMergeRequest":{"enabledAt":"2026-11-04T12:00:00Z"}}}}}`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		now:             time.Now,
		graphQLContext:  func(ctx context.Context) context.Context { return ctx },
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL,
	}

	enabledAt, err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "", "", "merge", "head-sha")
	require.NoError(t, err)
	assert.Equal(t, 1, requests, "a retry after a lost answer finds auto-merge on and sends no second mutation")
	assert.Equal(t, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC), enabledAt.UTC(), "the time auto-merge went on, not now")
}

func TestEnablePullRequestAutoMergeTrustsAcceptanceBesideErrors(t *testing.T) {
	c, _ := newAutoMergeTestClient(t,
		`{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"autoMergeRequest":{"enabledAt":"2026-11-04T12:00:00Z"}}}},"errors":[{"type":"FORBIDDEN","message":"Resource not accessible"}]}`)

	_, err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "", "", "merge", "head-sha")
	require.NoError(t, err)
}

func TestEnablePullRequestAutoMergeFailedReadIsNotARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream timeout", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		now:             time.Now,
		graphQLContext:  func(ctx context.Context) context.Context { return ctx },
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL,
	}

	_, err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "", "", "merge", "head-sha")
	require.Error(t, err)
	require.NotErrorIs(t, err, &platform.Error{Code: platform.ErrCodeConflict},
		"a failed read says nothing about an auto-merge an earlier attempt may have enabled")
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

	_, err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "", "", "merge", "head-sha")
	require.Error(t, err)
	require.NotErrorIs(t, err, &platform.Error{Code: platform.ErrCodeConflict},
		"GitHub may have enabled auto-merge before the response was lost")
}

func TestPullRequestAutoMergeStateReadsSettingAndRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want platform.AutoMergeState
	}{
		{
			name: "allowed and off",
			body: `{"data":{"repository":{"autoMergeAllowed":true,"pullRequest":{"id":"PR_node","autoMergeRequest":null}}}}`,
			want: platform.AutoMergeState{RepositoryAllows: true},
		},
		{
			name: "disallowed",
			body: `{"data":{"repository":{"autoMergeAllowed":false,"pullRequest":{"id":"PR_node","autoMergeRequest":null}}}}`,
			want: platform.AutoMergeState{},
		},
		{
			name: "mergeable now, so GitHub would refuse",
			body: `{"data":{"repository":{"autoMergeAllowed":true,"pullRequest":{"id":"PR_node","mergeStateStatus":"UNSTABLE","autoMergeRequest":null}}}}`,
			want: platform.AutoMergeState{RepositoryAllows: true, MergeableNow: true},
		},
		{
			name: "allowed and waiting on required checks",
			body: `{"data":{"repository":{"autoMergeAllowed":true,"pullRequest":{"id":"PR_node","mergeStateStatus":"BLOCKED","autoMergeRequest":null}}}}`,
			want: platform.AutoMergeState{RepositoryAllows: true},
		},
		{
			name: "on",
			body: `{"data":{"repository":{"autoMergeAllowed":true,"pullRequest":{"id":"PR_node","autoMergeRequest":{"enabledAt":"2026-11-04T12:00:00Z"}}}}}`,
			want: platform.AutoMergeState{
				RepositoryAllows: true, Enabled: true, EnabledAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			c := &Client{
				now:             time.Now,
				graphQLContext:  func(ctx context.Context) context.Context { return ctx },
				httpClient:      srv.Client(),
				graphQLEndpoint: srv.URL,
			}

			state, err := c.PullRequestAutoMergeState(t.Context(), "acme", "widget", 7)
			require.NoError(t, err)
			assert.Equal(t, tt.want, state)
		})
	}
}

// bearerTransport sends every request as one credential.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

// newSplitCredentialClient reads as the sync App and writes as the user, as
// a split-auth host does; answer serves each request by its credential.
func newSplitCredentialClient(t *testing.T, answer func(token string, w http.ResponseWriter)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		answer(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), w)
	}))
	t.Cleanup(srv.Close)
	return &Client{
		now:             time.Now,
		graphQLContext:  func(ctx context.Context) context.Context { return ctx },
		httpClient:      &http.Client{Transport: bearerTransport{token: "sync-app", base: srv.Client().Transport}},
		httpWriteClient: &http.Client{Transport: bearerTransport{token: "merge-user", base: srv.Client().Transport}},
		graphQLEndpoint: srv.URL,
	}
}

func TestPullRequestAutoMergeStateReadsAsTheMergeCredential(t *testing.T) {
	c := newSplitCredentialClient(t, func(token string, w http.ResponseWriter) {
		allowed := token == "merge-user"
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"autoMergeAllowed":%t,"pullRequest":{"id":"PR_node","mergeStateStatus":"BLOCKED","autoMergeRequest":null}}}}`, allowed)
	})

	state, err := c.PullRequestAutoMergeState(t.Context(), "acme", "widget", 7)
	require.NoError(t, err)
	assert.True(t, state.RepositoryAllows, "the credential that would enable auto-merge decides, not the sync App")
}

func TestEnablePullRequestAutoMergeFailsForADeniedMergeCredential(t *testing.T) {
	c := newSplitCredentialClient(t, func(token string, w http.ResponseWriter) {
		if token != "merge-user" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}],"data":{"repository":null}}`)
	})

	_, err := c.EnablePullRequestAutoMerge(t.Context(), "acme", "widget", 7, "", "", "merge", "head-sha")
	require.ErrorContains(t, err, "Resource not accessible by integration")
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

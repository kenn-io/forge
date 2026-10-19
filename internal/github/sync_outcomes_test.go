package github

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucketdc"
	"go.kenn.io/forge/platform/gitealike"
)

func TestSyncFailedFullPassRecovery(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	client := &partialFailureMock{}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	for i, failure := range []bool{true, true, false, true} {
		client.listOpenIssuesErr = nil
		if failure {
			client.listOpenIssuesErr = &gh.ErrorResponse{Response: &http.Response{StatusCode: 503}}
		}
		s.RunOnce(t.Context())
		want := 1
		if i == 3 {
			want = 2
		}
		require.Len(t, events, want)
	}
	assert.Equal(t, [][2]string{{"issues", "provider"}, {"issues", "provider"}}, events)
}

func TestSyncFailedRetainsUnreadRepositories(t *testing.T) {
	a := RepoRef{Owner: "acme", Name: "a"}
	b := RepoRef{Owner: "acme", Name: "b"}
	for _, tc := range []struct {
		name      string
		next      map[string]error
		tracked   []RepoRef
		wantLater int
	}{
		{"recovery overlaps new failure", map[string]error{"a": nil, "b": platform.ErrNotFound}, []RepoRef{a, b}, 1},
		{"cadence skipped", map[string]error{"b": nil}, []RepoRef{a, b}, 1},
		{"budget refused", map[string]error{"a": platform.ErrSyncBudgetExhausted, "b": nil}, []RepoRef{a, b}, 1},
		{"disabled", map[string]error{"a": platform.ErrRepositoryFeatureDisabled}, []RepoRef{a, b}, 1},
		{"sync disabled", map[string]error{"a": platform.ErrSyncDisabled}, []RepoRef{a, b}, 1},
		{"untracked", map[string]error{"b": nil}, []RepoRef{b}, 2},
		{"archived", map[string]error{"b": nil}, []RepoRef{{Owner: "acme", Name: "a", Archived: true}, b}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events [][2]string
			s := &Syncer{onSyncFailed: func(source, reason string) { events = append(events, [2]string{source, reason}) }}
			first := &syncPass{}
			first.observe(a, "issues", platform.ErrNotFound)
			s.finishSyncPass(first, []RepoRef{a, b})
			second := &syncPass{}
			for name, err := range tc.next {
				second.observe(RepoRef{Owner: "acme", Name: name}, "issues", err)
			}
			s.finishSyncPass(second, tc.tracked)
			require.Len(t, events, 1)
			later := &syncPass{}
			later.observe(b, "issues", platform.ErrNotFound)
			s.finishSyncPass(later, tc.tracked)
			assert.Len(t, events, tc.wantLater)
		})
	}
}

func TestSyncFailedScopeCauses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		issuesErr error
		want      [][2]string
	}{
		{"separate reasons", &gh.ErrorResponse{Response: &http.Response{StatusCode: 503}}, [][2]string{{"merge_requests", "auth"}, {"issues", "provider"}}},
		{"budget cannot hide auth", platform.ErrSyncBudgetExhausted, [][2]string{{"merge_requests", "auth"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
			id, err := reposeed.Seed(t.Context(), d, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
			require.NoError(t, err)
			now := time.Now().UTC()
			_, err = d.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: id, PlatformID: 1, Number: 1, State: "open", Title: "Example", CreatedAt: now, UpdatedAt: now})
			require.NoError(t, err)
			client := &partialFailureMock{}
			client.listOpenIssuesErr = tc.issuesErr
			client.getPullRequestFn = func(context.Context, string, string, int) (*gh.PullRequest, error) {
				return nil, platform.ErrPermissionDenied
			}
			s := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
			var events [][2]string
			s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
			s.RunOnce(t.Context())
			assert.Equal(t, tc.want, events)
			// A pull-list failure leaves issues unread, preserving its outage.
			client.listOpenPRsErr = platform.ErrPermissionDenied
			s.RunOnce(t.Context())
			assert.Equal(t, tc.want, events)
		})
	}
}

func TestSyncFailedPassEligibility(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	client := &partialFailureMock{}
	client.listOpenIssuesErr = platform.ErrNotFound
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.runOnce(t.Context(), true, nil, []RepoRef{repo})
	require.Empty(t, events)
	s.runOnce(t.Context(), true, nil, nil)
	require.Equal(t, [][2]string{{"issues", "not_found"}}, events)
	client.listOpenIssuesErr = nil
	s.runOnce(t.Context(), true, nil, []RepoRef{repo})
	client.listOpenIssuesErr = platform.ErrNotFound
	s.RunOnce(t.Context())
	require.Len(t, events, 1)
	client.listOpenIssuesErr = nil
	s.runOnce(t.Context(), true, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client.listOpenIssuesFn = func(context.Context, string, string) ([]*gh.Issue, error) { cancel(); return nil, platform.ErrNotFound }
	s.RunOnce(ctx)
	assert.Len(t, events, 1)
	client.listOpenIssuesFn = nil
	client.listOpenIssuesErr = platform.ErrNotFound
	s.RunOnce(t.Context())
	assert.Len(t, events, 2)
}

func TestSyncFailedCredentialRoute(t *testing.T) {
	repo := RepoRef{Owner: "unrouted", Name: "widget", PlatformHost: "github.com"}
	client := &mockClient{}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	router, err := NewHostRouter("github.com", nil)
	require.NoError(t, err)
	s.SetGitHubRouters(map[string]*HostRouter{"github.com": router})
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	assert.Equal(t, [][2]string{{"merge_requests", "auth"}, {"issues", "auth"}}, events)
}

func TestSyncFailedRefsAndChecks(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	clones := gitclone.New(t.TempDir(), gitclone.HostSources{"github.com": failingGitTokenSource{err: ErrMissingWriteIdentity}})
	t.Cleanup(clones.Wait)
	client := &mockClient{listCheckRunsForRefFn: func(_ context.Context, _, _, sha string) ([]*gh.CheckRun, error) {
		if sha == "b" {
			return nil, platform.ErrPermissionDenied
		}
		return nil, nil
	}}
	s := NewSyncer(map[string]Client{"github.com": client}, openTestDB(t), clones, []RepoRef{repo}, time.Minute, nil, nil)
	pass := &syncPass{}
	ctx := context.WithValue(t.Context(), syncPassKey{}, pass)
	require.ErrorIs(t, s.ensureClone(ctx, repo), ErrMissingWriteIdentity)
	for _, sha := range []string{"a", "b", "c"} {
		_, err := s.fetchGitHubCIStatus(ctx, repo, 1, sha)
		require.NoError(t, err)
	}
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.finishSyncPass(pass, []RepoRef{repo})
	assert.Equal(t, [][2]string{{"checks", "auth"}, {"refs", "auth"}}, events)
}

func TestSyncFailureReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"github", &gh.ErrorResponse{Response: &http.Response{StatusCode: 503}}, "provider"},
		{"gitlab", &gitlab.ErrorResponse{StatusCode: 401}, "auth"},
		{"forgejo and gitea", &gitealike.HTTPError{StatusCode: 404}, "not_found"},
		{"bitbucket", &bitbucket.UnexpectedResponseStatusError{StatusCode: 429}, "rate_limited"},
		{"network", &net.DNSError{Err: "unavailable"}, "network"},
		{"deadline", context.DeadlineExceeded, "network"},
		{"contract", platform.ErrProviderContract, "provider"},
		{"rate limit", platform.ErrRateLimited, "rate_limited"},
		{"unknown", errors.New("private error details"), "other"},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, syncFailureReason(fmt.Errorf("read: %w", tc.err))) })
	}
}

func TestSyncFailedBitbucketDCLookup(t *testing.T) {
	repo := RepoRef{Platform: platform.KindBitbucket, PlatformHost: "bitbucket.example.com", Owner: "PROJECT", Name: "widget", Key: platform.RepositoryIDKey(42)}
	client, err := bitbucketdc.NewClient(repo.PlatformHost, failingGitTokenSource{}, platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	}), nil)
	require.NoError(t, err)
	registry, err := platform.NewRegistry(client)
	require.NoError(t, err)
	s := NewSyncerWithRegistry(registry, openTestDB(t), nil, []RepoRef{repo}, time.Minute, nil, nil)
	var events [][2]string
	s.SetOnSyncFailed(func(source, reason string) { events = append(events, [2]string{source, reason}) })
	s.RunOnce(t.Context())
	assert.Equal(t, [][2]string{{"merge_requests", "not_found"}}, events)
}

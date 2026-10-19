package collect_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/landedwork/collect"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/github"
	gittest "go.kenn.io/kit/git/test"
)

// largePullRequest is a merged pull request with more commits than GitHub's
// pull request commit list returns. Its branch was opened on base, then
// updated by merging the advanced target (mainUpdate) before more work landed.
type largePullRequest struct {
	repo                           *gittest.Repo
	base, mainUpdate, intervalBase string
	sourceHead, merge              string
	sources                        []string
}

func commitTree(r *gittest.Repo, msg string, parents ...string) string {
	args := []string{"commit-tree", "HEAD^{tree}", "-m", msg}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	return strings.TrimSpace(r.Run(args...))
}

func newLargePullRequest(t *testing.T, before, after int) largePullRequest {
	t.Helper()
	r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	pr := largePullRequest{repo: r}
	pr.base = r.CommitFile("work.txt", "old\n", "base")
	tip := pr.base
	for i := range before {
		tip = commitTree(r, "topic "+strconv.Itoa(i), tip)
	}
	pr.mainUpdate = r.CommitFile("main.txt", "update\n", "target update")
	tip = commitTree(r, "merge target into topic", tip, pr.mainUpdate)
	for i := range after {
		tip = commitTree(r, "topic after update "+strconv.Itoa(i), tip)
	}
	pr.sourceHead = tip
	pr.intervalBase = r.CommitFile("later.txt", "later\n", "later target work")
	pr.merge = commitTree(r, "merge topic", pr.intervalBase, pr.sourceHead)
	r.Run("update-ref", "refs/heads/main", pr.merge)
	r.Run("reset", "--hard", pr.merge)
	mergeBase := strings.TrimSpace(r.Run("merge-base", pr.mainUpdate, pr.sourceHead))
	pr.sources = strings.Fields(r.Run("rev-list", "--reverse", "--topo-order", pr.sourceHead, "^"+mergeBase))
	return pr
}

type largePullRequestAPI struct {
	pr *largePullRequest
	// reportedBase and reportedCount are the merged pull request's base.sha
	// and commits fields.
	reportedBase  string
	reportedCount int
	compareCalls  int
	commitListing int
}

// compare answers GitHub's three-dot comparison: commits reachable from head
// and not from the merge base of base and head, oldest first, in pages.
func (a *largePullRequestAPI) compare(t *testing.T, req *http.Request, base, head string) (string, http.Header) {
	t.Helper()
	a.compareCalls++
	r := a.pr.repo
	mergeBase := strings.TrimSpace(r.Run("merge-base", base, head))
	all := strings.Fields(r.Run("rev-list", "--reverse", "--topo-order", head, "^"+mergeBase))
	perPage, err := strconv.Atoi(req.URL.Query().Get("per_page"))
	require.NoError(t, err)
	page, err := strconv.Atoi(req.URL.Query().Get("page"))
	require.NoError(t, err)
	start := min((page-1)*perPage, len(all))
	end := min(start+perPage, len(all))
	header := make(http.Header)
	if end < len(all) {
		header.Set("Link", fmt.Sprintf(`<https://api.github.com%s?per_page=%d&page=%d>; rel="next"`, req.URL.Path, perPage, page+1))
	}
	type commit struct {
		SHA string `json:"sha"`
	}
	body := struct {
		Status       string   `json:"status"`
		TotalCommits int      `json:"total_commits"`
		Commits      []commit `json:"commits"`
	}{Status: "ahead", TotalCommits: len(all)}
	for _, sha := range all[start:end] {
		body.Commits = append(body.Commits, commit{SHA: sha})
	}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	return string(data), header
}

func (a *largePullRequestAPI) client(t *testing.T) *github.Provider {
	t.Helper()
	pr := a.pr
	hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, header := "", make(http.Header)
		switch path := req.URL.Path; {
		case path == "/repos/example/project":
			body = `{"id":12,"name":"project","owner":{"login":"example"}}`
		case path == "/repos/example/project/pulls/3":
			body = fmt.Sprintf(`{"id":7,"number":3,"merged":true,"merge_commit_sha":%q,"commits":%d,"base":{"ref":"main","sha":%q,"repo":{"id":12}},"head":{"sha":%q,"repo":{"id":12}}}`,
				pr.merge, a.reportedCount, a.reportedBase, pr.sourceHead)
		case path == "/repos/example/project/pulls/3/commits":
			// GitHub lists at most the newest 250 commits of a pull request.
			a.commitListing++
			rows := []string{}
			for _, sha := range pr.sources[max(0, len(pr.sources)-250):] {
				rows = append(rows, fmt.Sprintf(`{"sha":%q}`, sha))
			}
			body = "[" + strings.Join(rows, ",") + "]"
		case strings.HasPrefix(path, "/repos/example/project/compare/"):
			base, head, ok := strings.Cut(strings.TrimPrefix(path, "/repos/example/project/compare/"), "...")
			require.True(t, ok, path)
			body, header = a.compare(t, req, base, head)
		case strings.HasPrefix(path, "/repos/example/project/commits/") && strings.HasSuffix(path, "/pulls"):
			sha := strings.TrimSuffix(strings.TrimPrefix(path, "/repos/example/project/commits/"), "/pulls")
			body = `[]`
			if sha == pr.merge || slices.Contains(pr.sources, sha) {
				body = `[{"id":7,"number":3,"base":{"repo":{"id":12}}}]`
			}
		default:
			require.Fail(t, "unexpected request", req.URL.String())
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	client, err := github.NewClient(github.ClientConfig{Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
	require.NoError(t, err)
	reader, err := github.NewProvider(github.ProviderConfig{Host: "github.com", Client: client, Clock: time.Now})
	require.NoError(t, err)
	return reader
}

func collectLarge(t *testing.T, pr largePullRequest, api *largePullRequestAPI) (*landedwork.Interval, collect.Result) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	l := landedwork.Limits{Records: 100000, Nodes: 100000, InputBytes: 1 << 24, OutputBytes: 1 << 24}
	prepared, err := landedwork.Prepare(ctx, pr.repo.Root, landedwork.Bounds{Repository: query.Bounds.Repository, Base: pr.intervalBase, Head: pr.merge}, l)
	require.NoError(t, err)
	require.True(t, prepared.Query().Complete)
	collected, err := collect.Collect(ctx, api.client(t), route, prepared.Query(), collect.Limits{Calls: 10000, Records: 100000, OutputBytes: 1 << 24})
	require.NoError(t, err)
	return prepared, collected
}

func TestGitHubPullRequestBeyondCommitListCap(t *testing.T) {
	pr := newLargePullRequest(t, 200, 60)
	require.Greater(t, len(pr.sources), 250, "the fixture must exceed GitHub's pull request commit list")
	require.Contains(t, pr.sources, pr.sourceHead)
	require.NotContains(t, pr.sources, pr.mainUpdate, "the branch update's target commit is not pull request work")

	t.Run("proves from the reported base", func(t *testing.T) {
		assert, require := assert.New(t), require.New(t)
		api := &largePullRequestAPI{pr: &pr, reportedBase: pr.mainUpdate, reportedCount: len(pr.sources)}
		prepared, collected := collectLarge(t, pr, api)
		assert.True(collected.Evidence.Inventory.Complete, collected.Evidence.Inventory.Reason)
		require.Len(collected.Evidence.Candidates, 1)
		candidate := collected.Evidence.Candidates[0]
		assert.True(candidate.SourceComplete)
		assert.Equal(pr.sources, candidate.Source)
		assert.Zero(api.commitListing, "a capped commit list cannot be complete")
		assert.Equal(3, api.compareCalls)

		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		analysis, err := landedwork.Analyze(ctx, prepared, collected.Evidence, landedwork.Limits{Records: 100000, Nodes: 100000, InputBytes: 1 << 24, OutputBytes: 1 << 24})
		require.NoError(err)
		assert.Empty(analysis.Coverage.Gaps)
		assert.True(analysis.Coverage.Complete)
		require.Len(analysis.Landings, 1)
		assert.Equal([]string{"merge"}, analysis.Landings[0].Proofs)
		assert.Equal(pr.merge, analysis.Landings[0].Terminal)
		assert.ElementsMatch(pr.sources, analysis.Landings[0].Introduced)
	})

	t.Run("a base that disagrees with the reported count stays truncated", func(t *testing.T) {
		assert, require := assert.New(t), require.New(t)
		// From the original base, the comparison also contains the target
		// commit merged into the branch, so it cannot be the pull request's list.
		api := &largePullRequestAPI{pr: &pr, reportedBase: pr.base, reportedCount: len(pr.sources)}
		_, collected := collectLarge(t, pr, api)
		assert.Equal("provider_truncation", collected.Evidence.Inventory.Reason)
		require.Len(collected.Observations, 1)
		assert.Equal("provider_truncation", collected.Observations[0].Reason)
		assert.Equal("source", collected.Observations[0].FailureStage)
		require.Len(collected.Evidence.Candidates, 1)
		assert.False(collected.Evidence.Candidates[0].SourceComplete)
	})

	// GitHub's comparison reports at most 10,000 commits, so a count at that
	// ceiling cannot be distinguished from a truncated comparison.
	for _, tc := range []struct {
		count   int
		compare bool
	}{{9999, true}, {10000, false}, {10001, false}} {
		t.Run(fmt.Sprintf("comparison ceiling %d", tc.count), func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			api := &largePullRequestAPI{pr: &pr, reportedBase: pr.mainUpdate, reportedCount: tc.count}
			_, collected := collectLarge(t, pr, api)
			require.Len(collected.Observations, 1)
			assert.Equal("provider_truncation", collected.Observations[0].Reason)
			assert.Equal(tc.compare, api.compareCalls > 0)
			if tc.compare {
				assert.Equal("source", collected.Observations[0].FailureStage)
			} else {
				assert.Equal("detail", collected.Observations[0].FailureStage)
			}
			require.Len(collected.Evidence.Candidates, 1)
			assert.False(collected.Evidence.Candidates[0].SourceComplete)
		})
	}
}

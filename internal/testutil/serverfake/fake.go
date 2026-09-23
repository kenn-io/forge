package serverfake

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	gh "github.com/google/go-github/v91/github"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/stacks"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/testutil/processjob"
	"go.kenn.io/forge/internal/testutil/testsignal"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"
	platformgithub "go.kenn.io/forge/platform/github"
	"golang.org/x/sync/semaphore"
)

func AcquireRootWorkspaceGitSlot(t *testing.T) {
	t.Helper()
	require.NoError(t, RootWorkspaceGitSemaphore.Acquire(t.Context(), 1))
	t.Cleanup(func() { RootWorkspaceGitSemaphore.Release(1) })
}

func (p *ApiTestGitLabProvider) ApplyReviewSuggestions(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	input platform.ApplyReviewSuggestionsInput,
) (*platform.AppliedReviewSuggestions, error) {
	if p.ApplySuggestionsStarted != nil {
		p.ApplySuggestionsStarted <- struct{}{}
	}
	if p.ApplySuggestionsRelease != nil {
		<-p.ApplySuggestionsRelease
	}
	if p.ApplySuggestionsErr != nil {
		return nil, p.ApplySuggestionsErr
	}
	p.Mu.Lock()
	defer p.Mu.Unlock()
	p.AppliedSuggestions = append(p.AppliedSuggestions, input)
	result := platform.AppliedReviewSuggestions{CommitSHA: "suggestion-commit-sha"}
	if p.ApplySuggestionResult != nil {
		result = *p.ApplySuggestionResult
	}
	providerHead := result.CommitSHA
	if p.ApplySuggestionHead != "" {
		providerHead = p.ApplySuggestionHead
	}
	for i := range p.MergeRequests {
		if p.MergeRequests[i].Number == number && providerHead != "" {
			p.MergeRequests[i].HeadSHA = providerHead
		}
	}
	if p.CancelAfterApply != nil {
		p.CancelAfterApply()
	}
	if p.ApplySuggestionsErrAfterMutation != nil {
		return nil, p.ApplySuggestionsErrAfterMutation
	}
	if p.ApplySuggestionReturnsNil {
		return nil, nil
	}
	return &result, nil
}

func (p *ApiTestGitLabProvider) Capabilities() platform.Capabilities {
	if p.CapabilitiesValue != nil {
		return *p.CapabilitiesValue
	}
	return platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReleases:      true,
		ReadCI:            true,
	}
}

func (p *ApiTestGitLabProvider) GetIssue(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (platform.Issue, error) {
	for _, issue := range p.Issues {
		if issue.Number == number {
			return issue, nil
		}
	}
	return platform.Issue{}, fmt.Errorf("missing issue %d", number)
}

func (p *ApiTestGitLabProvider) GetMergeRequest(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (platform.MergeRequest, error) {
	p.Mu.Lock()
	var found platform.MergeRequest
	foundOK := false
	if p.MergeRequestDetail != nil {
		if mr, ok := p.MergeRequestDetail[number]; ok {
			found = mr
			foundOK = true
		}
	}
	if !foundOK {
		for _, mr := range p.MergeRequests {
			if mr.Number == number {
				found = mr
				foundOK = true
				break
			}
		}
	}
	p.Mu.Unlock()
	if foundOK {
		if p.BlockNextMRFetch.CompareAndSwap(true, false) {
			if p.MrFetchStarted != nil {
				p.MrFetchStarted <- struct{}{}
			}
			if p.MrFetchRelease != nil {
				<-p.MrFetchRelease
			}
		}
		return found, nil
	}
	return platform.MergeRequest{}, fmt.Errorf("missing merge request %d", number)
}

func (p *ApiTestGitLabProvider) GetRepository(
	context.Context,
	platform.RepoRef,
) (platform.Repository, error) {
	return platform.Repository{
		Ref:                p.Ref,
		PlatformID:         p.Ref.PlatformID,
		PlatformExternalID: p.Ref.PlatformExternalID,
		DefaultBranch:      p.Ref.DefaultBranch,
		WebURL:             p.Ref.WebURL,
		CloneURL:           p.Ref.CloneURL,
	}, nil
}

func (p *ApiTestGitLabProvider) Host() string {
	return p.Ref.Host
}

func (p *ApiTestGitLabProvider) ListCIChecks(
	_ context.Context,
	_ platform.RepoRef,
	sha string,
) ([]platform.CICheck, error) {
	if p.CiErr != nil {
		return nil, p.CiErr
	}
	return p.CiChecks[sha], nil
}

func (p *ApiTestGitLabProvider) ListIssueEvents(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) ([]platform.IssueEvent, error) {
	return p.IssueEvents[number], nil
}

func (p *ApiTestGitLabProvider) ListMergeRequestEvents(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) ([]platform.MergeRequestEvent, error) {
	return p.MergeRequestEvents[number], nil
}

func (p *ApiTestGitLabProvider) ListMergeRequestReviewThreads(
	ctx context.Context,
	repo platform.RepoRef,
	number int,
) ([]platform.MergeRequestReviewThread, error) {
	if p.ReviewThreadsFn != nil {
		return p.ReviewThreadsFn(ctx, repo, number)
	}
	if p.ReviewThreadsErr != nil {
		return nil, p.ReviewThreadsErr
	}
	return p.ReviewThreads, nil
}

func (p *ApiTestGitLabProvider) ListOpenIssues(
	context.Context,
	platform.RepoRef,
) ([]platform.Issue, error) {
	return slices.Clone(p.Issues), nil
}

func (p *ApiTestGitLabProvider) ListOpenMergeRequests(
	context.Context,
	platform.RepoRef,
) ([]platform.MergeRequest, error) {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	return slices.Clone(p.MergeRequests), nil
}

func (p *ApiTestGitLabProvider) ListReleases(
	context.Context,
	platform.RepoRef,
) ([]platform.Release, error) {
	return p.Releases, nil
}

func (p *ApiTestGitLabProvider) ListRepositories(
	context.Context,
	string,
	platform.RepositoryListOptions,
) ([]platform.Repository, error) {
	repo, err := p.GetRepository(context.Background(), p.Ref)
	if err != nil {
		return nil, err
	}
	return []platform.Repository{repo}, nil
}

func (p *ApiTestGitLabProvider) ListTags(
	context.Context,
	platform.RepoRef,
) ([]platform.Tag, error) {
	return p.Tags, nil
}

func (p *ApiTestGitLabProvider) OperationRateLimitBuckets(
	operation platform.OperationName,
) ([]platform.RateLimitBucket, bool) {
	if p.RateLimitBuckets == nil {
		return nil, false
	}
	buckets, ok := p.RateLimitBuckets[operation]
	return buckets, ok
}

func (p *ApiTestGitLabProvider) Platform() platform.Kind {
	return p.Ref.Platform
}

func (p *ApiTestGitLabProvider) PublishDiffReviewDraft(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
	input platform.PublishDiffReviewDraftInput,
) (*platform.PublishedDiffReview, error) {
	p.PublishedReviews = append(p.PublishedReviews, input)
	return &platform.PublishedDiffReview{SubmittedAt: time.Now().UTC()}, p.PublishReviewErr
}

func (p *ApiTestGitLabProvider) ResolveDiffReviewThread(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
	providerThreadID string,
) error {
	p.ResolvedThreads = append(p.ResolvedThreads, providerThreadID)
	return nil
}

func (p *ApiTestGitLabProvider) UnresolveDiffReviewThread(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
	providerThreadID string,
) error {
	p.UnresolvedThreads = append(p.UnresolvedThreads, providerThreadID)
	return nil
}

type ApiTestGitLabProvider struct {
	Mu                               sync.Mutex
	Ref                              platform.RepoRef
	CapabilitiesValue                *platform.Capabilities
	MergeRequests                    []platform.MergeRequest
	MergeRequestDetail               map[int]platform.MergeRequest
	MergeRequestEvents               map[int][]platform.MergeRequestEvent
	Issues                           []platform.Issue
	IssueEvents                      map[int][]platform.IssueEvent
	Releases                         []platform.Release
	Tags                             []platform.Tag
	CiChecks                         map[string][]platform.CICheck
	CiErr                            error
	ReviewThreads                    []platform.MergeRequestReviewThread
	ReviewThreadsErr                 error
	ReviewThreadsFn                  func(context.Context, platform.RepoRef, int) ([]platform.MergeRequestReviewThread, error)
	PublishedReviews                 []platform.PublishDiffReviewDraftInput
	PublishReviewErr                 error
	AppliedSuggestions               []platform.ApplyReviewSuggestionsInput
	ApplySuggestionsErr              error
	ApplySuggestionsErrAfterMutation error
	ApplySuggestionResult            *platform.AppliedReviewSuggestions
	ApplySuggestionReturnsNil        bool
	ApplySuggestionHead              string
	ApplySuggestionsStarted          chan struct{}
	ApplySuggestionsRelease          <-chan struct{}
	CancelAfterApply                 func()
	BlockNextMRFetch                 atomic.Bool
	MrFetchStarted                   chan struct{}
	MrFetchRelease                   <-chan struct{}
	RateLimitBuckets                 map[platform.OperationName][]platform.RateLimitBucket
	ResolvedThreads                  []string
	UnresolvedThreads                []string
}

func (t *ApiTestGitealikeTransport) CreateIssue(
	_ context.Context,
	ref platform.RepoRef,
	title string,
	body string,
) (gitealike.IssueDTO, error) {
	if t.NextIssueID == 0 {
		t.NextIssueID = 1
	}
	if t.NextIssueIndex == 0 {
		t.NextIssueIndex = 1
	}
	id := t.NextIssueID
	t.NextIssueID++
	number := t.NextIssueIndex
	t.NextIssueIndex++
	t.MutationCalls = append(t.MutationCalls, "create_issue:"+title)
	issue := gitealike.IssueDTO{
		ID:      id,
		Index:   number,
		HTMLURL: fmt.Sprintf("https://%s/%s/%s/issues/%d", ref.Host, ref.Owner, ref.Name, number),
		Title:   title,
		User:    gitealike.UserDTO{UserName: "mutation-bot"},
		State:   "open",
		Body:    body,
		Created: time.Now().UTC().Truncate(time.Second),
		Updated: time.Now().UTC().Truncate(time.Second),
	}
	t.Issues = append(t.Issues, issue)
	return issue, nil
}

func (t *ApiTestGitealikeTransport) CreateIssueComment(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	body string,
) (gitealike.CommentDTO, error) {
	if t.NextCommentID == 0 {
		t.NextCommentID = 1
	}
	id := t.NextCommentID
	t.NextCommentID++
	t.MutationCalls = append(t.MutationCalls, fmt.Sprintf("create_comment:%d:%s", number, body))
	comment := gitealike.CommentDTO{
		ID:      id,
		User:    gitealike.UserDTO{UserName: "mutation-bot"},
		Body:    body,
		Created: time.Now().UTC().Truncate(time.Second),
		Updated: time.Now().UTC().Truncate(time.Second),
	}
	if issue := t.FindIssue(number); issue != nil {
		t.IssueComments = UpsertComment(t.IssueComments, comment)
		return comment, nil
	}
	t.PullComments = UpsertComment(t.PullComments, comment)
	return comment, nil
}

func (t *ApiTestGitealikeTransport) CreatePullReview(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	opts gitealike.ReviewOptions,
) (gitealike.ReviewDTO, error) {
	if t.FindPull(number) == nil {
		return gitealike.ReviewDTO{}, platform.ErrNotFound
	}
	t.MutationCalls = append(t.MutationCalls, fmt.Sprintf("review:%d:%s:%s", number, opts.Body, opts.CommitID))
	t.LastReviewOpts = opts
	return gitealike.ReviewDTO{
		ID:        980,
		User:      gitealike.UserDTO{UserName: "mutation-bot"},
		State:     opts.State,
		Body:      opts.Body,
		Submitted: time.Now().UTC().Truncate(time.Second),
	}, nil
}

func (t *ApiTestGitealikeTransport) DeleteIssueComment(
	_ context.Context,
	_ platform.RepoRef,
	commentID int64,
) error {
	t.MutationCalls = append(t.MutationCalls, fmt.Sprintf("delete_comment:%d", commentID))
	t.PullComments = slices.DeleteFunc(t.PullComments, func(comment gitealike.CommentDTO) bool {
		return comment.ID == commentID
	})
	t.IssueComments = slices.DeleteFunc(t.IssueComments, func(comment gitealike.CommentDTO) bool {
		return comment.ID == commentID
	})
	return nil
}

func (t *ApiTestGitealikeTransport) EditIssue(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	opts gitealike.IssueMutationOptions,
) (gitealike.IssueDTO, error) {
	issue := t.FindIssue(number)
	if issue == nil {
		return gitealike.IssueDTO{}, platform.ErrNotFound
	}
	if opts.State != nil {
		issue.State = *opts.State
		t.MutationCalls = append(t.MutationCalls, fmt.Sprintf("edit_issue:%d:%s", number, *opts.State))
	}
	if opts.Title != nil {
		issue.Title = *opts.Title
	}
	if opts.Body != nil {
		issue.Body = *opts.Body
	}
	issue.Updated = time.Now().UTC().Truncate(time.Second)
	if issue.State == "closed" {
		closed := issue.Updated
		issue.Closed = &closed
	}
	return *issue, nil
}

func (t *ApiTestGitealikeTransport) EditIssueComment(
	_ context.Context,
	_ platform.RepoRef,
	commentID int64,
	body string,
) (gitealike.CommentDTO, error) {
	t.MutationCalls = append(t.MutationCalls, fmt.Sprintf("edit_comment:%d:%s", commentID, body))
	comment := gitealike.CommentDTO{
		ID:      commentID,
		User:    gitealike.UserDTO{UserName: "mutation-bot"},
		Body:    body,
		Created: time.Now().UTC().Truncate(time.Second),
		Updated: time.Now().UTC().Truncate(time.Second),
	}
	t.PullComments = UpsertComment(t.PullComments, comment)
	t.IssueComments = UpsertComment(t.IssueComments, comment)
	return comment, nil
}

func (t *ApiTestGitealikeTransport) EditPullRequest(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	opts gitealike.PullRequestMutationOptions,
) (gitealike.PullRequestDTO, error) {
	pr := t.FindPull(number)
	if pr == nil {
		return gitealike.PullRequestDTO{}, platform.ErrNotFound
	}
	if opts.State != nil {
		pr.State = *opts.State
		t.MutationCalls = append(t.MutationCalls, fmt.Sprintf("edit_pull:%d:%s", number, *opts.State))
	}
	if opts.Title != nil {
		pr.Title = *opts.Title
	}
	if opts.Body != nil {
		pr.Body = *opts.Body
	}
	if opts.Title != nil || opts.Body != nil {
		t.MutationCalls = append(t.MutationCalls, fmt.Sprintf("edit_pull_content:%d:%s:%s", number, pr.Title, pr.Body))
	}
	pr.Updated = time.Now().UTC().Truncate(time.Second)
	if pr.State == "closed" {
		closed := pr.Updated
		pr.Closed = &closed
	}
	return *pr, nil
}

func (t *ApiTestGitealikeTransport) GetIssue(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (gitealike.IssueDTO, error) {
	for _, issue := range t.Issues {
		if issue.Index == number {
			return issue, nil
		}
	}
	return gitealike.IssueDTO{}, platform.ErrNotFound
}

func (t *ApiTestGitealikeTransport) GetPullRequest(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (gitealike.PullRequestDTO, error) {
	for _, pr := range t.Pulls {
		if pr.Index == number {
			t.HeadCalls++
			if len(t.HeadOverrides) > 0 {
				i := t.HeadCalls - 1
				if i >= len(t.HeadOverrides) {
					i = len(t.HeadOverrides) - 1
				}
				pr.Head.SHA = t.HeadOverrides[i]
			}
			return pr, nil
		}
	}
	return gitealike.PullRequestDTO{}, platform.ErrNotFound
}

func (t *ApiTestGitealikeTransport) GetRepository(
	context.Context,
	string,
	string,
) (gitealike.RepositoryDTO, error) {
	return t.Repo, nil
}

func (t *ApiTestGitealikeTransport) ListActionRuns(
	context.Context,
	platform.RepoRef,
	string,
	gitealike.PageOptions,
) ([]gitealike.ActionRunDTO, gitealike.Page, error) {
	return t.ActionRuns, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) ListIssueComments(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	return t.IssueComments, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) ListOpenIssues(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.IssueDTO, gitealike.Page, error) {
	return t.Issues, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) ListOpenPullRequests(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.PullRequestDTO, gitealike.Page, error) {
	return t.Pulls, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) ListOrgRepositories(
	context.Context,
	string,
	gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	return []gitealike.RepositoryDTO{t.Repo}, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) ListPullRequestComments(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	return t.PullComments, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) ListPullRequestCommits(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommitDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) ListPullRequestReviews(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.ReviewDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) ListReleases(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.ReleaseDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) ListStatuses(
	context.Context,
	platform.RepoRef,
	string,
	gitealike.PageOptions,
) ([]gitealike.StatusDTO, gitealike.Page, error) {
	return t.Statuses, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) ListTags(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.TagDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) ListUserRepositories(
	context.Context,
	string,
	gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	return []gitealike.RepositoryDTO{t.Repo}, gitealike.Page{}, nil
}

func (t *ApiTestGitealikeTransport) MergePullRequest(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	opts gitealike.MergeOptions,
) (gitealike.MergeResultDTO, error) {
	pr := t.FindPull(number)
	if pr == nil {
		return gitealike.MergeResultDTO{}, platform.ErrNotFound
	}
	t.MutationCalls = append(t.MutationCalls, fmt.Sprintf("merge:%d:%s", number, opts.Method))
	t.MergeHeadPins = append(t.MergeHeadPins, opts.ExpectedHeadSHA)
	t.LastMergeOpts = opts
	if t.MergeErr != nil {
		return gitealike.MergeResultDTO{}, t.MergeErr
	}
	now := time.Now().UTC().Truncate(time.Second)
	pr.State = "merged"
	pr.Merged = true
	pr.MergedAt = &now
	pr.Updated = now
	return gitealike.MergeResultDTO{Merged: true, SHA: "merged-sha", Message: "merged"}, nil
}

func (t *ApiTestGitealikeTransport) FindIssue(number int) *gitealike.IssueDTO {
	for i := range t.Issues {
		if t.Issues[i].Index == number {
			return &t.Issues[i]
		}
	}
	return nil
}

func (t *ApiTestGitealikeTransport) FindPull(number int) *gitealike.PullRequestDTO {
	for i := range t.Pulls {
		if t.Pulls[i].Index == number {
			return &t.Pulls[i]
		}
	}
	return nil
}

type ApiTestGitealikeTransport struct {
	Repo           gitealike.RepositoryDTO
	Pulls          []gitealike.PullRequestDTO
	PullComments   []gitealike.CommentDTO
	Issues         []gitealike.IssueDTO
	IssueComments  []gitealike.CommentDTO
	Statuses       []gitealike.StatusDTO
	MergeErr       error
	ActionRuns     []gitealike.ActionRunDTO
	NextCommentID  int64
	NextIssueID    int64
	NextIssueIndex int
	MutationCalls  []string
	MergeHeadPins  []string
	LastReviewOpts gitealike.ReviewOptions

	LastMergeOpts gitealike.MergeOptions
	// headOverrides, when non-empty, replaces the head SHA returned by
	// successive GetPullRequest calls (the final entry repeats),
	// simulating a push racing an in-flight mutation.
	HeadOverrides []string
	HeadCalls     int
}

func AssertRFC3339UTC(t *testing.T, got string, want time.Time) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, got)
	require.NoError(t, err)
	assert.Equal(t, want.UTC(), parsed.UTC())
	assert.True(t, strings.HasSuffix(got, "Z"), "expected UTC RFC3339 with trailing Z: %s", got)
}

func BindLoopback8091() config.HostKey {
	return config.HostKey{Host: "127.0.0.1", Port: "8091"}
}

func BlockServerRuntimeHelper() {
	for {
		time.Sleep(time.Hour)
	}
}

func CleanupContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func CleanupServerTestTmux(owner *testtmux.Owner) error {
	if owner == nil {
		return nil
	}
	return owner.Cleanup()
}

func CreateRuntimeTestProject(t *testing.T, database *db.DB, localPath string) *db.Project {
	t.Helper()
	project, err := database.CreateProject(context.Background(), db.CreateProjectInput{
		DisplayName: "runtime-project",
		LocalPath:   localPath,
	})
	require.NoError(t, err)
	return project
}

func DecodeProblem(t *testing.T, response *http.Response) httpapi.ProblemError {
	t.Helper()
	var problem httpapi.ProblemError
	require.NoError(t, json.UnmarshalRead(response.Body, &problem, V1JSON))
	return problem
}

func DecodeZstdBody(t *testing.T, body io.Reader) string {
	t.Helper()
	reader, err := zstd.NewReader(body)
	require.NoError(t, err)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	return string(data)
}

const DefaultTestConfigContent = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`

var DefaultTestRepos = []ghclient.RepoRef{
	{
		Platform:           "github",
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "repo-acme-widget",
		CloneURL:           "https://github.com/acme/widget.git",
	},
}

func DirectDaemonRequest(t *testing.T, bearer string, headers http.Header) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/snapshot", nil)
	req.Host = "127.0.0.1:8091"
	req.RemoteAddr = "127.0.0.1:1234"
	if headers != nil {
		req.Header = headers.Clone()
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

func EmptyFrontend() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte("<!DOCTYPE html><html><body>ok</body></html>"),
		},
	}
}

func (f *FakeTelemetry) Capture(event string, properties map[string]any) error {
	f.Event = event
	f.Properties = properties
	return nil
}

func (f *FakeTelemetry) Close() error { return nil }

func (f *FakeTelemetry) Enabled() bool { return f.EnabledValue }

type FakeTelemetry struct {
	EnabledValue bool
	Event        string
	Properties   map[string]any
}

const FederationEventTestNodeID = "55555555555555555555555555555555"

func GracefulShutdown(t *testing.T, srv interface{ Shutdown(context.Context) error }) {
	t.Helper()
	ctx, cancel := CleanupContext(t)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
}

func HttpDo(t *testing.T, ts *httptest.Server, method, path string, body []byte) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, ts.URL+path, bodyReader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	} else if method == http.MethodPost || method == http.MethodDelete ||
		method == http.MethodPut || method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	return resp
}

func IsServerHelperProcess() bool {
	if os.Getenv("KENN_FORGE_SERVER_RUNTIME_HELPER") == "1" ||
		os.Getenv("KENN_FORGE_SERVER_PTY_OWNER_HELPER") == "1" {
		return true
	}
	args := os.Args
	if sep := slices.Index(args, "--"); sep >= 0 {
		args = args[sep+1:]
	}
	return len(args) > 0 &&
		(args[0] == ServerRuntimeHelperMarker ||
			args[0] == ServerPtyOwnerParentHelperMarker ||
			args[0] == "pty-owner")
}

// issueMutatorGitLabProvider embeds apiTestGitLabProvider but advertises
// issue_mutation capability and returns the supplied error from
// CreateIssue. Used by TestAPIRateLimitedEnvelope.
type IssueMutatorGitLabProvider struct {
	ApiTestGitLabProvider
	ProviderErr error
}

func (p *IssueMutatorGitLabProvider) Capabilities() platform.Capabilities {
	caps := p.ApiTestGitLabProvider.Capabilities()
	caps.IssueMutation = true
	caps.StateMutation = true
	return caps
}

func (p *IssueMutatorGitLabProvider) CreateIssue(
	_ context.Context,
	_ platform.RepoRef,
	_, _ string,
) (platform.Issue, error) {
	return platform.Issue{}, p.ProviderErr
}

func (p *IssueMutatorGitLabProvider) EditMergeRequestContent(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
	_, _ *string,
) (platform.MergeRequest, error) {
	return platform.MergeRequest{}, p.ProviderErr
}

func Make422Error() error {
	return &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusUnprocessableEntity},
		Message:  "Validation Failed",
	}
}

func MarkArchiveItemRemovedUpstreamForServerTest(
	t *testing.T,
	database *db.DB,
	repoID int64,
	itemType db.ArchiveItemType,
	number int,
) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	_, err := database.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, ?, ?, ?, ?, ?, 'removed_upstream')`,
		repoID, itemType, number, fmt.Sprintf("%s-%d", itemType, number), now, now,
	)
	require.NoError(t, err)
}

// InvalidateListETagsForRepo is a no-op for the server test mock,
// which has no underlying HTTP cache.
func (m *MockGH) InvalidateListETagsForRepo(_, _ string, _ ...string) {}

func (m *MockGH) ApplyReviewSuggestions(
	ctx context.Context,
	owner string,
	repo string,
	number int,
	input platform.ApplyReviewSuggestionsInput,
) (*platform.AppliedReviewSuggestions, error) {
	if m.ApplyReviewSuggestionsFn != nil {
		return m.ApplyReviewSuggestionsFn(ctx, owner, repo, number, input)
	}
	return &platform.AppliedReviewSuggestions{CommitSHA: "suggestion-commit-sha"}, nil
}

func (m *MockGH) ApproveWorkflowRun(
	ctx context.Context, owner, repo string, runID int64,
) error {
	if m.ApproveWorkflowRunFn != nil {
		return m.ApproveWorkflowRunFn(ctx, owner, repo, runID)
	}
	return nil
}

func (m *MockGH) AuthenticatedViewerLogin(ctx context.Context) (string, error) {
	m.AuthenticatedViewerCalls++
	if m.AuthenticatedViewerLoginFn != nil {
		return m.AuthenticatedViewerLoginFn(ctx)
	}
	return "", nil
}

func (m *MockGH) ConvertPullRequestToDraft(
	ctx context.Context, owner, repo string, number int,
) (*gh.PullRequest, error) {
	if m.ConvertToDraftFn != nil {
		return m.ConvertToDraftFn(ctx, owner, repo, number)
	}
	draft := true
	state := "open"
	return &gh.PullRequest{Number: &number, State: &state, Draft: &draft}, nil
}

func (m *MockGH) CreateIssue(
	ctx context.Context, owner, repo, title, body string,
) (*gh.Issue, error) {
	if m.CreateIssueFn != nil {
		return m.CreateIssueFn(ctx, owner, repo, title, body)
	}
	number := 1
	now := gh.Timestamp{Time: time.Now().UTC()}
	state := "open"
	htmlURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, number)
	login := "fixture-bot"
	return &gh.Issue{
		Number:    &number,
		Title:     &title,
		Body:      &body,
		State:     &state,
		HTMLURL:   &htmlURL,
		User:      &gh.User{Login: &login},
		CreatedAt: &now,
		UpdatedAt: &now,
	}, nil
}

func (m *MockGH) CreateIssueComment(
	ctx context.Context, owner, repo string, number int, body string,
) (*gh.IssueComment, error) {
	if m.CreateIssueCommentFn != nil {
		return m.CreateIssueCommentFn(ctx, owner, repo, number, body)
	}
	id := int64(42)
	return &gh.IssueComment{
		ID:   &id,
		Body: &body,
	}, nil
}

func (m *MockGH) CreatePullRequestReviewCommentReply(
	ctx context.Context, owner, repo string, number int, body string, commentID int64,
) (*gh.PullRequestComment, error) {
	if m.CreateReviewCommentReplyFn != nil {
		return m.CreateReviewCommentReplyFn(ctx, owner, repo, number, body, commentID)
	}
	id := commentID + 1
	login := "fixture-bot"
	now := gh.Timestamp{Time: time.Now().UTC()}
	return &gh.PullRequestComment{
		ID:        &id,
		Body:      &body,
		User:      &gh.User{Login: &login},
		CreatedAt: &now,
	}, nil
}

func (m *MockGH) CreateReview(
	ctx context.Context, owner, repo string, number int, event string, body string,
) (*gh.PullRequestReview, error) {
	if m.CreateReviewFn != nil {
		return m.CreateReviewFn(ctx, owner, repo, number, event, body)
	}
	id := int64(99)
	state := "APPROVED"
	return &gh.PullRequestReview{ID: &id, State: &state}, nil
}

func (m *MockGH) CreateReviewWithComments(
	ctx context.Context,
	owner, repo string,
	number int,
	event string,
	body string,
	commitID string,
	comments []*gh.DraftReviewComment,
) (*gh.PullRequestReview, error) {
	if m.CreateReviewWithCommentsFn != nil {
		return m.CreateReviewWithCommentsFn(ctx, owner, repo, number, event, body, commitID, comments)
	}
	return m.CreateReview(ctx, owner, repo, number, event, body)
}

func (m *MockGH) DeleteIssueComment(
	ctx context.Context, owner, repo string, commentID int64,
) error {
	if m.DeleteIssueCommentFn != nil {
		return m.DeleteIssueCommentFn(ctx, owner, repo, commentID)
	}
	return nil
}

func (m *MockGH) DismissReview(
	ctx context.Context, owner, repo string, number int, reviewID int64, message string,
) (*gh.PullRequestReview, error) {
	if m.DismissReviewFn != nil {
		return m.DismissReviewFn(ctx, owner, repo, number, reviewID, message)
	}
	return &gh.PullRequestReview{ID: &reviewID}, nil
}

func (m *MockGH) EditIssue(
	ctx context.Context, owner, repo string, number int, state string,
) (*gh.Issue, error) {
	if m.EditIssueFn != nil {
		return m.EditIssueFn(ctx, owner, repo, number, state)
	}
	return &gh.Issue{State: &state}, nil
}

func (m *MockGH) EditIssueComment(
	ctx context.Context, owner, repo string, commentID int64, body string,
) (*gh.IssueComment, error) {
	if m.EditIssueCommentFn != nil {
		return m.EditIssueCommentFn(ctx, owner, repo, commentID, body)
	}
	login := "fixture-bot"
	now := gh.Timestamp{Time: time.Now().UTC()}
	return &gh.IssueComment{
		ID:        &commentID,
		Body:      &body,
		User:      &gh.User{Login: &login},
		CreatedAt: &now,
		UpdatedAt: &now,
	}, nil
}

func (m *MockGH) EditIssueContent(
	ctx context.Context, owner, repo string, number int, title *string, body *string,
) (*gh.Issue, error) {
	if m.EditIssueContentFn != nil {
		return m.EditIssueContentFn(ctx, owner, repo, number, title, body)
	}
	out := &gh.Issue{}
	if title != nil {
		out.Title = title
	}
	if body != nil {
		out.Body = body
	}
	return out, nil
}

func (m *MockGH) EditPullRequest(
	ctx context.Context, owner, repo string, number int, opts platformgithub.EditPullRequestOpts,
) (*gh.PullRequest, error) {
	if m.EditPullRequestFn != nil {
		return m.EditPullRequestFn(ctx, owner, repo, number, opts)
	}
	pr := &gh.PullRequest{}
	if opts.State != nil {
		pr.State = opts.State
	}
	if opts.Title != nil {
		pr.Title = opts.Title
	}
	if opts.Body != nil {
		pr.Body = opts.Body
	}
	now := time.Now().UTC()
	ghTime := gh.Timestamp{Time: now}
	pr.UpdatedAt = &ghTime
	return pr, nil
}

func (m *MockGH) GetCombinedStatus(
	ctx context.Context, owner, repo, ref string,
) (*gh.CombinedStatus, error) {
	if m.GetCombinedStatusFn != nil {
		return m.GetCombinedStatusFn(ctx, owner, repo, ref)
	}
	return nil, nil
}

func (m *MockGH) GetIssue(ctx context.Context, owner, repo string, number int) (*gh.Issue, error) {
	if m.GetIssueFn != nil {
		return m.GetIssueFn(ctx, owner, repo, number)
	}
	return nil, nil
}

func (m *MockGH) GetIssueIfChanged(
	ctx context.Context,
	owner, repo string,
	number int,
	etag string,
) (*gh.Issue, string, bool, error) {
	if m.GetIssueIfChangedFn != nil {
		return m.GetIssueIfChangedFn(ctx, owner, repo, number, etag)
	}
	issue, err := m.GetIssue(ctx, owner, repo, number)
	return issue, "", false, err
}

func (m *MockGH) GetMarkdownImage(
	ctx context.Context,
	owner, repo, sourceURL string,
) (platform.MarkdownImage, error) {
	if m.GetMarkdownImageFn != nil {
		return m.GetMarkdownImageFn(ctx, owner, repo, sourceURL)
	}
	return platform.MarkdownImage{}, nil
}

func (m *MockGH) GetPullRequest(ctx context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
	if m.GetPullRequestFn != nil {
		return m.GetPullRequestFn(ctx, owner, repo, number)
	}
	return nil, nil
}

func (m *MockGH) GetPullRequestIfChanged(
	ctx context.Context,
	owner, repo string,
	number int,
	etag string,
) (*gh.PullRequest, string, bool, error) {
	if m.GetPullRequestIfChangedFn != nil {
		return m.GetPullRequestIfChangedFn(ctx, owner, repo, number, etag)
	}
	pr, err := m.GetPullRequest(ctx, owner, repo, number)
	return pr, "", false, err
}

func (m *MockGH) GetRateLimitSnapshot(ctx context.Context) (*platformgithub.RateLimitSnapshot, error) {
	m.RateLimitSnapshotCalls++
	if m.RateLimitSnapshotFn != nil {
		return m.RateLimitSnapshotFn(ctx)
	}
	return nil, nil
}

func (m *MockGH) GetRepository(
	ctx context.Context, owner, repo string,
) (*gh.Repository, error) {
	if m.GetRepositoryFn != nil {
		return m.GetRepositoryFn(ctx, owner, repo)
	}
	nodeID := "repo-" + owner + "-" + repo
	return &gh.Repository{
		Name:     &repo,
		NodeID:   &nodeID,
		Owner:    &gh.User{Login: &owner},
		Archived: new(false),
	}, nil
}

func (m *MockGH) GetUser(ctx context.Context, login string) (*gh.User, error) {
	if m.GetUserFn != nil {
		return m.GetUserFn(ctx, login)
	}
	return &gh.User{Login: &login}, nil
}

func (m *MockGH) ListCheckRunsForRef(
	ctx context.Context, owner, repo, ref string,
) ([]*gh.CheckRun, error) {
	if m.ListCheckRunsForRefFn != nil {
		return m.ListCheckRunsForRefFn(ctx, owner, repo, ref)
	}
	return nil, nil
}

func (m *MockGH) ListCommits(
	_ context.Context, _, _ string, _ int,
) ([]*gh.RepositoryCommit, error) {
	return nil, nil
}

func (m *MockGH) ListForcePushEvents(
	_ context.Context, _, _ string, _ int,
) ([]platformgithub.ForcePushEvent, error) {
	return nil, nil
}

func (m *MockGH) ListIssueComments(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	if m.ListIssueCommentsFn != nil {
		return m.ListIssueCommentsFn(ctx, owner, repo, number)
	}
	if m.ListIssueCommentsErr != nil {
		return nil, m.ListIssueCommentsErr
	}
	return nil, nil
}

func (m *MockGH) ListIssueCommentsIfChanged(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	if m.ListIssueCommentsFn == nil && m.ListIssueCommentsErr == nil {
		return nil, &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusNotModified},
		}
	}
	return m.ListIssueComments(ctx, owner, repo, number)
}

func (m *MockGH) ListIssuesPage(
	ctx context.Context, owner, repo, state string, page int,
) ([]*gh.Issue, bool, error) {
	if m.ListIssuesPageFn != nil {
		return m.ListIssuesPageFn(ctx, owner, repo, state, page)
	}
	return nil, false, nil
}

func (m *MockGH) ListNativeStacksPage(
	ctx context.Context, owner, repo string, page int,
) (platformgithub.NativeStackPage, error) {
	if m.NativeStackAPI != nil && m.NativeStackAPI.ListStackPage != nil {
		return m.NativeStackAPI.ListStackPage(ctx, owner, repo, page)
	}
	return platformgithub.NativeStackPage{}, nil
}

func (m *MockGH) ListNotifications(ctx context.Context, opts ghclient.NotificationListOptions) ([]ghclient.NotificationThread, bool, error) {
	if m.ListNotificationsFn != nil {
		return m.ListNotificationsFn(ctx, opts)
	}
	return nil, false, nil
}

func (m *MockGH) ListOpenIssues(ctx context.Context, owner, repo string) ([]*gh.Issue, error) {
	if m.ListOpenIssuesFn != nil {
		return m.ListOpenIssuesFn(ctx, owner, repo)
	}
	return nil, nil
}

func (m *MockGH) ListOpenPullRequests(ctx context.Context, owner, repo string) ([]*gh.PullRequest, error) {
	if m.ListOpenPullRequestsFn != nil {
		return m.ListOpenPullRequestsFn(ctx, owner, repo)
	}
	if m.ListOpenPRsErr != nil {
		return nil, m.ListOpenPRsErr
	}
	return nil, nil
}

func (m *MockGH) ListOpenPullRequestsWithNativeStackHints(
	ctx context.Context, owner, repo string,
) ([]*gh.PullRequest, map[int]*platformgithub.NativeStackHint, error) {
	if m.NativeStackAPI != nil && m.NativeStackAPI.ListOpenPullRequests != nil {
		return m.NativeStackAPI.ListOpenPullRequests(ctx, owner, repo)
	}
	prs, err := m.ListOpenPullRequests(ctx, owner, repo)
	return prs, nil, err
}

func (m *MockGH) ListPullRequestReviewThreads(
	ctx context.Context,
	owner string,
	repo string,
	number int,
) ([]platformgithub.PullRequestReviewThread, error) {
	if m.ListReviewThreadsFn != nil {
		return m.ListReviewThreadsFn(ctx, owner, repo, number)
	}
	return nil, nil
}

func (m *MockGH) ListPullRequestTimelineEvents(
	ctx context.Context, owner, repo string, number int,
) ([]platformgithub.PullRequestTimelineEvent, error) {
	if m.ListPRTimelineEventsFn != nil {
		return m.ListPRTimelineEventsFn(ctx, owner, repo, number)
	}
	return nil, nil
}

func (m *MockGH) ListPullRequestsPage(
	ctx context.Context, owner, repo, state string, page int,
) ([]*gh.PullRequest, bool, error) {
	if m.ListPullRequestsPageFn != nil {
		return m.ListPullRequestsPageFn(ctx, owner, repo, state, page)
	}
	return nil, false, nil
}

func (m *MockGH) ListReleases(
	ctx context.Context, owner, repo string, perPage int,
) ([]*gh.RepositoryRelease, error) {
	if m.ListReleasesFn != nil {
		return m.ListReleasesFn(ctx, owner, repo, perPage)
	}
	return nil, nil
}

func (m *MockGH) ListRepositoriesByOwner(
	ctx context.Context, owner string,
) ([]*gh.Repository, error) {
	if m.ListReposByOwnerFn != nil {
		return m.ListReposByOwnerFn(ctx, owner)
	}
	return nil, nil
}

func (m *MockGH) ListReviews(
	_ context.Context, _, _ string, _ int,
) ([]*gh.PullRequestReview, error) {
	return nil, nil
}

func (m *MockGH) ListTags(
	ctx context.Context, owner, repo string, perPage int,
) ([]*gh.RepositoryTag, error) {
	if m.ListTagsFn != nil {
		return m.ListTagsFn(ctx, owner, repo, perPage)
	}
	return nil, nil
}

func (m *MockGH) ListWorkflowRunsForHeadSHA(
	ctx context.Context, owner, repo, headSHA string,
) ([]*gh.WorkflowRun, error) {
	if m.ListWorkflowRunsForHeadFn != nil {
		return m.ListWorkflowRunsForHeadFn(ctx, owner, repo, headSHA)
	}
	return nil, nil
}

func (m *MockGH) MarkNotificationThreadRead(ctx context.Context, threadID string) error {
	if m.MarkNotificationReadFn != nil {
		return m.MarkNotificationReadFn(ctx, threadID)
	}
	return nil
}

func (m *MockGH) MarkPullRequestReadyForReview(
	ctx context.Context, owner, repo string, number int,
) (*gh.PullRequest, error) {
	if m.MarkReadyForReviewFn != nil {
		return m.MarkReadyForReviewFn(ctx, owner, repo, number)
	}
	draft := false
	return &gh.PullRequest{Number: &number, Draft: &draft}, nil
}

func (m *MockGH) MergePullRequest(
	ctx context.Context, owner, repo string, number int,
	commitTitle, commitMessage, method, _ string,
) (*gh.PullRequestMergeResult, error) {
	if m.MergePullRequestFn != nil {
		return m.MergePullRequestFn(ctx, owner, repo, number, commitTitle, commitMessage, method)
	}
	merged := true
	sha := "abc123"
	msg := "merged"
	return &gh.PullRequestMergeResult{
		Merged: &merged, SHA: &sha, Message: &msg,
	}, nil
}

type MockGH struct {
	GetRepositoryFn            func(context.Context, string, string) (*gh.Repository, error)
	GetPullRequestFn           func(context.Context, string, string, int) (*gh.PullRequest, error)
	GetPullRequestIfChangedFn  func(context.Context, string, string, int, string) (*gh.PullRequest, string, bool, error)
	GetIssueFn                 func(context.Context, string, string, int) (*gh.Issue, error)
	GetIssueIfChangedFn        func(context.Context, string, string, int, string) (*gh.Issue, string, bool, error)
	CreateIssueFn              func(context.Context, string, string, string, string) (*gh.Issue, error)
	GetUserFn                  func(context.Context, string) (*gh.User, error)
	AuthenticatedViewerLoginFn func(context.Context) (string, error)
	AuthenticatedViewerCalls   int
	MarkReadyForReviewFn       func(context.Context, string, string, int) (*gh.PullRequest, error)
	ConvertToDraftFn           func(context.Context, string, string, int) (*gh.PullRequest, error)
	DismissReviewFn            func(context.Context, string, string, int, int64, string) (*gh.PullRequestReview, error)
	EditPullRequestFn          func(context.Context, string, string, int, platformgithub.EditPullRequestOpts) (*gh.PullRequest, error)
	EditIssueFn                func(context.Context, string, string, int, string) (*gh.Issue, error)
	EditIssueContentFn         func(context.Context, string, string, int, *string, *string) (*gh.Issue, error)
	CreateIssueCommentFn       func(context.Context, string, string, int, string) (*gh.IssueComment, error)
	EditIssueCommentFn         func(context.Context, string, string, int64, string) (*gh.IssueComment, error)
	DeleteIssueCommentFn       func(context.Context, string, string, int64) error
	CreateReviewCommentReplyFn func(context.Context, string, string, int, string, int64) (*gh.PullRequestComment, error)
	CreateReviewFn             func(context.Context, string, string, int, string, string) (*gh.PullRequestReview, error)
	CreateReviewWithCommentsFn func(context.Context, string, string, int, string, string, string, []*gh.DraftReviewComment) (*gh.PullRequestReview, error)
	ApplyReviewSuggestionsFn   func(context.Context, string, string, int, platform.ApplyReviewSuggestionsInput) (*platform.AppliedReviewSuggestions, error)
	MergePullRequestFn         func(context.Context, string, string, int, string, string, string) (*gh.PullRequestMergeResult, error)
	ListWorkflowRunsForHeadFn  func(context.Context, string, string, string) ([]*gh.WorkflowRun, error)
	ApproveWorkflowRunFn       func(context.Context, string, string, int64) error
	ListReposByOwnerFn         func(context.Context, string) ([]*gh.Repository, error)
	ListReleasesFn             func(context.Context, string, string, int) ([]*gh.RepositoryRelease, error)
	ListTagsFn                 func(context.Context, string, string, int) ([]*gh.RepositoryTag, error)
	ListOpenPullRequestsFn     func(context.Context, string, string) ([]*gh.PullRequest, error)
	NativeStackAPI             *MockGHNativeStackAPI
	ListPullRequestsPageFn     func(context.Context, string, string, string, int) ([]*gh.PullRequest, bool, error)
	ListIssuesPageFn           func(context.Context, string, string, string, int) ([]*gh.Issue, bool, error)
	ListCheckRunsForRefFn      func(context.Context, string, string, string) ([]*gh.CheckRun, error)
	GetCombinedStatusFn        func(context.Context, string, string, string) (*gh.CombinedStatus, error)
	ListPRTimelineEventsFn     func(context.Context, string, string, int) ([]platformgithub.PullRequestTimelineEvent, error)
	ListOpenPRsErr             error
	ListOpenIssuesFn           func(context.Context, string, string) ([]*gh.Issue, error)
	ListIssueCommentsFn        func(context.Context, string, string, int) ([]*gh.IssueComment, error)
	ListReviewThreadsFn        func(context.Context, string, string, int) ([]platformgithub.PullRequestReviewThread, error)
	RateLimitSnapshotFn        func(context.Context) (*platformgithub.RateLimitSnapshot, error)
	RateLimitSnapshotCalls     int
	ListIssueCommentsErr       error
	ListNotificationsFn        func(context.Context, ghclient.NotificationListOptions) ([]ghclient.NotificationThread, bool, error)
	MarkNotificationReadFn     func(context.Context, string) error
	GetMarkdownImageFn         func(context.Context, string, string, string) (platform.MarkdownImage, error)
}

// mockGH implements ghclient.Client for testing.
type MockGHNativeStackAPI struct {
	ListOpenPullRequests func(
		context.Context, string, string,
	) ([]*gh.PullRequest, map[int]*platformgithub.NativeStackHint, error)
	ListStackPage func(
		context.Context, string, string, int,
	) (platformgithub.NativeStackPage, error)
}

func MustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v, V1JSON)
	require.NoError(t, err)
	return out
}

func NormalizeRecordedTmuxArg(arg string) string {
	if runtime.GOOS != "windows" {
		return arg
	}
	switch arg {
	case "#session_name":
		return "#{session_name}"
	case "#pane_title":
		return "#{pane_title}"
	default:
		return arg
	}
}

func OpenTestDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

var ParallelServerTestSlots = semaphore.NewWeighted(4)

const (
	PreparationHubNodeID    = "0123456789abcdef0123456789abcdef"
	PreparationLocalNodeID  = "fedcba9876543210fedcba9876543210"
	PreparationEnrollmentID = "11111111111111111111111111111111"
)

var PrivateTmuxOwner *testtmux.Owner

// providerStatePR builds the provider's view of a seeded test PR after a
// state mutation, the way the real provider returns it from an edit or a
// post-merge fetch. The handler commits this snapshot through the canonical
// parent-snapshot path, so updatedAt must be newer than the seeded row's or
// the monotonic guard rejects it; merged/closed timestamps come from here,
// not from any local clock.
func ProviderStatePR(
	number int,
	state string,
	updatedAt time.Time,
	closedAt, mergedAt *time.Time,
	headSHA string,
) *gh.PullRequest {
	toTimestamp := func(value *time.Time) *gh.Timestamp {
		if value == nil {
			return nil
		}
		return &gh.Timestamp{Time: *value}
	}
	numberText := strconv.Itoa(number)
	pr := &gh.PullRequest{
		ID:        new(int64(number) * 1000),
		Number:    &number,
		State:     &state,
		Title:     new("Test PR #" + numberText),
		HTMLURL:   new("https://github.com/acme/widget/pull/" + numberText),
		User:      &gh.User{Login: new("testuser")},
		CreatedAt: &gh.Timestamp{Time: updatedAt.Add(-2 * time.Hour)},
		UpdatedAt: &gh.Timestamp{Time: updatedAt},
		ClosedAt:  toTimestamp(closedAt),
		MergedAt:  toTimestamp(mergedAt),
		Head: &gh.PullRequestBranch{
			Ref: new("feature"), SHA: &headSHA,
			Repo: &gh.Repository{ID: new(int64(1)), FullName: new("acme/widget")},
		},
		Base: &gh.PullRequestBranch{
			Ref: new("main"), SHA: new("base-sha"),
			Repo: &gh.Repository{ID: new(int64(1)), FullName: new("acme/widget")},
		},
	}
	if mergedAt != nil {
		pr.Merged = new(true)
		pr.MergedBy = &gh.User{Login: new("merger")}
	}
	return pr
}

type RawProblemDetail struct {
	Type    string         `json:"type"`
	Title   string         `json:"title"`
	Status  int            `json:"status"`
	Detail  string         `json:"detail"`
	Code    string         `json:"code"`
	Details map[string]any `json:"details"`
	Errors  []struct {
		Message  string `json:"message"`
		Location string `json:"location"`
		Value    any    `json:"value"`
	} `json:"errors"`
}

func ReadTmuxRecord(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	// Split on NUL. Each record is "<argc>\0<arg0>\0<arg1>\0...\0",
	// so a flushed stream always ends with a trailing \0 and Split
	// produces a final empty element after it. Strip exactly one
	// trailing empty so we don't mistake it for part of the next
	// record. Interior empty elements are real args (the NUL framing
	// exists to preserve them) and must NOT be skipped.
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	var out [][]string
	for i := 0; i < len(parts); {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			// Trailing record is mid-write: argc isn't a valid
			// integer yet. Stop; the next poll will see the full
			// record once the recorder script flushes.
			break
		}
		if i+1+n > len(parts) {
			// argc is parsed but not all args are on disk yet.
			// Same treatment: defer to the next poll.
			break
		}
		i++
		argv := parts[i : i+n]
		for j := range argv {
			argv[j] = NormalizeRecordedTmuxArg(argv[j])
		}
		out = append(out, argv)
		i += n
	}
	return out
}

func RequireMR(t *testing.T, database *db.DB, repoID int64, number int) *db.MergeRequest {
	t.Helper()
	require := require.New(t)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, number)
	require.NoError(err)
	require.NotNil(mr)
	return mr
}

var // Bound Git-heavy root-package tests independently from the workspacetest
// binary.
RootWorkspaceGitSemaphore = semaphore.NewWeighted(2)

func (f RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type RoundTripFunc func(*http.Request) (*http.Response, error)

func RunParallelServerTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.NoError(t, ParallelServerTestSlots.Acquire(t.Context(), 1))
	t.Cleanup(func() { ParallelServerTestSlots.Release(1) })
}

func RunStackDetection(t *testing.T, database *db.DB, owner, name string) {
	t.Helper()
	ctx := t.Context()
	repo, err := database.GetRepoByIdentity(ctx, VerifiedGitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)
	require.NotNil(t, repo)
	require.NoError(t, stacks.RunDetection(ctx, database, repo.ID))
}

// seedIssue inserts a repo and an issue into the DB.
func SeedIssue(t *testing.T, database *db.DB, owner, name string, number int, state string) int64 {
	t.Helper()
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, VerifiedGitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)
	SeedRepoLaunchMetadata(t, database, repoID)

	now := time.Now().UTC().Truncate(time.Second)
	issue := &db.Issue{
		RepoID: repoID, PlatformID: int64(number) * 1000, Number: number,
		URL:   fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, name, number),
		Title: "Test Issue", Author: "testuser", State: state,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	}
	if state == "closed" {
		issue.ClosedAt = &now
	}
	issueID, err := database.UpsertIssue(ctx, issue)
	require.NoError(t, err)
	return issueID
}

func SeedIssueForRepo(
	t *testing.T, database *db.DB,
	repoID int64, host, owner, name string, number int,
	state, title string,
) int64 {
	t.Helper()
	ctx := t.Context()
	SeedRepoLaunchMetadata(t, database, repoID)

	now := time.Now().UTC().Truncate(time.Second)
	issue := &db.Issue{
		RepoID:         repoID,
		PlatformID:     int64(number) * 1000,
		Number:         number,
		URL:            fmt.Sprintf("https://%s/%s/%s/issues/%d", host, owner, name, number),
		Title:          title,
		Author:         "testuser",
		State:          state,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	if state == "closed" {
		issue.ClosedAt = &now
	}

	issueID, err := database.UpsertIssue(ctx, issue)
	require.NoError(t, err)
	return issueID
}

func SeedIssueOnHost(
	t *testing.T, database *db.DB,
	host, owner, name string, number int,
	state, title string,
) int64 {
	t.Helper()
	ctx := context.Background()

	repoID, err := database.UpsertRepo(ctx, VerifiedGitHubRepoIdentity(host, owner, name))
	require.NoError(t, err)

	return SeedIssueForRepo(t, database, repoID, host, owner, name, number, state, title)
}

// seedPR inserts a repo and a PR into the DB, returning the PR's internal ID.
func SeedPR(t *testing.T, database *db.DB, owner, name string, number int, opts ...SeedPROpt) int64 {
	t.Helper()
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, VerifiedGitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)
	SeedRepoLaunchMetadata(t, database, repoID)

	numberText := strconv.Itoa(number)
	now := time.Now().UTC().Truncate(time.Second)
	pr := &db.MergeRequest{
		RepoID:           repoID,
		PlatformID:       int64(number) * 1000,
		Number:           number,
		URL:              "https://github.com/" + owner + "/" + name + "/pull/" + numberText,
		Title:            "Test PR #" + numberText,
		Author:           "testuser",
		State:            "open",
		IsDraft:          false,
		Body:             "test body",
		HeadBranch:       "feature",
		HeadRepoCloneURL: "https://github.com/" + owner + "/" + name + ".git",
		BaseBranch:       "main",
		Additions:        5,
		Deletions:        2,
		CommentCount:     0,
		ReviewDecision:   "",
		CIStatus:         "",
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	}
	for _, opt := range opts {
		opt(pr)
	}

	prID, err := database.UpsertMergeRequest(ctx, pr)
	require.NoError(t, err)
	if pr.PlatformHeadSHA != "" {
		require.NoError(t, database.UpdateDiffSHAs(
			ctx, repoID, number,
			pr.PlatformHeadSHA, pr.PlatformBaseSHA, "merge-base",
		))
	}
	if len(pr.Labels) > 0 {
		require.NoError(t, database.ReplaceMergeRequestLabels(ctx, repoID, prID, pr.Labels))
	}
	require.NoError(t, database.EnsureKanbanState(ctx, prID))

	return prID
}

func SeedPRForRepo(
	t *testing.T, database *db.DB,
	repoID int64, host, owner, name string, number int,
	opts ...SeedPROpt,
) int64 {
	t.Helper()
	ctx := t.Context()
	SeedRepoLaunchMetadata(t, database, repoID)
	now := time.Now().UTC().Truncate(time.Second)
	pr := &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     int64(number) * 1000,
		Number:         number,
		URL:            fmt.Sprintf("https://%s/%s/%s/pull/%d", host, owner, name, number),
		Title:          fmt.Sprintf("Test PR #%d", number),
		Author:         "testuser",
		State:          "open",
		IsDraft:        false,
		Body:           "test body",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		Additions:      5,
		Deletions:      2,
		CommentCount:   0,
		ReviewDecision: "",
		CIStatus:       "",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	for _, opt := range opts {
		opt(pr)
	}

	prID, err := database.UpsertMergeRequest(ctx, pr)
	require.NoError(t, err)
	require.NoError(t, database.EnsureKanbanState(ctx, prID))

	return prID
}

// seedPROnHost seeds a repo on a specific platform host and
// inserts a PR for it.
func SeedPROnHost(
	t *testing.T, database *db.DB,
	host, owner, name string, number int,
	opts ...SeedPROpt,
) int64 {
	t.Helper()
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, VerifiedGitHubRepoIdentity(host, owner, name))
	require.NoError(t, err)

	return SeedPRForRepo(t, database, repoID, host, owner, name, number, opts...)
}

type SeedPROpt func(*db.MergeRequest)

func SeedRepoLaunchMetadata(t *testing.T, database *db.DB, repoID int64) {
	t.Helper()
	ctx := t.Context()
	repo, err := database.GetRepoByID(ctx, repoID)
	require.NoError(t, err)
	require.NotNil(t, repo)
	require.NotEmpty(t, repo.PlatformRepoID)

	cloneURL := strings.TrimSpace(repo.CloneURL)
	if cloneURL == "" {
		repoPath := strings.Trim(strings.TrimSpace(repo.RepoPath), "/")
		if repoPath == "" {
			repoPath = strings.Trim(repo.Owner, "/") + "/" + strings.Trim(repo.Name, "/")
		}
		cloneURL = "https://" + repo.PlatformHost + "/" + repoPath + ".git"
	}
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	err = database.UpdateRepoProviderMetadata(
		ctx, repoID,
		db.RepoProviderMetadata{
			PlatformRepoID: repo.PlatformRepoID,
			WebURL:         strings.TrimSuffix(cloneURL, ".git"),
			CloneURL:       cloneURL,
			DefaultBranch:  defaultBranch,
		},
	)
	require.NoError(t, err)
}

func SeedServerNotification(t *testing.T, database *db.DB) int64 {
	t.Helper()
	require := require.New(t)
	repoID, err := database.UpsertRepo(t.Context(), VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	number := 42
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "thread-42",
		RepoID:                 &repoID,
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Review requested",
		WebURL:                 "https://github.com/acme/widget/pull/42",
		ItemNumber:             &number,
		ItemType:               "pr",
		ItemAuthor:             "octocat",
		Reason:                 "review_requested",
		Unread:                 true,
		Participating:          true,
		SourceUpdatedAt:        now,
		SyncedAt:               now,
	}}))
	items, err := database.ListNotifications(t.Context(), db.ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)
	return items[0].ID
}

func SeedStackedPR(
	t *testing.T, database *db.DB,
	owner, name string, number int,
	head, base string, state db.MergeRequestState, ci, review string,
) int64 {
	return SeedStackedPRState(t, database, owner, name, number, head, base, state, ci, review, false, "")
}

func SeedStackedPRDraft(
	t *testing.T, database *db.DB,
	owner, name string, number int,
	head, base string, state db.MergeRequestState, ci, review string,
	isDraft bool,
) int64 {
	return SeedStackedPRState(t, database, owner, name, number, head, base, state, ci, review, isDraft, "")
}

func SeedStackedPRState(
	t *testing.T, database *db.DB,
	owner, name string, number int,
	head, base string, state db.MergeRequestState, ci, review string,
	isDraft bool,
	mergeableState string,
) int64 {
	t.Helper()
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, VerifiedGitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)
	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, name)
	require.NoError(t, database.UpdateRepoProviderMetadata(ctx, repoID, db.RepoProviderMetadata{
		CloneURL:      cloneURL,
		DefaultBranch: "main",
	}))
	now := time.Now().UTC().Truncate(time.Second)
	pr := &db.MergeRequest{
		RepoID:           repoID,
		PlatformID:       int64(number) * 1000,
		Number:           number,
		Title:            fmt.Sprintf("PR #%d: %s", number, head),
		Author:           "testuser",
		State:            state,
		IsDraft:          isDraft,
		HeadBranch:       head,
		BaseBranch:       base,
		HeadRepoCloneURL: cloneURL,
		CIStatus:         ci,
		ReviewDecision:   review,
		MergeableState:   mergeableState,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	}
	prID, err := database.UpsertMergeRequest(ctx, pr)
	require.NoError(t, err)
	require.NoError(t, database.EnsureKanbanState(ctx, prID))
	return prID
}

func SeedVerifiedRepo(
	t *testing.T, database *db.DB, identity db.RepoIdentity,
) {
	t.Helper()
	entry, _, err := database.ReconcileRepositoryObservation(
		t.Context(), identity, time.Now().UTC(),
	)
	require.NoError(t, err)
	require.NotNil(t, entry)
}

func SeedWorkspace(
	t *testing.T,
	database *db.DB,
	id, owner, name, itemType string,
	number int,
) {
	t.Helper()
	repoID, err := database.UpsertRepo(
		t.Context(), VerifiedGitHubRepoIdentity("github.com", owner, name),
	)
	require.NoError(t, err)
	require.NoError(t, database.InsertWorkspace(t.Context(), &db.Workspace{
		ID:              id,
		RepoID:          repoID,
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       owner,
		RepoName:        name,
		ItemType:        itemType,
		ItemNumber:      number,
		GitHeadRef:      "feature/" + id,
		WorkspaceBranch: "kenn-forge/" + id,
		WorktreePath:    filepath.Join(t.TempDir(), id),
		TmuxSession:     id,
		Status:          "ready",
	}))
}

const (
	ServerRuntimeHelperMarker        = "kenn-forge-runtime-helper"
	ServerPtyOwnerParentHelperMarker = "kenn-forge-pty-owner-parent-helper"
)

func SettingsReposFromBody(t *testing.T, body []byte) []ghclient.ConfiguredRepoStatus {
	t.Helper()
	var resp struct {
		Repos []ghclient.ConfiguredRepoStatus `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(body, &resp, V1JSON))
	return resp.Repos
}

func StackMemberNumbers(members []generated.StackMemberResponse) []int64 {
	numbers := make([]int64, len(members))
	for i, member := range members {
		numbers[i] = member.Number
	}
	return numbers
}

func (l StaticListener) Accept() (net.Conn, error) { return nil, errors.New("unused listener") }

func (l StaticListener) Addr() net.Addr { return l.AddrValue }

func (l StaticListener) Close() error { return nil }

type StaticListener struct {
	AddrValue net.Addr
}

func (a StaticListenerAddr) Network() string { return "tcp" }

func (a StaticListenerAddr) String() string { return string(a) }

type StaticListenerAddr string

func (s StaticTestTokenSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{Key: tokenauth.Key{Platform: "test", Host: "test"}}
}

func (s StaticTestTokenSource) Invalidate(string) {}

func (s StaticTestTokenSource) Token(context.Context) (string, error) {
	return string(s), nil
}

type StaticTestTokenSource string

func TestTokenSource(token string) tokenauth.Source {
	return StaticTestTokenSource(token)
}

func UpsertComment(comments []gitealike.CommentDTO, comment gitealike.CommentDTO) []gitealike.CommentDTO {
	for i := range comments {
		if comments[i].ID == comment.ID {
			comments[i] = comment
			return comments
		}
	}
	return append(comments, comment)
}

func VerifiedGitHubRepoIdentity(host, owner, name string) db.RepoIdentity {
	identity := db.GitHubRepoIdentity(host, owner, name)
	identity.PlatformRepoID = "repo-" + strings.ToLower(owner+"-"+name)
	return identity
}

func WithSeedPRAuthor(author string) SeedPROpt {
	return func(pr *db.MergeRequest) { pr.Author = author }
}

func WithSeedPRBaseSHA(baseSHA string) SeedPROpt {
	return func(pr *db.MergeRequest) { pr.PlatformBaseSHA = baseSHA }
}

func WithSeedPRCI(status, checksJSON string) SeedPROpt {
	return func(pr *db.MergeRequest) {
		pr.CIStatus = status
		pr.CIChecksJSON = checksJSON
	}
}

func WithSeedPRHeadSHA(headSHA string) SeedPROpt {
	return func(pr *db.MergeRequest) { pr.PlatformHeadSHA = headSHA }
}

func WithSeedPRLifecycle(
	state db.MergeRequestState,
	mergedAt *time.Time,
	closedAt *time.Time,
) SeedPROpt {
	return func(pr *db.MergeRequest) {
		pr.State = state
		pr.MergedAt = mergedAt
		pr.ClosedAt = closedAt
	}
}

func WithSeedPRTimes(createdAt, updatedAt, lastActivityAt time.Time) SeedPROpt {
	return func(pr *db.MergeRequest) {
		pr.CreatedAt = createdAt
		pr.UpdatedAt = updatedAt
		pr.LastActivityAt = lastActivityAt
	}
}

func WithSeedPRTitle(title string) SeedPROpt {
	return func(pr *db.MergeRequest) { pr.Title = title }
}

// RunMain runs a server test binary inside a private tmux owner and a
// contained process tree, and cleans both up. Test packages that start real
// servers call it from TestMain.
func RunMain(m *testing.M) int {
	if code, ok := testtmux.CommandWrapperExitCode(); ok {
		return code
	}
	if IsServerHelperProcess() {
		return m.Run()
	}
	if err := processjob.ContainCurrentProcessTree(); err != nil {
		fmt.Fprintf(os.Stderr, "contain server test process tree: %v\n", err)
		return 1
	}
	if testtmux.Supported() {
		var ownerErr error
		PrivateTmuxOwner, ownerErr = testtmux.New()
		if ownerErr != nil {
			fmt.Fprintf(os.Stderr, "initialize private test tmux owner: %v\n", ownerErr)
			return 1
		}
	}
	envDir, envDirErr := os.MkdirTemp("", "kenn-forge-server-tmux-env-*")
	if envDirErr == nil {
		_ = os.Setenv("KENN_FORGE_TMUX_ENV_DIR", envDir)
	}
	runCleanup, stopSignalCleanup := testsignal.Install(func() error {
		return CleanupServerTestTmux(PrivateTmuxOwner)
	}, func(err error) {
		fmt.Fprintf(os.Stderr, "cleanup kenn-forge test tmux sessions: %v\n", err)
	})
	code := gitsafe.RunIsolatedMain(m)
	if err := runCleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup kenn-forge test tmux sessions: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	stopSignalCleanup()
	if envDirErr == nil {
		_ = os.RemoveAll(envDir)
	}
	return code
}

// V1JSON keeps encoding/json v1 semantics for helpers written against it:
// case-insensitive field names, nil slices and maps as null, and v1's
// tolerance of duplicate names and invalid UTF-8.
var V1JSON = json.JoinOptions(
	json.MatchCaseInsensitiveNames(true),
	json.FormatNilSliceAsNull(true),
	json.FormatNilMapAsNull(true),
	jsontext.AllowDuplicateNames(true),
	jsontext.AllowInvalidUTF8(true),
)

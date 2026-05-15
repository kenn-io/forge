package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/ratelimit"
)

const (
	defaultPreviewLimit = 200
	maxPreviewLimit     = 1000
	defaultPageSize     = 100
)

type ClientOption func(*clientOptions)

type clientOptions struct {
	baseURL           string
	foregroundTimeout time.Duration
	rateTracker       *ratelimit.RateTracker
}

type Client struct {
	host              string
	baseURL           string
	api               *gitlab.Client
	foregroundTimeout time.Duration
}

type PreviewOptions struct {
	Limit int
}

type PreviewResult struct {
	Repositories  []platform.Repository
	Limit         int
	ReturnedCount int
	ScannedCount  int
	Truncated     bool
	PartialErrors []PartialError
}

type PartialError struct {
	Code      string
	Namespace string
	Page      int64
}

func WithBaseURLForTesting(baseURL string) ClientOption {
	return func(opts *clientOptions) {
		opts.baseURL = strings.TrimRight(baseURL, "/")
	}
}

func WithForegroundTimeoutForTesting(timeout time.Duration) ClientOption {
	return func(opts *clientOptions) {
		opts.foregroundTimeout = timeout
	}
}

func WithRateTracker(rateTracker *ratelimit.RateTracker) ClientOption {
	return func(opts *clientOptions) {
		opts.rateTracker = rateTracker
	}
}

func NewClient(host, token string, options ...ClientOption) (*Client, error) {
	opts := clientOptions{
		baseURL:           "https://" + strings.TrimRight(host, "/") + "/api/v4",
		foregroundTimeout: 20 * time.Second,
	}
	for _, option := range options {
		option(&opts)
	}

	clientOptions := []gitlab.ClientOptionFunc{gitlab.WithBaseURL(opts.baseURL)}
	if opts.rateTracker != nil {
		clientOptions = append(clientOptions, gitlab.WithHTTPClient(&http.Client{
			Transport: &rateTrackingTransport{
				base:        http.DefaultTransport,
				rateTracker: opts.rateTracker,
			},
		}))
	}

	api, err := gitlab.NewClient(token, clientOptions...)
	if err != nil {
		return nil, err
	}
	return &Client{
		host:              host,
		baseURL:           opts.baseURL,
		api:               api,
		foregroundTimeout: opts.foregroundTimeout,
	}, nil
}

type rateTrackingTransport struct {
	base        http.RoundTripper
	rateTracker *ratelimit.RateTracker
}

func (t *rateTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if resp == nil || t.rateTracker == nil {
		return resp, err
	}
	t.rateTracker.RecordRequest()
	if rate, ok := parseRateLimitHeaders(resp); ok {
		t.rateTracker.UpdateFromRate(rate)
	}
	return resp, err
}

func parseRateLimitHeaders(resp *http.Response) (ratelimit.Rate, bool) {
	remaining, remainingOK := parseHeaderInt(resp, "RateLimit-Remaining", "X-RateLimit-Remaining")
	if !remainingOK {
		return ratelimit.Rate{}, false
	}
	limit, _ := parseHeaderInt(resp, "RateLimit-Limit", "X-RateLimit-Limit")
	resetAt := time.Now().UTC().Add(time.Minute)
	if resetUnix, ok := parseHeaderInt64(resp, "RateLimit-Reset", "X-RateLimit-Reset"); ok {
		resetAt = time.Unix(resetUnix, 0).UTC()
	}
	return ratelimit.Rate{
		Limit:     limit,
		Remaining: remaining,
		Reset:     resetAt,
	}, true
}

func parseHeaderInt(resp *http.Response, names ...string) (int, bool) {
	for _, name := range names {
		raw := strings.TrimSpace(resp.Header.Get(name))
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err == nil {
			return value, true
		}
	}
	return 0, false
}

func parseHeaderInt64(resp *http.Response, names ...string) (int64, bool) {
	for _, name := range names {
		raw := strings.TrimSpace(resp.Header.Get(name))
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err == nil {
			return value, true
		}
	}
	return 0, false
}

func (c *Client) Platform() platform.Kind {
	return platform.KindGitLab
}

func (c *Client) Host() string {
	return c.host
}

func (c *Client) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReadReleases:           true,
		ReadCI:                 true,
		ThreadReply:            true,
		ThreadResolve:          true,
		ReviewDraftMutation:    true,
		ReviewThreadResolution: true,
		ReadReviewThreads:      true,
		NativeMultilineRanges:  false,
		SupportedReviewActions: []platform.ReviewAction{
			platform.ReviewActionComment,
			platform.ReviewActionApprove,
		},
	}
}

func (c *Client) GetRepository(ctx context.Context, ref platform.RepoRef) (platform.Repository, error) {
	pid, err := projectLookupArg(ref)
	if err != nil {
		return platform.Repository{}, err
	}
	project, _, err := c.api.Projects.GetProject(pid, nil, gitlab.WithContext(ctx))
	if err != nil {
		return platform.Repository{}, mapGitLabError("get_repository", err)
	}
	return NormalizeProject(c.host, project)
}

func (c *Client) ListRepositories(
	ctx context.Context,
	owner string,
	opts platform.RepositoryListOptions,
) ([]platform.Repository, error) {
	preview, err := c.PreviewNamespace(ctx, owner, PreviewOptions{Limit: opts.Limit})
	if err != nil {
		return nil, err
	}
	return preview.Repositories, nil
}

func (c *Client) PreviewNamespace(
	ctx context.Context,
	namespace string,
	opts PreviewOptions,
) (PreviewResult, error) {
	if err := ctx.Err(); err != nil {
		return PreviewResult{}, err
	}

	limit, capped := normalizePreviewLimit(opts.Limit)
	result := PreviewResult{Limit: limit, Truncated: capped}
	ctx, cancel := c.withForegroundTimeout(ctx)
	defer cancel()

	result, err := c.previewGroup(ctx, namespace, result)
	if err == nil {
		return result.finish(), nil
	}
	if !isGitLabStatus(err, http.StatusNotFound) {
		return PreviewResult{}, mapGitLabError("preview_group", err)
	}
	if err := ctx.Err(); err != nil {
		return PreviewResult{}, err
	}
	result = PreviewResult{Limit: limit, Truncated: capped}
	result, err = c.previewUser(ctx, namespace, result)
	if err != nil {
		return PreviewResult{}, mapGitLabError("preview_user", err)
	}
	return result.finish(), nil
}

func (c *Client) ListOpenMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
) ([]platform.MergeRequest, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return nil, err
	}
	state := "opened"
	recheck := true
	opt := &gitlab.ListProjectMergeRequestsOptions{
		State:                  &state,
		WithMergeStatusRecheck: &recheck,
		ListOptions: gitlab.ListOptions{
			Page:    1,
			PerPage: defaultPageSize,
		},
	}

	var out []platform.MergeRequest
	for {
		mrs, resp, err := c.api.MergeRequests.ListProjectMergeRequests(pid, opt, gitlab.WithContext(ctx))
		if err != nil {
			return nil, mapGitLabError("list_merge_requests", err)
		}
		for _, mr := range mrs {
			out = append(out, NormalizeMergeRequest(normalizedRef, mr, nil))
		}
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}

func (c *Client) GetMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.MergeRequest, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	mr, _, err := c.api.MergeRequests.GetMergeRequest(pid, int64(number), nil, gitlab.WithContext(ctx))
	if err != nil {
		return platform.MergeRequest{}, mapGitLabError("get_merge_request", err)
	}
	return NormalizeDetailedMergeRequest(normalizedRef, mr), nil
}

func (c *Client) ListMergeRequestEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.MergeRequestEvent, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return nil, err
	}
	discussions, err := c.listMergeRequestDiscussions(ctx, pid, number)
	if err != nil {
		return nil, err
	}
	commits, err := c.listMergeRequestCommits(ctx, pid, number)
	if err != nil {
		return nil, err
	}

	events := NormalizeMergeRequestDiscussions(normalizedRef, number, discussions)
	for _, commit := range commits {
		events = append(events, NormalizeCommitEvent(normalizedRef, number, commit))
	}
	return events, nil
}

func (c *Client) ListOpenIssues(ctx context.Context, ref platform.RepoRef) ([]platform.Issue, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return nil, err
	}
	state := "opened"
	opt := &gitlab.ListProjectIssuesOptions{
		State: &state,
		ListOptions: gitlab.ListOptions{
			Page:    1,
			PerPage: defaultPageSize,
		},
	}

	var out []platform.Issue
	for {
		issues, resp, err := c.api.Issues.ListProjectIssues(pid, opt, gitlab.WithContext(ctx))
		if err != nil {
			return nil, mapGitLabError("list_issues", err)
		}
		for _, issue := range issues {
			out = append(out, NormalizeIssue(normalizedRef, issue))
		}
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}

func (c *Client) GetIssue(ctx context.Context, ref platform.RepoRef, number int) (platform.Issue, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.Issue{}, err
	}
	issue, _, err := c.api.Issues.GetIssue(pid, int64(number), nil, gitlab.WithContext(ctx))
	if err != nil {
		return platform.Issue{}, mapGitLabError("get_issue", err)
	}
	return NormalizeIssue(normalizedRef, issue), nil
}

func (c *Client) ListIssueEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.IssueEvent, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return nil, err
	}
	discussions, err := c.listIssueDiscussions(ctx, pid, number)
	if err != nil {
		return nil, err
	}
	return NormalizeIssueDiscussions(normalizedRef, number, discussions), nil
}

func (c *Client) ListReleases(ctx context.Context, ref platform.RepoRef) ([]platform.Release, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return nil, err
	}
	opt := &gitlab.ListReleasesOptions{ListOptions: gitlab.ListOptions{Page: 1, PerPage: defaultPageSize}}

	var out []platform.Release
	for {
		releases, resp, err := c.api.Releases.ListReleases(pid, opt, gitlab.WithContext(ctx))
		if err != nil {
			return nil, mapGitLabError("list_releases", err)
		}
		for _, release := range releases {
			out = append(out, NormalizeRelease(normalizedRef, release))
		}
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}

func (c *Client) ListTags(ctx context.Context, ref platform.RepoRef) ([]platform.Tag, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return nil, err
	}
	opt := &gitlab.ListTagsOptions{ListOptions: gitlab.ListOptions{Page: 1, PerPage: defaultPageSize}}

	var out []platform.Tag
	for {
		tags, resp, err := c.api.Tags.ListTags(pid, opt, gitlab.WithContext(ctx))
		if err != nil {
			return nil, mapGitLabError("list_tags", err)
		}
		for _, tag := range tags {
			out = append(out, NormalizeTag(normalizedRef, tag))
		}
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}

func (c *Client) ListCIChecks(
	ctx context.Context,
	ref platform.RepoRef,
	sha string,
) ([]platform.CICheck, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return nil, err
	}
	opt := &gitlab.ListProjectPipelinesOptions{
		SHA: &sha,
		ListOptions: gitlab.ListOptions{
			Page:    1,
			PerPage: 1,
		},
	}
	pipelines, _, err := c.api.Pipelines.ListProjectPipelines(pid, opt, gitlab.WithContext(ctx))
	if err != nil {
		return nil, mapGitLabError("list_ci_checks", err)
	}
	if len(pipelines) == 0 {
		return nil, nil
	}
	return []platform.CICheck{NormalizePipeline(normalizedRef, pipelines[0])}, nil
}

func (c *Client) listMergeRequestDiscussions(ctx context.Context, pid any, number int) ([]*gitlab.Discussion, error) {
	opt := &gitlab.ListMergeRequestDiscussionsOptions{ListOptions: gitlab.ListOptions{Page: 1, PerPage: defaultPageSize}}
	var out []*gitlab.Discussion
	for {
		discussions, resp, err := c.api.Discussions.ListMergeRequestDiscussions(pid, int64(number), opt, gitlab.WithContext(ctx))
		if err != nil {
			return nil, mapGitLabError("list_merge_request_discussions", err)
		}
		out = append(out, discussions...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}

func (c *Client) listIssueDiscussions(ctx context.Context, pid any, number int) ([]*gitlab.Discussion, error) {
	opt := &gitlab.ListIssueDiscussionsOptions{ListOptions: gitlab.ListOptions{Page: 1, PerPage: defaultPageSize}}
	var out []*gitlab.Discussion
	for {
		discussions, resp, err := c.api.Discussions.ListIssueDiscussions(pid, int64(number), opt, gitlab.WithContext(ctx))
		if err != nil {
			return nil, mapGitLabError("list_issue_discussions", err)
		}
		out = append(out, discussions...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}

func (c *Client) listMergeRequestCommits(ctx context.Context, pid any, number int) ([]*gitlab.Commit, error) {
	opt := &gitlab.GetMergeRequestCommitsOptions{ListOptions: gitlab.ListOptions{Page: 1, PerPage: defaultPageSize}}
	var out []*gitlab.Commit
	for {
		commits, resp, err := c.api.MergeRequests.GetMergeRequestCommits(pid, int64(number), opt, gitlab.WithContext(ctx))
		if err != nil {
			return nil, mapGitLabError("list_merge_request_commits", err)
		}
		out = append(out, commits...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}

func (c *Client) previewGroup(
	ctx context.Context,
	namespace string,
	result PreviewResult,
) (PreviewResult, error) {
	archived := false
	includeSubGroups := true
	opt := &gitlab.ListGroupProjectsOptions{
		Archived:         &archived,
		IncludeSubGroups: &includeSubGroups,
		ListOptions: gitlab.ListOptions{
			Page:    1,
			PerPage: pageSizeForRemaining(result.Limit),
		},
	}
	for {
		projects, resp, err := c.api.Groups.ListGroupProjects(namespace, opt, gitlab.WithContext(ctx))
		if err != nil {
			if len(result.Repositories) > 0 {
				result.Truncated = true
				result.PartialErrors = append(result.PartialErrors, partialError(namespace, opt.Page, err))
				return result, nil
			}
			return result, err
		}
		result = appendPreviewProjects(result, c.host, namespace, projects)
		if result.ReturnedCount >= result.Limit {
			result.Truncated = true
			return result, nil
		}
		if resp == nil || resp.NextPage == 0 {
			return result, nil
		}
		opt.Page = resp.NextPage
		opt.PerPage = pageSizeForRemaining(result.Limit - result.ReturnedCount)
	}
}

func (c *Client) previewUser(
	ctx context.Context,
	namespace string,
	result PreviewResult,
) (PreviewResult, error) {
	archived := false
	opt := &gitlab.ListProjectsOptions{
		Archived: &archived,
		ListOptions: gitlab.ListOptions{
			Page:    1,
			PerPage: pageSizeForRemaining(result.Limit),
		},
	}
	for {
		projects, resp, err := c.api.Projects.ListUserProjects(namespace, opt, gitlab.WithContext(ctx))
		if err != nil {
			if len(result.Repositories) > 0 {
				result.Truncated = true
				result.PartialErrors = append(result.PartialErrors, partialError(namespace, opt.Page, err))
				return result, nil
			}
			return result, err
		}
		result = appendPreviewProjects(result, c.host, namespace, projects)
		if result.ReturnedCount >= result.Limit {
			result.Truncated = true
			return result, nil
		}
		if resp == nil || resp.NextPage == 0 {
			return result, nil
		}
		opt.Page = resp.NextPage
		opt.PerPage = pageSizeForRemaining(result.Limit - result.ReturnedCount)
	}
}

func appendPreviewProjects(
	result PreviewResult,
	host string,
	namespace string,
	projects []*gitlab.Project,
) PreviewResult {
	for _, project := range projects {
		if project == nil {
			continue
		}
		result.ScannedCount++
		if project.Archived {
			continue
		}
		if !namespaceMatches(namespace, project.PathWithNamespace) {
			continue
		}
		if result.ReturnedCount >= result.Limit {
			result.Truncated = true
			continue
		}
		repo, err := NormalizeProject(host, project)
		if err != nil {
			result.PartialErrors = append(result.PartialErrors, PartialError{
				Code:      "unsafe_project_path",
				Namespace: namespace,
			})
			continue
		}
		result.Repositories = append(result.Repositories, repo)
		result.ReturnedCount++
	}
	return result
}

func (r PreviewResult) finish() PreviewResult {
	r.ReturnedCount = len(r.Repositories)
	return r
}

func normalizePreviewLimit(limit int) (int, bool) {
	if limit <= 0 {
		return defaultPreviewLimit, false
	}
	if limit > maxPreviewLimit {
		return maxPreviewLimit, true
	}
	return limit, false
}

func pageSizeForRemaining(remaining int) int64 {
	if remaining <= 0 {
		return 1
	}
	if remaining < defaultPageSize {
		return int64(remaining)
	}
	return defaultPageSize
}

func namespaceMatches(namespace, repoPath string) bool {
	return repoPath == namespace || strings.HasPrefix(repoPath, namespace+"/")
}

func (c *Client) withForegroundTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.foregroundTimeout <= 0 {
		return ctx, func() {}
	}
	deadline := time.Now().Add(c.foregroundTimeout)
	if existing, ok := ctx.Deadline(); ok && existing.Before(deadline) {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, deadline)
}

func (c *Client) projectScopedArg(ctx context.Context, ref platform.RepoRef) (any, platform.RepoRef, error) {
	if ref.PlatformID != 0 {
		return ref.PlatformID, c.normalizeRef(ref, ref.PlatformID), nil
	}
	repo, err := c.GetRepository(ctx, ref)
	if err != nil {
		return nil, platform.RepoRef{}, err
	}
	return repo.Ref.PlatformID, repo.Ref, nil
}

func (c *Client) normalizeRef(ref platform.RepoRef, id int64) platform.RepoRef {
	ref.Platform = platform.KindGitLab
	ref.Host = c.host
	ref.PlatformID = id
	if ref.PlatformExternalID == "" && id != 0 {
		ref.PlatformExternalID = strconv.FormatInt(id, 10)
	}
	return ref
}

func projectLookupArg(ref platform.RepoRef) (any, error) {
	if ref.PlatformID != 0 {
		return ref.PlatformID, nil
	}
	return rawProjectPath(ref)
}

func rawProjectPath(ref platform.RepoRef) (string, error) {
	repoPath := strings.Trim(ref.RepoPath, "/")
	if repoPath == "" {
		repoPath = strings.Trim(strings.Trim(ref.Owner, "/")+"/"+strings.Trim(ref.Name, "/"), "/")
	}
	if repoPath == "" || !strings.Contains(repoPath, "/") || hasEscapedSlash(repoPath) {
		return "", &platform.Error{
			Code:       platform.ErrCodeInvalidRepoRef,
			Provider:   platform.KindGitLab,
			Field:      "repo_path",
			Capability: "project_lookup",
		}
	}
	return repoPath, nil
}

func hasEscapedSlash(value string) bool {
	for {
		lower := strings.ToLower(value)
		if strings.Contains(lower, "%2f") {
			return true
		}
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return true
		}
		if decoded == value {
			return false
		}
		if strings.Count(decoded, "/") > strings.Count(value, "/") {
			return true
		}
		value = decoded
	}
}

func mapGitLabError(capability string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var gitlabErr *gitlab.ErrorResponse
	code := platform.ErrCodeInvalidRepoRef
	if errors.As(err, &gitlabErr) {
		switch {
		case gitlabErr.HasStatusCode(http.StatusUnauthorized), gitlabErr.HasStatusCode(http.StatusForbidden):
			code = platform.ErrCodePermissionDenied
		case gitlabErr.HasStatusCode(http.StatusNotFound):
			code = platform.ErrCodeNotFound
		case gitlabErr.HasStatusCode(http.StatusTooManyRequests):
			code = platform.ErrCodeRateLimited
		}
	}
	return &platform.Error{
		Code:       code,
		Provider:   platform.KindGitLab,
		Capability: capability,
		Err:        err,
	}
}

func isGitLabStatus(err error, status int) bool {
	var gitlabErr *gitlab.ErrorResponse
	if errors.As(err, &gitlabErr) && gitlabErr.HasStatusCode(status) {
		return true
	}
	return strings.Contains(err.Error(), strconv.Itoa(status))
}

func partialError(namespace string, page int64, err error) PartialError {
	code := "upstream_error"
	var platformErr *platform.Error
	if errors.As(mapGitLabError("preview_page", err), &platformErr) {
		code = string(platformErr.Code)
		if code == string(platform.ErrCodeInvalidRepoRef) {
			code = "upstream_error"
		}
	}
	return PartialError{Code: code, Namespace: namespace, Page: page}
}

func pipelineInfo(mr *gitlab.MergeRequest) *gitlab.PipelineInfo {
	if mr == nil {
		return nil
	}
	if mr.Pipeline != nil {
		return mr.Pipeline
	}
	if mr.HeadPipeline != nil {
		return &gitlab.PipelineInfo{
			ID:        mr.HeadPipeline.ID,
			IID:       mr.HeadPipeline.IID,
			ProjectID: mr.HeadPipeline.ProjectID,
			Status:    mr.HeadPipeline.Status,
			Ref:       mr.HeadPipeline.Ref,
			SHA:       mr.HeadPipeline.SHA,
			WebURL:    mr.HeadPipeline.WebURL,
			CreatedAt: mr.HeadPipeline.CreatedAt,
			UpdatedAt: mr.HeadPipeline.UpdatedAt,
		}
	}
	return nil
}

var _ platform.Provider = (*Client)(nil)
var _ platform.RepositoryReader = (*Client)(nil)
var _ platform.MergeRequestReader = (*Client)(nil)
var _ platform.IssueReader = (*Client)(nil)
var _ platform.ReleaseReader = (*Client)(nil)
var _ platform.TagReader = (*Client)(nil)
var _ platform.CIReader = (*Client)(nil)
var _ platform.ThreadReplier = (*Client)(nil)
var _ platform.ThreadResolver = (*Client)(nil)

package gitealike

import (
	"context"
	"errors"
	"fmt"

	"go.kenn.io/middleman/internal/platform"
)

const maxCollectedPages = 1000

type Provider struct {
	kind      platform.Kind
	host      string
	transport Transport
	options   options
}

type options struct {
	ReadActions bool
	Mutations   bool
}

type Option func(*options)

func WithReadActions() Option {
	return func(options *options) {
		options.ReadActions = true
	}
}

func WithMutations() Option {
	return func(options *options) {
		options.Mutations = true
	}
}

func NewProvider(
	kind platform.Kind,
	host string,
	transport Transport,
	opts ...Option,
) *Provider {
	var options options
	for _, opt := range opts {
		opt(&options)
	}
	return &Provider{
		kind:      kind,
		host:      host,
		transport: transport,
		options:   options,
	}
}

func (p *Provider) Platform() platform.Kind {
	return p.kind
}

func (p *Provider) Host() string {
	return p.host
}

func (p *Provider) Capabilities() platform.Capabilities {
	caps := platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReleases:      true,
		ReadCI:            true,
	}
	if p.options.Mutations {
		caps.CommentMutation = true
		caps.StateMutation = true
		caps.MergeMutation = true
		caps.ReviewMutation = true
		caps.IssueMutation = true
	}
	return caps
}

func (p *Provider) GetRepository(
	ctx context.Context,
	ref platform.RepoRef,
) (platform.Repository, error) {
	repo, err := p.transport.GetRepository(ctx, ref.Owner, ref.Name)
	if err != nil {
		return platform.Repository{}, p.mapError(err)
	}
	return NormalizeRepository(p.kind, p.host, repo)
}

func (p *Provider) ListRepositories(
	ctx context.Context,
	owner string,
	opts platform.RepositoryListOptions,
) ([]platform.Repository, error) {
	repos, err := p.listRepositories(ctx, owner, p.transport.ListUserRepositories)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			repos, err = p.listRepositories(ctx, owner, p.transport.ListOrgRepositories)
		}
	}
	if err != nil {
		return nil, err
	}
	if opts.Limit <= 0 && opts.Offset <= 0 {
		return repos, nil
	}
	return applyRepositoryListOptions(repos, opts), nil
}

func (p *Provider) ListOpenMergeRequests(
	ctx context.Context,
	ref platform.RepoRef,
) ([]platform.MergeRequest, error) {
	items, err := collectPages(ctx, func(opts PageOptions) ([]PullRequestDTO, Page, error) {
		return p.transport.ListOpenPullRequests(ctx, ref, opts)
	})
	if err != nil {
		return nil, p.mapError(err)
	}
	out := make([]platform.MergeRequest, 0, len(items))
	for _, item := range items {
		out = append(out, NormalizePullRequest(ref, item))
	}
	return out, nil
}

func (p *Provider) GetMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.MergeRequest, error) {
	pr, err := p.transport.GetPullRequest(ctx, ref, number)
	if err != nil {
		return platform.MergeRequest{}, p.mapError(err)
	}
	return NormalizePullRequest(ref, pr), nil
}

func (p *Provider) ListMergeRequestEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.MergeRequestEvent, error) {
	comments, err := collectPages(ctx, func(opts PageOptions) ([]CommentDTO, Page, error) {
		return p.transport.ListPullRequestComments(ctx, ref, number, opts)
	})
	if err != nil {
		return nil, p.mapError(err)
	}
	reviews, err := collectPages(ctx, func(opts PageOptions) ([]ReviewDTO, Page, error) {
		return p.transport.ListPullRequestReviews(ctx, ref, number, opts)
	})
	if err != nil {
		return nil, p.mapError(err)
	}
	commits, err := collectPages(ctx, func(opts PageOptions) ([]CommitDTO, Page, error) {
		return p.transport.ListPullRequestCommits(ctx, ref, number, opts)
	})
	if err != nil {
		return nil, p.mapError(err)
	}
	events := NormalizeMergeRequestEvents(p.kind, ref, number, comments, reviews, commits)
	timeline, err := p.listTimelineEvents(ctx, ref, number)
	if err != nil {
		return nil, err
	}
	events = append(events, NormalizeMergeRequestTimelineEvents(p.kind, ref, number, timeline)...)
	return events, nil
}

func (p *Provider) ListOpenIssues(
	ctx context.Context,
	ref platform.RepoRef,
) ([]platform.Issue, error) {
	items, err := collectPages(ctx, func(opts PageOptions) ([]IssueDTO, Page, error) {
		return p.transport.ListOpenIssues(ctx, ref, opts)
	})
	if err != nil {
		return nil, p.mapError(err)
	}
	out := make([]platform.Issue, 0, len(items))
	for _, item := range items {
		if item.IsPullRequest {
			continue
		}
		out = append(out, NormalizeIssue(ref, item))
	}
	return out, nil
}

func (p *Provider) GetIssue(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) (platform.Issue, error) {
	issue, err := p.transport.GetIssue(ctx, ref, number)
	if err != nil {
		return platform.Issue{}, p.mapError(err)
	}
	return NormalizeIssue(ref, issue), nil
}

func (p *Provider) ListIssueEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.IssueEvent, error) {
	comments, err := collectPages(ctx, func(opts PageOptions) ([]CommentDTO, Page, error) {
		return p.transport.ListIssueComments(ctx, ref, number, opts)
	})
	if err != nil {
		return nil, p.mapError(err)
	}
	events := NormalizeIssueComments(p.kind, ref, number, comments)
	timeline, err := p.listTimelineEvents(ctx, ref, number)
	if err != nil {
		return nil, err
	}
	events = append(events, NormalizeIssueTimelineEvents(p.kind, ref, number, timeline)...)
	return events, nil
}

func (p *Provider) listTimelineEvents(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]TimelineEventDTO, error) {
	timelineTransport, ok := p.transport.(TimelineTransport)
	if !ok {
		return nil, nil
	}
	timeline, err := collectPages(ctx, func(opts PageOptions) ([]TimelineEventDTO, Page, error) {
		return timelineTransport.ListIssueTimeline(ctx, ref, number, opts)
	})
	if err != nil {
		err = p.mapError(err)
		if errors.Is(err, platform.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return timeline, nil
}

func (p *Provider) ListReleases(
	ctx context.Context,
	ref platform.RepoRef,
) ([]platform.Release, error) {
	items, err := collectPages(ctx, func(opts PageOptions) ([]ReleaseDTO, Page, error) {
		return p.transport.ListReleases(ctx, ref, opts)
	})
	if err != nil {
		return nil, p.mapError(err)
	}
	out := make([]platform.Release, 0, len(items))
	for _, item := range items {
		out = append(out, NormalizeRelease(ref, item))
	}
	return out, nil
}

func (p *Provider) ListTags(ctx context.Context, ref platform.RepoRef) ([]platform.Tag, error) {
	items, err := collectPages(ctx, func(opts PageOptions) ([]TagDTO, Page, error) {
		return p.transport.ListTags(ctx, ref, opts)
	})
	if err != nil {
		return nil, p.mapError(err)
	}
	out := make([]platform.Tag, 0, len(items))
	for _, item := range items {
		out = append(out, NormalizeTag(ref, item))
	}
	return out, nil
}

func (p *Provider) ListCIChecks(
	ctx context.Context,
	ref platform.RepoRef,
	sha string,
) ([]platform.CICheck, error) {
	statuses, err := collectPages(ctx, func(opts PageOptions) ([]StatusDTO, Page, error) {
		return p.transport.ListStatuses(ctx, ref, sha, opts)
	})
	if err != nil {
		return nil, p.mapError(err)
	}
	var actionRuns []ActionRunDTO
	if p.options.ReadActions {
		if actionsTransport, ok := p.transport.(ActionsTransport); ok {
			actionRuns, err = collectPages(ctx, func(opts PageOptions) ([]ActionRunDTO, Page, error) {
				return actionsTransport.ListActionRuns(ctx, ref, sha, opts)
			})
			if err != nil {
				return nil, p.mapError(err)
			}
		}
	}
	return NormalizeStatuses(ref, statuses, actionRuns), nil
}

func (p *Provider) CreateMergeRequestComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.MergeRequestEvent, error) {
	transport, err := p.mutationTransport("comment_mutation")
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	comment, err := transport.CreateIssueComment(ctx, ref, number, body)
	if err != nil {
		return platform.MergeRequestEvent{}, p.mapError(err)
	}
	return NormalizeMergeRequestEvents(p.kind, ref, number, []CommentDTO{comment}, nil, nil)[0], nil
}

func (p *Provider) EditMergeRequestComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commentID int64,
	body string,
) (platform.MergeRequestEvent, error) {
	transport, err := p.mutationTransport("comment_mutation")
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	comment, err := transport.EditIssueComment(ctx, ref, commentID, body)
	if err != nil {
		return platform.MergeRequestEvent{}, p.mapError(err)
	}
	return NormalizeMergeRequestEvents(p.kind, ref, number, []CommentDTO{comment}, nil, nil)[0], nil
}

func (p *Provider) CreateIssueComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.IssueEvent, error) {
	transport, err := p.mutationTransport("comment_mutation")
	if err != nil {
		return platform.IssueEvent{}, err
	}
	comment, err := transport.CreateIssueComment(ctx, ref, number, body)
	if err != nil {
		return platform.IssueEvent{}, p.mapError(err)
	}
	return NormalizeIssueComments(p.kind, ref, number, []CommentDTO{comment})[0], nil
}

func (p *Provider) EditIssueComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commentID int64,
	body string,
) (platform.IssueEvent, error) {
	transport, err := p.mutationTransport("comment_mutation")
	if err != nil {
		return platform.IssueEvent{}, err
	}
	comment, err := transport.EditIssueComment(ctx, ref, commentID, body)
	if err != nil {
		return platform.IssueEvent{}, p.mapError(err)
	}
	return NormalizeIssueComments(p.kind, ref, number, []CommentDTO{comment})[0], nil
}

func (p *Provider) CreateIssue(
	ctx context.Context,
	ref platform.RepoRef,
	title string,
	body string,
) (platform.Issue, error) {
	transport, err := p.mutationTransport("issue_mutation")
	if err != nil {
		return platform.Issue{}, err
	}
	issue, err := transport.CreateIssue(ctx, ref, title, body)
	if err != nil {
		return platform.Issue{}, p.mapError(err)
	}
	return NormalizeIssue(ref, issue), nil
}

func (p *Provider) SetMergeRequestState(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	state string,
) (platform.MergeRequest, error) {
	transport, err := p.mutationTransport("state_mutation")
	if err != nil {
		return platform.MergeRequest{}, err
	}
	pr, err := transport.EditPullRequest(ctx, ref, number, PullRequestMutationOptions{State: &state})
	if err != nil {
		return platform.MergeRequest{}, p.mapError(err)
	}
	return NormalizePullRequest(ref, pr), nil
}

func (p *Provider) SetIssueState(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	state string,
) (platform.Issue, error) {
	transport, err := p.mutationTransport("state_mutation")
	if err != nil {
		return platform.Issue{}, err
	}
	issue, err := transport.EditIssue(ctx, ref, number, IssueMutationOptions{State: &state})
	if err != nil {
		return platform.Issue{}, p.mapError(err)
	}
	return NormalizeIssue(ref, issue), nil
}

func (p *Provider) MergeMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commitTitle string,
	commitMessage string,
	method string,
) (platform.MergeResult, error) {
	transport, err := p.mutationTransport("merge_mutation")
	if err != nil {
		return platform.MergeResult{}, err
	}
	result, err := transport.MergePullRequest(ctx, ref, number, MergeOptions{
		CommitTitle:   commitTitle,
		CommitMessage: commitMessage,
		Method:        method,
	})
	if err != nil {
		return platform.MergeResult{}, p.mapError(err)
	}
	return platform.MergeResult{Merged: result.Merged, SHA: result.SHA, Message: result.Message}, nil
}

func (p *Provider) ApproveMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.MergeRequestEvent, error) {
	transport, err := p.mutationTransport("review_mutation")
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	review, err := transport.CreatePullReview(ctx, ref, number, body)
	if err != nil {
		return platform.MergeRequestEvent{}, p.mapError(err)
	}
	return NormalizeMergeRequestEvents(p.kind, ref, number, nil, []ReviewDTO{review}, nil)[0], nil
}

func (p *Provider) EditMergeRequestContent(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	title *string,
	body *string,
) (platform.MergeRequest, error) {
	transport, err := p.mutationTransport("state_mutation")
	if err != nil {
		return platform.MergeRequest{}, err
	}
	pr, err := transport.EditPullRequest(ctx, ref, number, PullRequestMutationOptions{Title: title, Body: body})
	if err != nil {
		return platform.MergeRequest{}, p.mapError(err)
	}
	return NormalizePullRequest(ref, pr), nil
}

func (p *Provider) EditIssueContent(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	title *string,
	body *string,
) (platform.Issue, error) {
	transport, err := p.mutationTransport("state_mutation")
	if err != nil {
		return platform.Issue{}, err
	}
	issue, err := transport.EditIssue(ctx, ref, number, IssueMutationOptions{Title: title, Body: body})
	if err != nil {
		return platform.Issue{}, p.mapError(err)
	}
	return NormalizeIssue(ref, issue), nil
}

func (p *Provider) listRepositories(
	ctx context.Context,
	owner string,
	list func(context.Context, string, PageOptions) ([]RepositoryDTO, Page, error),
) ([]platform.Repository, error) {
	items, err := collectPages(ctx, func(opts PageOptions) ([]RepositoryDTO, Page, error) {
		return list(ctx, owner, opts)
	})
	if err != nil {
		return nil, p.mapError(err)
	}
	out := make([]platform.Repository, 0, len(items))
	for _, item := range items {
		repo, err := NormalizeRepository(p.kind, p.host, item)
		if err != nil {
			return nil, err
		}
		if repo.Ref.Owner == owner {
			out = append(out, repo)
		}
	}
	return out, nil
}

func (p *Provider) mutationTransport(capability string) (MutationTransport, error) {
	if !p.options.Mutations {
		return nil, platform.UnsupportedCapability(p.kind, p.host, capability)
	}
	transport, ok := p.transport.(MutationTransport)
	if !ok {
		return nil, platform.UnsupportedCapability(p.kind, p.host, capability)
	}
	return transport, nil
}

func (p *Provider) mapError(err error) error {
	return mapTransportError(p.kind, p.host, err)
}

func collectPages[T any](
	ctx context.Context,
	fetch func(PageOptions) ([]T, Page, error),
) ([]T, error) {
	var out []T
	page := 1
	seen := make(map[int]bool)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if seen[page] {
			return nil, fmt.Errorf("gitealike pagination did not advance: page %d repeated", page)
		}
		if len(seen) >= maxCollectedPages {
			return nil, fmt.Errorf("gitealike pagination exceeded %d pages", maxCollectedPages)
		}
		seen[page] = true
		items, next, err := fetch(PageOptions{Page: page, PageSize: defaultPageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		nextPage := NextPage(next.Next)
		if nextPage == 0 {
			return out, nil
		}
		if nextPage <= page {
			return nil, fmt.Errorf("gitealike pagination did not advance: next page %d after page %d", nextPage, page)
		}
		page = nextPage
	}
}

func applyRepositoryListOptions(
	repos []platform.Repository,
	opts platform.RepositoryListOptions,
) []platform.Repository {
	start := max(opts.Offset, 0)
	if start >= len(repos) {
		return nil
	}
	end := len(repos)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}
	return repos[start:end]
}

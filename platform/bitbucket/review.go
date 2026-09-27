package bitbucket

import (
	"context"
	"strconv"

	"go.kenn.io/forge/platform"
)

func (c *Client) ListMergeRequestReviewThreads(ctx context.Context, ref platform.RepoRef, number int) ([]platform.MergeRequestReviewThread, error) {
	opts, err := pullOptions(ref, number)
	if err != nil {
		return nil, err
	}
	api, err := c.sdk(ctx)
	if err != nil {
		return nil, err
	}
	first, err := decode[page[comment]](api.Repositories.PullRequests.GetComments(opts))
	if err != nil {
		return nil, err
	}
	rows, err := collect(ctx, c, first)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]comment, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	result := make([]platform.MergeRequestReviewThread, 0)
	for _, row := range rows {
		if row.Deleted {
			continue
		}
		root, err := commentRoot(row, byID)
		if err != nil {
			return nil, err
		}
		if root.Inline == nil {
			continue
		}
		rangeInfo := platform.DiffReviewLineRange{Path: root.Inline.Path, Side: "RIGHT", NewLine: root.Inline.To, OldLine: root.Inline.From}
		if root.Inline.To != nil {
			rangeInfo.Line = *root.Inline.To
			rangeInfo.StartLine = root.Inline.StartTo
		} else if root.Inline.From != nil {
			rangeInfo.Side = "LEFT"
			rangeInfo.Line = *root.Inline.From
			rangeInfo.StartLine = root.Inline.StartFrom
		}
		result = append(result, platform.MergeRequestReviewThread{Repo: ref, MergeRequestNumber: number, ProviderThreadID: strconv.FormatInt(root.ID, 10), ProviderCommentID: strconv.FormatInt(row.ID, 10), Body: row.Content.Raw, AuthorLogin: row.User.UUID, DirectURL: row.Links.HTML.Href, Range: rangeInfo, CreatedAt: row.Created.UTC(), UpdatedAt: row.Updated.UTC(), Resolved: root.Resolution != nil})
	}
	return result, nil
}

func commentRoot(row comment, byID map[int64]comment) (comment, error) {
	for depth := 0; row.Parent != nil; depth++ {
		parent, ok := byID[row.Parent.ID]
		if !ok || depth >= len(byID) {
			return comment{}, missing("comment parent")
		}
		row = parent
	}
	return row, nil
}

func (c *Client) ResolveDiffReviewThread(ctx context.Context, ref platform.RepoRef, number int, threadID string) error {
	return c.setThreadResolution(ctx, ref, number, threadID, "POST")
}

func (c *Client) UnresolveDiffReviewThread(ctx context.Context, ref platform.RepoRef, number int, threadID string) error {
	return c.setThreadResolution(ctx, ref, number, threadID, "DELETE")
}

func (c *Client) setThreadResolution(ctx context.Context, ref platform.RepoRef, number int, threadID, method string) error {
	id, err := strconv.ParseInt(threadID, 10, 64)
	if err != nil || id <= 0 {
		return &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "thread_id"}
	}
	target, err := repoURL(ref)
	if err != nil {
		return err
	}
	_, err = request[struct{}](ctx, c, method, target+"/pullrequests/"+strconv.Itoa(number)+"/comments/"+strconv.FormatInt(id, 10)+"/resolve", nil)
	return err
}

func (c *Client) RequestMergeRequestReviewers(ctx context.Context, ref platform.RepoRef, number int, users []string) ([]string, error) {
	return c.updateReviewers(ctx, ref, number, users, false)
}

func (c *Client) RemoveMergeRequestReviewers(ctx context.Context, ref platform.RepoRef, number int, users []string) ([]string, error) {
	return c.updateReviewers(ctx, ref, number, users, true)
}

func (c *Client) updateReviewers(ctx context.Context, ref platform.RepoRef, number int, users []string, remove bool) ([]string, error) {
	target, err := repoURL(ref)
	if err != nil {
		return nil, err
	}
	target += "/pullrequests/" + strconv.Itoa(number)
	p, err := request[pull](ctx, c, "GET", target, nil)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for _, id := range users {
		selected[id] = true
	}
	reviewers := make([]user, 0, len(p.Reviewers)+len(users))
	present := map[string]bool{}
	for _, r := range p.Reviewers {
		if remove && selected[r.UUID] {
			continue
		}
		reviewers = append(reviewers, user{UUID: r.UUID})
		present[r.UUID] = true
	}
	if !remove {
		for _, id := range users {
			if !present[id] {
				reviewers = append(reviewers, user{UUID: id})
				present[id] = true
			}
		}
	}
	if len(users) > 0 {
		p, err = request[pull](ctx, c, "PUT", target, map[string]any{"reviewers": reviewers})
		if err != nil {
			return nil, err
		}
		reviewers = p.Reviewers
	}
	result := make([]string, 0, len(reviewers))
	for _, r := range reviewers {
		result = append(result, r.UUID)
	}
	return result, nil
}

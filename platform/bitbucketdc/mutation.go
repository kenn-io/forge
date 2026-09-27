package bitbucketdc

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
)

func (c *Client) SetIssueState(context.Context, platform.RepoRef, int, string) (platform.Issue, error) {
	return platform.Issue{}, platform.UnsupportedCapability(c.Platform(), c.host, "issues")
}

func (c *Client) EditIssueContent(context.Context, platform.RepoRef, int, *string, *string) (platform.Issue, error) {
	return platform.Issue{}, platform.UnsupportedCapability(c.Platform(), c.host, "issues")
}

func (c *Client) SetMergeRequestState(ctx context.Context, ref platform.RepoRef, number int, state string) (platform.MergeRequest, error) {
	var action string
	switch state {
	case "open":
		action = "reopen"
	case "closed":
		action = "decline"
	default:
		return platform.MergeRequest{}, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "state"}
	}
	prior, err := c.getPull(ctx, ref, number)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	path, err := c.pullPath(ref, number)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	row, err := request[pull](ctx, c, http.MethodPost, path+"/"+action+"?version="+strconv.Itoa(prior.Version), nil)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	return row.normalize(ref)
}

func (c *Client) EditMergeRequestContent(ctx context.Context, ref platform.RepoRef, number int, title, body *string) (platform.MergeRequest, error) {
	prior, err := c.getPull(ctx, ref, number)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	if title != nil {
		prior.Title = *title
	}
	if body != nil {
		prior.Description = *body
	}
	path, err := c.pullPath(ref, number)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	row, err := request[pull](ctx, c, http.MethodPut, path, map[string]any{"version": prior.Version, "title": prior.Title, "description": prior.Description})
	if err != nil {
		return platform.MergeRequest{}, err
	}
	return row.normalize(ref)
}

func (c *Client) MergeMergeRequest(ctx context.Context, ref platform.RepoRef, number int, title, body, method, expected string) (platform.MergeResult, error) {
	strategy := "no-ff"
	switch method {
	case "", "merge":
	case "squash":
		strategy = "squash"
	case "rebase":
		strategy = "rebase-no-ff"
	default:
		return platform.MergeResult{}, platform.UnsupportedCapability(c.Platform(), c.host, "merge_method_"+method)
	}
	prior, err := c.getPull(ctx, ref, number)
	if err != nil {
		return platform.MergeResult{}, err
	}
	if expected != "" && expected != prior.From.LatestCommit {
		return platform.MergeResult{}, &platform.Error{Code: platform.ErrCodeStaleState}
	}
	path, err := c.pullPath(ref, number)
	if err != nil {
		return platform.MergeResult{}, err
	}
	row, err := request[pull](ctx, c, http.MethodPost, path+"/merge?version="+strconv.Itoa(prior.Version), map[string]any{"strategyId": strategy, "message": strings.TrimSpace(title + "\n\n" + body), "autoMerge": false})
	if err != nil {
		return platform.MergeResult{}, err
	}
	if row.State != "MERGED" {
		return platform.MergeResult{}, &platform.Error{Code: platform.ErrCodeConflict}
	}
	return platform.MergeResult{Merged: true, SHA: row.Properties.MergeCommit.ID}, nil
}

func (c *Client) ApproveMergeRequest(ctx context.Context, ref platform.RepoRef, number int, body, expected string) (platform.MergeRequestEvent, error) {
	if body != "" {
		return platform.MergeRequestEvent{}, platform.UnsupportedCapability(c.Platform(), c.host, "comments")
	}

	prior, err := c.getPull(ctx, ref, number)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	if expected != "" && prior.From.LatestCommit != expected {
		return platform.MergeRequestEvent{}, &platform.Error{Code: platform.ErrCodeStaleState}
	}
	path, err := c.pullPath(ref, number)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}

	row, err := request[participant](ctx, c, http.MethodPost, path+"/approve", nil)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	if expected != "" {
		latest, err := c.getPull(ctx, ref, number)
		if err != nil || latest.From.LatestCommit != expected || row.LastReviewedCommit != expected {
			_, revokeErr := request[participant](ctx, c, http.MethodDelete, path+"/approve", nil)
			revoked := "succeeded"
			if revokeErr != nil {
				revoked = "failed"
			}
			return platform.MergeRequestEvent{}, &platform.Error{Code: platform.ErrCodeStaleState, Err: err, Details: map[string]string{"revocation": revoked}}
		}
	}
	if row.User.ID <= 0 || row.LastReviewedCommit == "" {
		return platform.MergeRequestEvent{}, platform.ProviderContract(c.Platform(), c.host, "approval identity", errors.New("missing approving user ID or reviewed commit"))
	}
	now := time.Now().UTC()
	// The API returns participant state, not a distinct activity ID. The
	// event is scoped to the PR by storage; repeated approval of the same
	// commit must update that observation rather than create another event.
	id := "approval:" + strconv.FormatInt(row.User.ID, 10) + ":" + row.LastReviewedCommit
	return platform.MergeRequestEvent{Repo: ref, MergeRequestNumber: number, PlatformExternalID: id, DedupeKey: id, EventType: "review", Author: row.User.Name, Summary: "approved", CreatedAt: now}, nil
}

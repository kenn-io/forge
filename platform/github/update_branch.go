package github

import (
	"context"
	"errors"
	"net/http"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/platform"
)

// Explicit rejections are safe to retry after refresh. 422 covers stale heads,
// merge conflicts, and other eligibility failures.
var updateBranchRejections = map[int]platform.PlatformErrorCode{
	http.StatusForbidden:           platform.ErrCodePermissionDenied,
	http.StatusUnprocessableEntity: platform.ErrCodeConflict,
}

type branchUpdateAPI interface {
	UpdatePullRequestBranch(context.Context, string, string, int, string) error
}

func (p *Provider) UpdateBranch(ctx context.Context, ref platform.RepoRef, number int, expectedHeadSHA string) error {
	client, ok := p.client.(branchUpdateAPI)
	if !ok {
		return platform.UnsupportedCapability(p.Platform(), p.Host(), "update_branch")
	}
	return client.UpdatePullRequestBranch(ctx, ref.Owner, ref.Name, number, expectedHeadSHA)
}

func (c *Client) UpdatePullRequestBranch(ctx context.Context, owner, repo string, number int, expectedHeadSHA string) error {
	_, resp, err := c.writeGH().PullRequests.UpdateBranch(ctx, owner, repo, number, gh.UpdatePullRequestBranchRequest{
		ExpectedHeadSHA: &expectedHeadSHA,
	})
	c.trackWriteRate(resp)
	// go-github reports 202 as an error. It means queued, never retry the PUT.
	if _, accepted := errors.AsType[*gh.AcceptedError](err); accepted {
		return nil
	}
	if rejection, ok := errors.AsType[*gh.ErrorResponse](err); ok && rejection.Response != nil {
		if code, ok := updateBranchRejections[rejection.Response.StatusCode]; ok {
			return &platform.Error{Code: code, Provider: platform.KindGitHub, PlatformHost: c.platformHost, Err: err}
		}
	}
	return err
}

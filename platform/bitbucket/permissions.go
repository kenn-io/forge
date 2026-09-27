package bitbucket

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"go.kenn.io/forge/platform"
)

// Repository permission is a user grant, not proof of token scopes or branch
// restrictions. Bitbucket remains authoritative when the merge is attempted.
func (c *Client) observeMerge(ctx context.Context, repo *platform.Repository) error {
	repo.ViewerCanMerge = new(false)
	if c.source == nil {
		return nil
	}
	token, err := c.source.Token(ctx)
	if err != nil {
		return err
	}
	// Bearer credentials can be resource tokens. The user-scoped permission
	// endpoint does not establish their merge authority.
	if !strings.Contains(token, ":") {
		return nil
	}
	type permission struct {
		Permission string     `json:"permission"`
		Repository repository `json:"repository"`
	}
	target := apiURL + "/user/workspaces/" + url.PathEscape(repo.Ref.Owner) + "/permissions/repositories?pagelen=100"
	first, err := request[page[permission]](ctx, c, http.MethodGet, target, nil)
	if errors.Is(err, platform.ErrPermissionDenied) || errors.Is(err, platform.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := collect(ctx, c, first)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Repository.UUID == repo.PlatformExternalID {
			repo.ViewerCanMerge = new(row.Permission == "write" || row.Permission == "admin")
			break
		}
	}
	if repo.DefaultBranch == "" {
		return nil
	}
	target, err = repoURL(repo.Ref)
	if err != nil {
		return err
	}
	type strategies struct {
		Strategies []string `json:"merge_strategies"`
	}
	settings, err := request[strategies](ctx, c, http.MethodGet, target+"/refs/branches/"+url.PathEscape(repo.DefaultBranch), nil)
	if errors.Is(err, platform.ErrPermissionDenied) || errors.Is(err, platform.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if settings.Strategies == nil {
		return nil
	}
	repo.MergeSettings = &platform.RepositoryMergeSettings{}
	for _, strategy := range settings.Strategies {
		switch strategy {
		case "merge_commit":
			repo.MergeSettings.AllowMergeCommit = true
		case "squash":
			repo.MergeSettings.AllowSquashMerge = true
		case "rebase_fast_forward":
			repo.MergeSettings.AllowRebaseMerge = true
		}
	}
	return nil
}

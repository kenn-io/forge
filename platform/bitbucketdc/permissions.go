package bitbucketdc

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"go.kenn.io/forge/platform"
)

func (c *Client) observeMerge(ctx context.Context, repo *platform.Repository) error {
	repo.ViewerCanMerge = new(false)
	if c.source == nil {
		return nil
	}
	token, err := c.source.Token(ctx)
	if err != nil {
		return err
	}
	// Project/repository bearer tokens cannot merge. Only the documented
	// username:personal-access-token credential contract is supported here.
	if !strings.Contains(token, ":") {
		return nil
	}
	rows, err := pages[repository](ctx, c, "/rest/api/latest/repos?permission=REPO_WRITE&archived=ALL")
	if errors.Is(err, platform.ErrPermissionDenied) || errors.Is(err, platform.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, row := range rows {
		if strconv.FormatInt(row.ID, 10) == repo.PlatformExternalID {
			repo.ViewerCanMerge = new(true)
			break
		}
	}
	path, err := c.repoPath(repo.Ref)
	if err != nil {
		return err
	}
	type strategy struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	type settings struct {
		MergeConfig *struct {
			Strategies []strategy `json:"strategies"`
		} `json:"mergeConfig"`
	}
	value, err := request[settings](ctx, c, http.MethodGet, path+"/settings/pull-requests", nil)
	if errors.Is(err, platform.ErrPermissionDenied) || errors.Is(err, platform.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if value.MergeConfig == nil || value.MergeConfig.Strategies == nil {
		return nil
	}
	repo.MergeSettings = &platform.RepositoryMergeSettings{}
	for _, s := range value.MergeConfig.Strategies {
		if !s.Enabled {
			continue
		}
		switch s.ID {
		case "no-ff":
			repo.MergeSettings.AllowMergeCommit = true
		case "squash":
			repo.MergeSettings.AllowSquashMerge = true
		case "rebase-no-ff":
			repo.MergeSettings.AllowRebaseMerge = true
		}
	}
	return nil
}

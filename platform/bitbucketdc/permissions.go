package bitbucketdc

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
)

// Repository write permissions are shared by all lookups on this client and
// reused for five minutes. Failed pages are never cached.
func (c *Client) mergePermissions(ctx context.Context) (map[int64]struct{}, error) {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	if c.source == nil {
		return nil, nil
	}
	token, err := c.source.Token(ctx)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256([]byte(token))
	if fingerprint != c.permissionCredential {
		c.writableRepos = nil
		c.permissionCredential = fingerprint
	}
	// Project/repository bearer tokens cannot merge.
	if !strings.Contains(token, ":") {
		return nil, nil
	}
	if c.writableRepos != nil && time.Now().Before(c.permissionExpires) {
		return c.writableRepos, nil
	}
	rows, err := pages[repository](ctx, c, "/rest/api/latest/repos?permission=REPO_WRITE&archived=ALL")
	if errors.Is(err, platform.ErrPermissionDenied) || errors.Is(err, platform.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	writable := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		writable[row.ID] = struct{}{}
	}
	c.writableRepos = writable
	c.permissionExpires = time.Now().Add(5 * time.Minute)
	return writable, nil
}

func (c *Client) observeMerge(ctx context.Context, repo *platform.Repository, writable map[int64]struct{}) error {
	repo.ViewerCanMerge = new(false)
	if writable == nil {
		return nil
	}
	id, _ := repo.Ref.Key.ID()
	_, canMerge := writable[id]
	repo.ViewerCanMerge = new(canMerge)
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

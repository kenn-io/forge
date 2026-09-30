package bitbucket

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"uuid"

	"go.kenn.io/forge/platform"
)

type permissionSnapshot struct {
	expires time.Time
	grants  map[uuid.UUID]bool
}

// Cache only complete inventories for five minutes, scoped to the current
// credential and workspace. A token change discards the former user's grants.
func (c *Client) repositoryPermissions(ctx context.Context, workspace string) (map[uuid.UUID]bool, error) {
	if c.source == nil {
		return nil, nil
	}
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	token, err := c.source.Token(ctx)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256([]byte(token))
	if fingerprint != c.permissionCredential {
		c.permissionCredential = fingerprint
		c.permissionWorkspaces = make(map[string]permissionSnapshot)
	}
	// Bearer credentials can be resource tokens, outside this user-scoped API.
	if !strings.Contains(token, ":") {
		return nil, nil
	}
	if cached, ok := c.permissionWorkspaces[workspace]; ok && time.Now().Before(cached.expires) {
		return cached.grants, nil
	}
	type permission struct {
		Permission string     `json:"permission"`
		Repository repository `json:"repository"`
	}
	target := apiURL + "/user/workspaces/" + url.PathEscape(workspace) + "/permissions/repositories?pagelen=100"
	first, err := request[page[permission]](ctx, c, http.MethodGet, target, nil)
	if errors.Is(err, platform.ErrPermissionDenied) || errors.Is(err, platform.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := collect(ctx, c, first)
	if err != nil {
		return nil, err
	}
	grants := make(map[uuid.UUID]bool, len(rows))
	for _, row := range rows {
		id, err := uuid.Parse(row.Repository.UUID)
		if err != nil || id == uuid.Nil() {
			return nil, platform.ProviderContract(platform.KindBitbucket, platform.DefaultBitbucketHost, "repository permission", errors.New("repository UUID is missing"))
		}
		grants[id] = row.Permission == "write" || row.Permission == "admin"
	}
	c.permissionWorkspaces[workspace] = permissionSnapshot{expires: time.Now().Add(5 * time.Minute), grants: grants}
	return grants, nil
}

// Repository permission is a user grant, not proof of token scopes or branch
// restrictions. Bitbucket remains authoritative when the merge is attempted.
func (c *Client) observeMerge(ctx context.Context, repo *platform.Repository, grants map[uuid.UUID]bool) error {
	id, _ := repo.Ref.Key.UUID()
	repo.ViewerCanMerge = new(grants[id])
	if grants == nil {
		return nil
	}
	if repo.DefaultBranch == "" {
		return nil
	}
	target, err := repoURL(repo.Ref)
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

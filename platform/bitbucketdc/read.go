package bitbucketdc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.kenn.io/forge/platform"
)

func (c *Client) GetRepository(ctx context.Context, ref platform.RepoRef) (platform.Repository, error) {
	path, err := c.repoPath(ref)
	if err != nil {
		return platform.Repository{}, err
	}

	row, err := request[repository](ctx, c, http.MethodGet, path, nil)
	if ref.PlatformExternalID != "" && ((err == nil && strconv.FormatInt(row.ID, 10) != ref.PlatformExternalID) || errors.Is(err, platform.ErrNotFound)) {
		rows, lookupErr := pages[repository](ctx, c, "/rest/api/latest/repos?archived=ALL")
		if lookupErr != nil {
			return platform.Repository{}, lookupErr
		}
		found := false
		for _, candidate := range rows {
			if strconv.FormatInt(candidate.ID, 10) == ref.PlatformExternalID {
				row = candidate
				found = true
				break
			}
		}
		if !found {
			return platform.Repository{}, platform.ProviderContract(c.Platform(), c.host, "repository identity", errors.New("original repository is no longer accessible"))
		}
		err = nil
	}
	if err != nil {
		return platform.Repository{}, err
	}
	repo, err := row.normalize(c.host)
	if err != nil {
		return repo, err
	}
	path, err = c.repoPath(repo.Ref)
	if err != nil {
		return platform.Repository{}, err
	}
	branch, err := request[branch](ctx, c, http.MethodGet, path+"/default-branch", nil)
	if err != nil && !errors.Is(err, platform.ErrNotFound) {
		return platform.Repository{}, err
	}
	repo.DefaultBranch = branch.DisplayID
	repo.Ref.DefaultBranch = branch.DisplayID
	permissions, err := c.mergePermissions(ctx)
	if err != nil {
		return repo, err
	}
	err = c.observeMerge(ctx, &repo, permissions)
	return repo, err
}

func (c *Client) ListRepositories(ctx context.Context, owner string, opts platform.RepositoryListOptions) ([]platform.Repository, error) {
	path, err := c.repoPath(platform.RepoRef{Platform: c.Platform(), Host: c.host, Owner: owner, Name: "placeholder"})
	if err != nil {
		return nil, err
	}
	if opts.Offset < 0 || opts.Limit < 0 {
		return nil, &platform.Error{Code: platform.ErrCodeInvalidArgument}
	}
	path = path[:len(path)-len("/placeholder")]
	rows, err := pages[repository](ctx, c, path)
	if err != nil {
		return nil, err
	}
	if opts.Offset >= len(rows) {
		return []platform.Repository{}, nil
	}
	rows = rows[opts.Offset:]
	if opts.Limit > 0 && len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
	}
	permissions, err := c.mergePermissions(ctx)
	if err != nil {
		return nil, err
	}
	repos := make([]platform.Repository, 0, len(rows))
	for _, r := range rows {
		v, err := r.normalize(c.host)
		if err != nil {
			return nil, err
		}
		if err := c.observeMerge(ctx, &v, permissions); err != nil {
			return nil, err
		}
		repos = append(repos, v)
	}
	return repos, nil
}

func (c *Client) getPull(ctx context.Context, ref platform.RepoRef, number int) (pull, error) {
	path, err := c.pullPath(ref, number)
	if err != nil {
		return pull{}, err
	}
	return request[pull](ctx, c, http.MethodGet, path, nil)
}

func (c *Client) GetMergeRequest(ctx context.Context, ref platform.RepoRef, number int) (platform.MergeRequest, error) {
	row, err := c.getPull(ctx, ref, number)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	return row.normalize(ref)
}

func (c *Client) ListOpenMergeRequests(ctx context.Context, ref platform.RepoRef) ([]platform.MergeRequest, error) {
	path, err := c.repoPath(ref)
	if err != nil {
		return nil, err
	}
	rows, err := pages[pull](ctx, c, path+"/pull-requests?state=OPEN")
	if err != nil {
		return nil, err
	}
	out := make([]platform.MergeRequest, 0, len(rows))
	for _, r := range rows {
		p, err := r.normalize(ref)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (c *Client) ListMergeRequestEvents(context.Context, platform.RepoRef, int) ([]platform.MergeRequestEvent, error) {
	return nil, platform.UnsupportedCapability(c.Platform(), c.host, "comments")
}

func (c *Client) ListTags(ctx context.Context, ref platform.RepoRef) ([]platform.Tag, error) {
	path, err := c.repoPath(ref)
	if err != nil {
		return nil, err
	}
	rows, err := pages[branch](ctx, c, path+"/tags")
	if err != nil {
		return nil, err
	}
	out := make([]platform.Tag, 0, len(rows))
	for _, r := range rows {
		out = append(out, platform.Tag{Repo: ref, PlatformExternalID: r.ID, Name: r.DisplayID, SHA: r.LatestCommit})
	}
	return out, nil
}

func (c *Client) ListCIChecks(ctx context.Context, ref platform.RepoRef, sha string) ([]platform.CICheck, error) {
	_, err := c.repoPath(ref)
	if err != nil {
		return nil, err
	}
	type build struct {
		Key     string `json:"key"`
		Name    string `json:"name"`
		State   string `json:"state"`
		URL     string `json:"url"`
		Created int64  `json:"createdDate"`
		Updated int64  `json:"updatedDate"`
	}
	rows, err := pages[build](ctx, c, "/rest/build-status/latest/commits/"+url.PathEscape(sha))
	if err != nil {
		return nil, err
	}
	out := make([]platform.CICheck, 0, len(rows))
	for _, r := range rows {
		v := platform.CICheck{Repo: ref, PlatformExternalID: r.Key, Name: r.Name, URL: r.URL, Status: "queued", App: "Bitbucket", StartedAt: new(time.UnixMilli(r.Created).UTC())}
		switch r.State {
		case "INPROGRESS":
			v.Status = "in_progress"
		case "SUCCESSFUL":
			v.Status = "completed"
			v.Conclusion = "success"
		case "FAILED":
			v.Status = "completed"
			v.Conclusion = "failure"
		case "CANCELLED":
			v.Status = "completed"
			v.Conclusion = "cancelled"
		}
		if v.Status == "completed" {
			v.CompletedAt = new(time.UnixMilli(r.Updated).UTC())
		}
		out = append(out, v)
	}
	return out, nil
}

package bitbucket

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"uuid"

	bitbucket "github.com/ktrysmt/go-bitbucket"
	"go.kenn.io/forge/platform"
)

func (c *Client) GetRepository(ctx context.Context, ref platform.RepoRef) (platform.Repository, error) {
	owner, name, err := repoParts(ref)
	if err != nil {
		return platform.Repository{}, err
	}
	if ref.BitbucketRepositoryUUID != uuid.Nil() {
		// Bitbucket's empty workspace placeholder resolves a UUID after a move or rename.
		owner = "{}"
	}
	target := apiURL + "/repositories/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	r, err := request[repository](ctx, c, http.MethodGet, target, nil)
	if err != nil {
		return platform.Repository{}, err
	}
	repo, err := r.normalize()
	if err != nil {
		return repo, err
	}
	if ref.BitbucketRepositoryUUID != uuid.Nil() && repo.Ref.BitbucketRepositoryUUID != ref.BitbucketRepositoryUUID {
		return platform.Repository{}, platform.ProviderContract(c.Platform(), ref.Host, "repository identity", errors.New("repository UUID does not match requested identity"))
	}
	grants, err := c.repositoryPermissions(ctx, repo.Ref.Owner)
	if err != nil {
		return repo, err
	}
	err = c.observeMerge(ctx, &repo, grants)
	return repo, err
}

func (c *Client) ListRepositories(ctx context.Context, owner string, opts platform.RepositoryListOptions) ([]platform.Repository, error) {
	if owner == "" || strings.Contains(owner, "/") || opts.Offset < 0 || opts.Limit < 0 {
		return nil, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "owner"}
	}
	// The SDK's RepositoriesRes drops the next URL. Read the wire page so
	// workspace inventories cannot silently stop at the first page.
	first, err := request[page[repository]](ctx, c, http.MethodGet, apiURL+"/repositories/"+url.PathEscape(owner)+"?pagelen=100", nil)
	if err != nil {
		return nil, err
	}
	rows, err := collect(ctx, c, first)
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
	grants, err := c.repositoryPermissions(ctx, owner)
	if err != nil {
		return nil, err
	}
	result := make([]platform.Repository, 0, len(rows))
	for _, row := range rows {
		r, err := row.normalize()
		if err != nil {
			return nil, err
		}
		if err := c.observeMerge(ctx, &r, grants); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, nil
}

func (c *Client) GetMergeRequest(ctx context.Context, ref platform.RepoRef, number int) (platform.MergeRequest, error) {
	opts, err := pullOptions(ref, number)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	api, err := c.sdk(ctx)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	p, err := decode[pull](api.Repositories.PullRequests.Get(opts))
	if err != nil {
		return platform.MergeRequest{}, err
	}
	return c.normalizePull(ctx, ref, p)
}

func (c *Client) ListOpenMergeRequests(ctx context.Context, ref platform.RepoRef) ([]platform.MergeRequest, error) {
	opts, err := pullOptions(ref, 0)
	if err != nil {
		return nil, err
	}
	opts.States = []string{"OPEN"}
	api, err := c.sdk(ctx)
	if err != nil {
		return nil, err
	}
	first, err := decode[page[pull]](api.Repositories.PullRequests.List(opts))
	if err != nil {
		return nil, err
	}
	rows, err := collect(ctx, c, first)
	if err != nil {
		return nil, err
	}
	result := make([]platform.MergeRequest, 0, len(rows))
	for _, row := range rows {
		p, err := c.normalizePull(ctx, ref, row)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, nil
}

// Cloud embeds abbreviated commit hashes in pull responses. Resolve them before
// exposing them to workspace and diff consumers, which compare full object IDs.
func (c *Client) normalizePull(ctx context.Context, ref platform.RepoRef, p pull) (platform.MergeRequest, error) {
	for _, b := range []*branch{&p.Source, &p.Destination} {
		if b.Commit.Hash == "" || len(b.Commit.Hash) == 40 {
			continue
		}
		commitRef := ref
		if owner, name, ok := strings.Cut(b.Repository.FullName, "/"); ok {
			commitRef.Owner, commitRef.Name = owner, name
			if b.Repository.UUID != "" {
				id, err := uuid.Parse(b.Repository.UUID)
				if err != nil || id == uuid.Nil() {
					return platform.MergeRequest{}, missing("repository identity")
				}
				commitRef.BitbucketRepositoryUUID = id
			}
		}
		target, err := repoURL(commitRef)
		if err != nil {
			return platform.MergeRequest{}, err
		}
		resolved, err := request[commit](ctx, c, http.MethodGet, target+"/commit/"+url.PathEscape(b.Commit.Hash)+"?fields=hash", nil)
		if err != nil {
			return platform.MergeRequest{}, err
		}
		if len(resolved.Hash) != 40 {
			return platform.MergeRequest{}, missing("full commit hash")
		}
		b.Commit = resolved
	}
	return p.normalize(ref)
}

func (c *Client) ListMergeRequestEvents(ctx context.Context, ref platform.RepoRef, number int) ([]platform.MergeRequestEvent, error) {
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
	result := make([]platform.MergeRequestEvent, 0, len(rows))
	byID := make(map[int64]comment, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	for _, row := range rows {
		if row.Deleted {
			continue
		}
		root, err := commentRoot(row, byID)
		if err != nil {
			return nil, err
		}
		if root.Inline == nil {
			event := row.pullEvent(ref, number)
			event.ThreadID = strconv.FormatInt(root.ID, 10)
			result = append(result, event)
		}
	}
	return result, nil
}

func (c *Client) GetIssue(ctx context.Context, ref platform.RepoRef, number int) (platform.Issue, error) {
	opts, err := issueOptions(ref, number)
	if err != nil {
		return platform.Issue{}, err
	}
	api, err := c.sdk(ctx)
	if err != nil {
		return platform.Issue{}, err
	}
	i, err := decode[issue](api.Repositories.Issues.Get(opts))
	if err != nil {
		return platform.Issue{}, err
	}
	return i.normalize(ref)
}

func (c *Client) ListOpenIssues(ctx context.Context, ref platform.RepoRef) ([]platform.Issue, error) {
	r, err := c.GetRepository(ctx, ref)
	if err != nil {
		return nil, err
	}
	if enabled, known := r.FeatureEnabled(platform.RepositoryFeatureIssues); known && !enabled {
		return nil, platform.RepositoryFeatureDisabled(c.Platform(), c.Host(), platform.RepositoryFeatureIssues, nil)
	}
	opts, err := issueOptions(ref, 0)
	if err != nil {
		return nil, err
	}
	opts.Query = `state = "new" OR state = "open" OR state = "on hold"`
	api, err := c.sdk(ctx)
	if err != nil {
		return nil, err
	}
	first, err := decode[page[issue]](api.Repositories.Issues.Gets(opts))
	if err != nil {
		return nil, err
	}
	rows, err := collect(ctx, c, first)
	if err != nil {
		return nil, err
	}
	result := make([]platform.Issue, 0, len(rows))
	for _, row := range rows {
		i, err := row.normalize(ref)
		if err != nil {
			return nil, err
		}
		result = append(result, i)
	}
	return result, nil
}

func (c *Client) ListIssueEvents(ctx context.Context, ref platform.RepoRef, number int) ([]platform.IssueEvent, error) {
	opts, err := issueOptions(ref, number)
	if err != nil {
		return nil, err
	}
	api, err := c.sdk(ctx)
	if err != nil {
		return nil, err
	}
	first, err := decode[page[comment]](api.Repositories.Issues.GetComments(&bitbucket.IssueCommentsOptions{IssuesOptions: *opts}))
	if err != nil {
		return nil, err
	}
	rows, err := collect(ctx, c, first)
	if err != nil {
		return nil, err
	}
	result := make([]platform.IssueEvent, 0, len(rows))
	for _, row := range rows {
		if !row.Deleted {
			result = append(result, row.issueEvent(ref, number))
		}
	}
	return result, nil
}

func (c *Client) AuthenticatedUser(ctx context.Context, ref platform.RepoRef) (string, error) {
	if _, _, err := repoParts(ref); err != nil {
		return "", err
	}
	u, err := request[user](ctx, c, http.MethodGet, apiURL+"/user", nil)
	if err != nil {
		return "", err
	}
	if u.UUID == "" {
		return "", missing("user uuid")
	}
	return u.UUID, nil
}

func (c *Client) ViewerAuthoredMergeRequest(ctx context.Context, mr platform.MergeRequest) (bool, error) {
	viewer, err := c.AuthenticatedUser(ctx, mr.Repo)
	return viewer == mr.Author, err
}

func (c *Client) ListTags(ctx context.Context, ref platform.RepoRef) ([]platform.Tag, error) {
	target, err := repoURL(ref)
	if err != nil {
		return nil, err
	}
	type tag struct {
		Name   string `json:"name"`
		Target commit `json:"target"`
		Links  links  `json:"links"`
	}
	first, err := request[page[tag]](ctx, c, http.MethodGet, target+"/refs/tags?pagelen=100", nil)
	if err != nil {
		return nil, err
	}
	rows, err := collect(ctx, c, first)
	if err != nil {
		return nil, err
	}
	result := make([]platform.Tag, 0, len(rows))
	for _, row := range rows {
		result = append(result, platform.Tag{Repo: ref, PlatformExternalID: row.Name, Name: row.Name, SHA: row.Target.Hash, URL: row.Links.HTML.Href})
	}
	return result, nil
}

func (c *Client) ListCIChecks(ctx context.Context, ref platform.RepoRef, sha string) ([]platform.CICheck, error) {
	owner, name, err := repoParts(ref)
	if err != nil {
		return nil, err
	}
	api, err := c.sdk(ctx)
	if err != nil {
		return nil, err
	}
	type status struct {
		Key     string    `json:"key"`
		Name    string    `json:"name"`
		State   string    `json:"state"`
		URL     string    `json:"url"`
		Created time.Time `json:"created_on"`
		Updated time.Time `json:"updated_on"`
	}
	first, err := decode[page[status]](api.Repositories.Commits.GetCommitStatuses(&bitbucket.CommitsOptions{Owner: owner, RepoSlug: name, Revision: url.PathEscape(sha)}))
	if err != nil {
		return nil, err
	}
	rows, err := collect(ctx, c, first)
	if err != nil {
		return nil, err
	}
	result := make([]platform.CICheck, 0, len(rows))
	for _, row := range rows {
		check := platform.CICheck{Repo: ref, PlatformExternalID: row.Key, Name: row.Name, URL: row.URL, Status: "queued", App: "Bitbucket", StartedAt: new(row.Created.UTC())}
		switch row.State {
		case "INPROGRESS":
			check.Status = "in_progress"
		case "SUCCESSFUL":
			check.Status = "completed"
			check.Conclusion = "success"
		case "FAILED", "ERROR":
			check.Status = "completed"
			check.Conclusion = "failure"
		case "STOPPED":
			check.Status = "completed"
			check.Conclusion = "cancelled"
		}
		if check.Status == "completed" {
			check.CompletedAt = new(row.Updated.UTC())
		}
		result = append(result, check)
	}
	return result, nil
}

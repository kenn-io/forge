package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v93/github"
)

// GetRepository reads repository metadata with the sync credential so
// app-token-only hosts keep working, then, on split-credential hosts,
// overlays the viewer-specific permissions block from the user's
// credential: it feeds viewer_can_merge, and the app installation
// token would report the read-only app's permissions instead of the
// user's. A failed overlay (missing or exhausted PAT) clears the
// permissions to "unknown" rather than keeping the app's, and never
// fails the metadata read sync depends on.
func (c *Client) GetRepository(
	ctx context.Context, owner, repo string,
) (*gh.Repository, error) {
	r, resp, err := c.gh.Repositories.Get(ctx, owner, repo)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf("getting repository %s/%s: %w", owner, repo, err)
	}
	return c.withViewerOverlay(ctx, r)
}

// GetRepositoryByID reads a repository by its integer ID, so a renamed or
// transferred repository resolves to its current route. owner, the last known
// owner, only selects credentials; the ID alone decides the repository.
func (c *Client) GetRepositoryByID(
	ctx context.Context, owner string, id int64,
) (*gh.Repository, error) {
	if c.ownerContext != nil {
		ctx = c.ownerContext(ctx, owner)
	}
	r, resp, err := c.gh.Repositories.GetByID(ctx, id)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf("getting repository %d: %w", id, err)
	}
	return c.withViewerOverlay(ctx, r)
}

func (c *Client) withViewerOverlay(
	ctx context.Context, r *gh.Repository,
) (*gh.Repository, error) {
	owner, repo := r.GetOwner().GetLogin(), r.GetName()
	if !c.splitAuthActive() {
		return r, nil
	}
	if c.writesDisabled {
		r.Permissions = nil
		return r, nil
	}
	if c.readOnlyContext != nil && c.readOnlyContext(ctx) {
		// Archive requests hold admission and provider-work leases for the
		// read identity only, so they must not spend the write credential on
		// the viewer overlay. Archive classification does not need viewer
		// permissions; clear the app's instead of persisting the wrong
		// viewer's.
		r.Permissions = nil
		return r, nil
	}
	overlay, err := c.viewerRepoOverlay(ctx, owner, repo)
	if err != nil {
		c.warn(
			"viewer permission refresh failed; merge permission unknown until it succeeds",
			"repo", owner+"/"+repo, "err", err,
		)
		r.Permissions = nil
		return r, nil
	}
	if overlay.permissions != nil {
		permissions := *overlay.permissions
		r.Permissions = &permissions
	} else {
		r.Permissions = nil
	}
	if overlay.mergeSettings {
		r.AllowSquashMerge = new(overlay.allowSquash)
		r.AllowMergeCommit = new(overlay.allowMerge)
		r.AllowRebaseMerge = new(overlay.allowRebase)
	}
	return r, nil
}

// viewerRepoOverlay returns the user's view of a repository. Sync reads every
// tracked repository on each pass, so the overlay is revalidated with its ETag:
// GitHub answers an unchanged repository with a 304 that costs no rate limit,
// and the cached overlay stays current without spending the user's credential.
func (c *Client) viewerRepoOverlay(
	ctx context.Context, owner, repo string,
) (viewerRepoOverlay, error) {
	key := strings.ToLower(owner) + "/" + strings.ToLower(repo)
	if c.auth.CredentialKey != nil {
		key = c.auth.CredentialKey() + "\x00" + key
	}
	c.viewerRepoMu.Lock()
	cached, ok := c.viewerRepos[key]
	c.viewerRepoMu.Unlock()
	req, err := c.writeGH().NewRequest(
		ctx, http.MethodGet, fmt.Sprintf("repos/%v/%v", owner, repo), nil,
	)
	if err != nil {
		return viewerRepoOverlay{}, err
	}
	if ok && cached.etag != "" {
		req.Header.Set("If-None-Match", cached.etag)
	}
	viewerRepo := new(gh.Repository)
	resp, err := c.writeGH().Do(req, viewerRepo)
	c.trackWriteRate(resp)
	if err != nil {
		if ok && IsNotModified(err) {
			return cached, nil
		}
		return viewerRepoOverlay{}, err
	}
	overlay := viewerRepoOverlay{
		permissions:   viewerRepo.Permissions,
		mergeSettings: MergeSettingsComplete(viewerRepo),
		allowSquash:   viewerRepo.GetAllowSquashMerge(),
		allowMerge:    viewerRepo.GetAllowMergeCommit(),
		allowRebase:   viewerRepo.GetAllowRebaseMerge(),
	}
	if resp.Response != nil {
		overlay.etag = resp.Header.Get("ETag")
	}
	c.viewerRepoMu.Lock()
	if c.viewerRepos == nil {
		c.viewerRepos = map[string]viewerRepoOverlay{}
	}
	c.viewerRepos[key] = overlay
	c.viewerRepoMu.Unlock()
	return overlay, nil
}

func MergeSettingsComplete(repo *gh.Repository) bool {
	return repo != nil &&
		repo.AllowSquashMerge != nil &&
		repo.AllowMergeCommit != nil &&
		repo.AllowRebaseMerge != nil
}

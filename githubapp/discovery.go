package githubapp

import (
	"context"
	"fmt"
	"math"
	"net/http"
)

// InstallationPage is one observation, not a snapshot of all installations.
// NextPage is zero after a short page. A full page requires another read,
// possibly empty. Callers own total request limits and stopping between pages.
type InstallationPage struct {
	Installations []Installation
	NextPage      int
}

// Repository is an installation-visible repository. ID is stable within the
// GitHub instance; names, owner and default branch are observed metadata.
type Repository struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	FullName      string  `json:"full_name"`
	Owner         Account `json:"owner"`
	DefaultBranch string  `json:"default_branch"`
	Private       bool    `json:"private"`
}

// RepositoryPage has the same continuation contract as InstallationPage.
// Discovery does not grant access or select repositories on the caller's behalf.
type RepositoryPage struct {
	Repositories []Repository
	NextPage     int
}

// ListInstallationsPage reads one page using an App JWT; it never mints an
// installation token. page starts at one and perPage must be between 1 and 100.
func (c *Client) ListInstallationsPage(
	ctx context.Context, appJWT string, page, perPage int,
) (InstallationPage, error) {
	path, err := discoveryPath("/app/installations", page, perPage)
	if err != nil {
		return InstallationPage{}, err
	}
	var items []Installation
	if err := c.do(ctx, http.MethodGet, path, appJWT, nil, &items); err != nil {
		return InstallationPage{}, fmt.Errorf("listing app installations: %w", err)
	}
	if items == nil || len(items) > perPage {
		return InstallationPage{}, fmt.Errorf("missing or oversized installation page")
	}
	result := InstallationPage{Installations: items}
	if len(items) == perPage {
		result.NextPage = page + 1
	}
	return result, nil
}

// ListInstallationRepositoriesPage reads one page with an installation token,
// not an App JWT. page starts at one; perPage must be between 1 and 100.
func (c *Client) ListInstallationRepositoriesPage(
	ctx context.Context, installationToken string, page, perPage int,
) (RepositoryPage, error) {
	path, err := discoveryPath("/installation/repositories", page, perPage)
	if err != nil {
		return RepositoryPage{}, err
	}
	var out struct {
		Repositories []Repository `json:"repositories"`
	}
	if err := c.do(ctx, http.MethodGet, path, installationToken, nil, &out); err != nil {
		return RepositoryPage{}, fmt.Errorf("listing installation repositories: %w", err)
	}
	if out.Repositories == nil || len(out.Repositories) > perPage {
		return RepositoryPage{}, fmt.Errorf("missing or oversized repository page")
	}
	result := RepositoryPage{Repositories: out.Repositories}
	if len(out.Repositories) == perPage {
		result.NextPage = page + 1
	}
	return result, nil
}

func discoveryPath(path string, page, perPage int) (string, error) {
	if page < 1 || page == math.MaxInt || perPage < 1 || perPage > 100 {
		return "", fmt.Errorf("page must be positive and incrementable; perPage must be between 1 and 100")
	}
	return fmt.Sprintf("%s?per_page=%d&page=%d", path, perPage, page), nil
}

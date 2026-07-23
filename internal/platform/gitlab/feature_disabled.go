package gitlab

import (
	"context"
	"errors"
	"net/http"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"go.kenn.io/middleman/internal/platform"
)

func (c *Client) repositoryFeatureError(
	ctx context.Context,
	ref platform.RepoRef,
	feature string,
	capability string,
	err error,
) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return c.mapGitLabError(capability, err)
	}

	var responseErr *gitlab.ErrorResponse
	if !errors.As(err, &responseErr) || responseErr == nil ||
		(!responseErr.HasStatusCode(http.StatusForbidden) &&
			!responseErr.HasStatusCode(http.StatusNotFound) &&
			!responseErr.HasStatusCode(http.StatusGone)) {
		return c.mapGitLabError(capability, err)
	}

	repository, lookupErr := c.GetRepository(ctx, ref)
	if lookupErr != nil {
		return c.mapGitLabError(capability, err)
	}
	enabled, known := repository.FeatureEnabled(feature)
	if known && !enabled {
		return platform.RepositoryFeatureDisabled(platform.KindGitLab, c.host, feature, err)
	}
	return c.mapGitLabError(capability, err)
}

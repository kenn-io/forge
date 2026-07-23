package gitealike

import (
	"context"
	"errors"
	"net/http"

	"go.kenn.io/middleman/internal/platform"
)

func (p *Provider) repositoryFeatureError(
	ctx context.Context,
	ref platform.RepoRef,
	feature string,
	err error,
) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return p.mapError(err)
	}

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr == nil ||
		(httpErr.StatusCode != http.StatusForbidden &&
			httpErr.StatusCode != http.StatusNotFound &&
			httpErr.StatusCode != http.StatusGone) {
		return p.mapError(err)
	}

	dto, lookupErr := p.transport.GetRepository(ctx, ref.Owner, ref.Name)
	if lookupErr != nil {
		return p.mapError(err)
	}
	repository, normalizeErr := NormalizeRepository(p.kind, p.host, dto)
	if normalizeErr != nil {
		return p.mapError(err)
	}
	enabled, known := repository.FeatureEnabled(feature)
	if known && !enabled {
		return platform.RepositoryFeatureDisabled(p.kind, p.host, feature, err)
	}
	return p.mapError(err)
}

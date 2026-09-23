package pullapi

import (
	"context"
	"fmt"
	"strings"

	"go.kenn.io/forge/internal/ghshim"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
)

type ghShimInput struct{ Body ghshim.Query }
type ghShimResponse struct {
	Handled bool   `json:"handled"`
	Output  string `json:"output"`
	Reason  string `json:"reason"`
}
type ghShimOutput = httpapi.BodyOutput[ghShimResponse]

func (s *Handler) ghShim(ctx context.Context, input *ghShimInput) (*ghShimOutput, error) {
	fallback := func(reason string) (*ghShimOutput, error) {
		return &ghShimOutput{Body: ghShimResponse{Reason: reason}}, nil
	}
	q := input.Body
	if !q.Valid() {
		return fallback("unsupported")
	}
	if s.syncer == nil {
		return fallback("provider_unavailable")
	}
	repo, err := s.resolver.LookupRoute(ctx, "github", q.Host, q.Owner, q.Repo)
	if err != nil {
		return fallback("untracked")
	}
	refs, err := s.syncer.ConfiguredRepositories(ctx)
	if err != nil {
		return fallback("provider_unavailable")
	}
	tracked := false
	for _, ref := range refs {
		if ref.Platform == platform.KindGitHub && strings.EqualFold(ref.Host, q.Host) && strings.EqualFold(ref.Owner, q.Owner) && strings.EqualFold(ref.Name, q.Repo) {
			tracked = true
			break
		}
	}
	if !tracked {
		return fallback("untracked")
	}
	fence, found, err := s.resolver.CaptureRepositoryRouteFence(ctx, *repo)
	if err != nil || !found {
		return fallback("untracked")
	}
	client, err := s.syncer.DirectClientForHost(q.Host)
	if err != nil {
		return fallback("provider_unavailable")
	}
	// Cache identity includes the stable repository and current route generation.
	identity := fmt.Sprintf("%d/%v", repo.ID, fence)
	output, err := s.ghCache.Query(ctx, client, identity, q)
	if err != nil {
		return fallback("hydration_failed")
	}
	matches, err := s.resolver.RepositoryRouteFenceMatches(ctx, *repo, fence)
	if err != nil || !matches {
		return fallback("route_changed")
	}
	return &ghShimOutput{Body: ghShimResponse{Handled: true, Output: string(output), Reason: "served"}}, nil
}

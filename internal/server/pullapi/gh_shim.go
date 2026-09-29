package pullapi

import (
	"context"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ghshim"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
)

type (
	ghShimInput    struct{ Body ghshim.Query }
	ghShimResponse struct {
		Handled bool   `json:"handled"`
		Output  string `json:"output"`
		Reason  string `json:"reason"`
	}
)
type ghShimOutput = httpapi.BodyOutput[ghShimResponse]

func (s *Handler) ghShim(ctx context.Context, input *ghShimInput) (*ghShimOutput, error) {
	fallback := func(reason string) (*ghShimOutput, error) {
		return &ghShimOutput{Body: ghShimResponse{Reason: reason}}, nil
	}
	q := input.Body
	if !q.Valid() {
		return fallback("unsupported")
	}
	repo, err := s.resolver.LookupRoute(ctx, "github", q.Host, q.Owner, q.Repo)
	if err != nil {
		return fallback("untracked")
	}
	tracked := false
	if s.syncer != nil {
		refs, err := s.syncer.ConfiguredRepositories(ctx)
		if err != nil {
			return fallback("provider_unavailable")
		}
		for _, ref := range refs {
			// Routes are mutable; only the stable identity proves the routed
			// row is the configured repository.
			if ref.Identity() == repo.Identity() {
				tracked = true
				break
			}
		}
	} else {
		// Spokes retain repository config and data but have no provider syncer.
		candidate := ghclient.RepoRef{Platform: platform.KindGitHub, PlatformHost: repo.PlatformHost, PlatformRepoID: repo.PlatformRepoID, Owner: repo.Owner, Name: repo.Name, RepoPath: repo.RepoPath}
		for _, configured := range s.ConfigSnapshot().Repositories {
			for _, ref := range ghclient.FallbackConfiguredRepoRefs([]ghclient.RepoRef{candidate}, configured) {
				if ref.Identity() == repo.Identity() {
					tracked = true
				}
			}
		}
	}
	if !tracked {
		return fallback("untracked")
	}
	output, err := ghshim.Read(ctx, s.db, repo.Repo, q)
	if err != nil && q.Command == "view" && s.providerSource != nil {
		// Use the existing provider read path; the hub needs no shim endpoint.
		detail, readErr := s.providerSource.GetPull(ctx, ItemIdentity{Provider: "github", PlatformHost: q.Host, Owner: q.Owner, Name: q.Repo, Number: q.Number})
		// The hub resolves owner/name itself; serve only the repository this
		// spoke verified as tracked.
		if readErr == nil && detail.MergeRequest != nil && detail.Repo.Identity() == repo.Identity() {
			output, err = ghshim.Encode(q, []db.MergeRequest{*detail.MergeRequest})
		}
	}
	if err != nil {
		return fallback("data_unavailable")
	}
	return &ghShimOutput{Body: ghShimResponse{Handled: true, Output: string(output), Reason: "served"}}, nil
}

package server

import (
	"context"
	"fmt"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/operationapi"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

func (s *Server) MergeRequestAuthoredByViewer(
	ctx context.Context,
	repo db.Repo,
	mr db.MergeRequest,
) bool {
	if s == nil || s.syncer == nil || s.syncer.Registry() == nil {
		return false
	}
	resolver, err := s.syncer.Registry().MergeRequestViewerResolver(
		httpapi.ProviderKind(repo), httpapi.ProviderHost(repo),
	)
	if err != nil {
		return false
	}
	authored, err := resolver.ViewerAuthoredMergeRequest(ctx, platform.MergeRequest{
		Repo:   httpapi.PlatformRepoRef(repo),
		Number: mr.Number,
		Author: mr.Author,
	})
	if err != nil {
		return false
	}
	return authored
}

func (s *Server) OperationRateLimitBuckets(repo db.Repo, op operationapi.OperationDescriptor) ([]operationapi.ApiBucket, bool) {
	if s != nil && s.syncer != nil && s.syncer.Registry() != nil {
		provider, err := s.syncer.Registry().Provider(httpapi.ProviderKind(repo), httpapi.ProviderHost(repo))
		if err == nil {
			if reporter, ok := provider.(platform.OperationRateLimitReporter); ok {
				if buckets, ok := reporter.OperationRateLimitBuckets(platform.OperationName(op.Name)); ok {
					if converted, ok := operationapi.ApiBucketsFromPlatform(buckets); ok {
						return converted, true
					}
					return nil, false
				}
			}
		}
	}
	return op.RateLimitBuckets(), true
}

// mutationRateLimitedReason resolves the rate state gating a write
// operation. Every operation in httpapi.RepoOperations is a mutation, and
// mutations authenticate with the host's write credential (the user's
// PAT when a GitHub App handles sync reads). When the host has a
// dedicated write tracker for the bucket the operation consumes, that
// tracker alone decides: an exhausted app budget must not disable
// PAT-backed writes, and PAT exhaustion must surface even though sync
// reads still flow. Hosts without write trackers share one credential
// across reads and writes, so the sync bucket tracker keeps gating.
func (s *Server) MutationRateLimitedReason(
	repo db.Repo, bucket operationapi.ApiBucket,
) operationapi.RateLimitAvailability {
	if s == nil || s.syncer == nil {
		return operationapi.RateLimitAvailability{}
	}
	host := httpapi.ProviderHost(repo)
	apiType := "rest"
	if bucket == operationapi.ApiBucketGraphQL {
		apiType = "graphql"
	} else if bucket != operationapi.ApiBucketREST {
		return operationapi.RateLimitAvailability{}
	}
	ref := operationapi.OperationRepoRef(repo)
	if wt, ok := s.syncer.WriteRateTrackerForRepo(ref, apiType); ok && wt != nil {
		if wt.IsPaused() {
			return operationapi.FormatRateLimit(host, wt.ResetAt())
		}
		return operationapi.RateLimitAvailability{}
	}
	return s.rateLimitedReason(repo, bucket)
}

func (s *Server) rateLimitedReason(repo db.Repo, bucket operationapi.ApiBucket) operationapi.RateLimitAvailability {
	if s == nil || s.syncer == nil {
		return operationapi.RateLimitAvailability{}
	}
	host := httpapi.ProviderHost(repo)
	apiType := "rest"
	if bucket == operationapi.ApiBucketGraphQL {
		apiType = "graphql"
	} else if bucket != operationapi.ApiBucketREST {
		return operationapi.RateLimitAvailability{}
	}
	if rt, ok := s.syncer.RateTrackerForRepo(operationapi.OperationRepoRef(repo), apiType); ok &&
		rt != nil && rt.IsPaused() {
		return operationapi.FormatRateLimit(host, rt.ResetAt())
	}
	return operationapi.RateLimitAvailability{}
}

// writeCredentialGateForRepo reports why mutations against repo's
// host cannot authenticate, or the zero gate when they can. Only
// split hosts can be in this state: when a GitHub App serves sync
// reads, mutations deliberately skip the app candidate so writes stay
// attributed to the user, and a host configured with only the app
// would accept the operation in the UI and then fail auth at request
// time. Probing the mutation-marked chain surfaces that before the UI
// offers the action. Shared-credential hosts are exempt — sync reads
// already exercise the same credential mutations would use.
//
// Results are cached per (host, canonical chain): a config reload
// that re-points the host's credential chain misses the cache
// immediately, while the TTL covers changes behind an unchanged chain
// (a token file rewritten in place, a gh CLI login). Cold-cache
// probes are single-flighted so concurrent list requests share one
// resolution instead of each shelling out to the gh CLI.
func (s *Server) WriteCredentialGateForRepo(repo db.Repo) operationapi.WriteCredentialGate {
	if s == nil {
		return operationapi.WriteCredentialGate{}
	}
	if httpapi.ProviderKind(repo) == platform.KindGitHub && s.syncer != nil {
		ref := operationapi.OperationRepoRef(repo)
		if operationapi.RouterIdentityRequired(s.syncer, ref) {
			if _, ok := s.syncer.WriteIdentityForRepo(ref); !ok {
				return operationapi.WriteCredentialGate{
					Code: operationapi.AvailabilityCodeMissingWriteCredential,
					Reason: fmt.Sprintf(
						"No startup-resolved user credential for writes on %s. Configure a PAT or gh CLI auth, then restart kenn-forge.",
						httpapi.ProviderHost(repo),
					),
				}
			}
			cacheKey, err := s.syncer.WriteCredentialProbeKeyForRepo(ref)
			if err != nil {
				return operationapi.WriteCredentialGateFromError(httpapi.ProviderHost(repo), err)
			}
			if cacheKey == "" {
				return operationapi.WriteCredentialGate{}
			}
			return s.operationapi.CachedWriteCredentialGate(
				"routed\x00"+cacheKey,
				func() operationapi.WriteCredentialGate {
					parent := s.bgCtx
					if parent == nil {
						parent = context.Background()
					}
					ctx, cancel := context.WithTimeout(
						parent, operationapi.WriteCredentialProbeTimeout,
					)
					defer cancel()
					return operationapi.WriteCredentialGateFromError(
						httpapi.ProviderHost(repo),
						s.syncer.ProbeWriteCredentialForRepo(ctx, ref),
					)
				},
			)
		}
	}
	if s.tokenSources == nil {
		return operationapi.WriteCredentialGate{}
	}
	key := tokenauth.Key{
		Platform: string(httpapi.ProviderKind(repo)),
		Host:     httpapi.ProviderHost(repo),
	}
	src, ok := s.tokenSources.Get(key)
	if !ok || src == nil {
		return operationapi.WriteCredentialGate{}
	}
	desc := src.Descriptor()
	if !desc.HasActiveGitHubApp() {
		return operationapi.WriteCredentialGate{}
	}
	cacheKey := key.String() + "\x00" + desc.CanonicalSourceString()
	return s.operationapi.CachedWriteCredentialGate(cacheKey, func() operationapi.WriteCredentialGate {
		return s.operationapi.ProbeWriteCredential(src, key.Host)
	})
}

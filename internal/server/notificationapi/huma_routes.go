package notificationapi

import (
	"context"
	"strings"
	"time"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/ratelimit"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/platform"
)

func (s *Handlers) GetRateLimits(
	_ context.Context, _ *struct{},
) (*itemapi.RateLimitsOutput, error) {
	if (*s.Syncer) == nil {
		return &itemapi.RateLimitsOutput{Body: itemapi.RateLimitsResponse{
			ProviderPools: map[string]itemapi.RateLimitHostStatus{},
			LocalCeilings: map[string]itemapi.LocalSyncCeilingStatus{},
		}}, nil
	}
	trackers := (*s.Syncer).RateTrackers()
	gqlTrackers := (*s.Syncer).GQLRateTrackers()
	for key, rt := range (*s.Syncer).WriteGQLRateTrackers() {
		if gqlTrackers[key] == nil {
			gqlTrackers[key] = rt
		}
	}
	budgets := (*s.Syncer).Budgets()
	principalLabels := (*s.Syncer).RatePrincipalLabels()
	quotaRegistry := (*s.Syncer).QuotaRegistry()

	labelFor := func(key, providerName, principal string) string {
		if label := principalLabels[key]; label != "" {
			return label
		}
		return itemapi.RatePrincipalLabel(providerName, principal)
	}
	fromTracker := func(rt *ratelimit.RateTracker) itemapi.RateLimitResourceStatus {
		if rt == nil {
			return itemapi.RateLimitResourceStatus{Remaining: -1, Limit: -1}
		}
		requests := rt.RequestsThisHour()
		remaining := rt.Remaining()
		resource := itemapi.RateLimitResourceStatus{
			Remaining: remaining, Limit: rt.RateLimit(),
			Known: rt.Known() && remaining >= 0, Requests: requests,
		}
		if resetAt := rt.ResetAt(); resetAt != nil {
			resource.ResetAt = itemapi.FormatUTCRFC3339(*resetAt)
		}
		return resource
	}

	hosts := make(map[string]itemapi.RateLimitHostStatus, len(trackers))
	for key, rt := range trackers {
		statusKey := itemapi.RateLimitStatusKey(rt)
		hosts[statusKey] = itemapi.RateLimitHostStatus{
			Provider:           rt.Provider(),
			PlatformHost:       rt.PlatformHost(),
			RatePrincipal:      rt.Principal(),
			PrincipalLabel:     labelFor(key, rt.Provider(), rt.Principal()),
			ReserveBuffer:      ghclient.RateReserveBuffer,
			SyncThrottleFactor: rt.ThrottleFactor(),
			SyncPaused:         rt.IsPaused(),
			REST:               fromTracker(rt),
			GraphQL:            fromTracker(gqlTrackers[key]),
		}
	}
	// The registry records what GitHub actually reported per principal and
	// per resource, including pools no local tracker observed yet, so it
	// overrides the tracker-derived view wherever it holds a pool.
	if quotaRegistry != nil {
		now := time.Now().UTC()
		for _, pool := range quotaRegistry.Snapshot() {
			key := itemapi.RateLimitStatusKeyFor(
				string(platform.KindGitHub), pool.Identity.Host, pool.Identity.Principal,
			)
			status, ok := hosts[key]
			if !ok {
				status = itemapi.RateLimitHostStatus{
					Provider:       string(platform.KindGitHub),
					PlatformHost:   pool.Identity.Host,
					RatePrincipal:  pool.Identity.Principal,
					PrincipalLabel: labelFor(key, string(platform.KindGitHub), pool.Identity.Principal),
					ReserveBuffer:  ghclient.RateReserveBuffer,
					REST:           itemapi.RateLimitResourceStatus{Remaining: -1, Limit: -1},
					GraphQL:        itemapi.RateLimitResourceStatus{Remaining: -1, Limit: -1},
				}
			}
			resource := itemapi.RateLimitResourceStatus{
				Remaining: pool.Remaining, Limit: pool.Limit,
				Known: pool.Known && pool.ResetAt.After(now), Requests: pool.Requests,
			}
			if !pool.ResetAt.IsZero() {
				resource.ResetAt = itemapi.FormatUTCRFC3339(pool.ResetAt)
			}
			if pool.Resource == ghclient.QuotaResourceGraphQL {
				status.GraphQL = resource
			} else {
				status.REST = resource
			}
			hosts[key] = status
		}
	}

	ceilings := make(map[string]itemapi.LocalSyncCeilingStatus, len(budgets))
	for key, budget := range budgets {
		if budget == nil {
			continue
		}
		providerName := string(platform.KindGitHub)
		host := key
		principal := ""
		statusKey := key
		if tracker := trackers[key]; tracker != nil {
			providerName = tracker.Provider()
			host = tracker.PlatformHost()
			principal = tracker.Principal()
			statusKey = itemapi.RateLimitStatusKey(tracker)
		} else if prefix, remainder, ok := strings.Cut(key, ":"); ok {
			providerName = prefix
			host = remainder
		}
		ceilings[statusKey] = itemapi.LocalSyncCeilingStatus{
			Provider: providerName, PlatformHost: host,
			RatePrincipal:   principal,
			PrincipalLabel:  labelFor(key, providerName, principal),
			Limit:           budget.Limit(),
			BackgroundLimit: budget.BackgroundLimit(),
			Spent:           budget.Spent(),
			Remaining:       budget.Remaining(),
			ResetAt:         itemapi.FormatUTCRFC3339(budget.ResetAt()),
		}
	}
	return &itemapi.RateLimitsOutput{
		Body: itemapi.RateLimitsResponse{ProviderPools: hosts, LocalCeilings: ceilings},
	}, nil
}

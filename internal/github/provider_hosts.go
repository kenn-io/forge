package github

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
)

// providerHostState is the per-host provider wiring that sync, rate-limit
// reporting, and operation availability read. A published state is never
// changed: setters and AddProviderHost publish a modified copy, so readers on
// other goroutines keep a consistent view without locking.
type providerHostState struct {
	rateTrackers         map[string]*RateTracker // provider/host bucket -> tracker
	writeRateTrackers    map[string]*RateTracker // provider/host bucket -> mutation-credential REST tracker
	writeGQLRateTrackers map[string]*RateTracker // provider/host bucket -> mutation-credential GraphQL tracker
	budgets              map[string]*SyncBudget  // provider/host bucket -> budget
	fetchers             map[string]*GraphQLFetcher
	routers              map[string]*HostRouter
	ratePrincipalLabels  map[string]string
	outages              map[providerHostKey]ProviderHostOutage
}

type providerHostKey struct {
	platform platform.Kind
	host     string
}

func newProviderHostKey(kind platform.Kind, host string) providerHostKey {
	if kind == "" {
		kind = platform.KindGitHub
	}
	return providerHostKey{platform: kind, host: canonicalRepoHost(strings.TrimSpace(host))}
}

// ProviderHostOutage describes a configured provider host that is not
// registered because it failed to start.
type ProviderHostOutage struct {
	// Reason is safe to show to users: it never carries provider response
	// text, account identifiers, or credentials.
	Reason string
	// RetryAt is when the next start attempt runs. It is zero when the
	// failure cannot clear on its own and no attempt is scheduled.
	RetryAt time.Time
}

var emptyProviderHostState = &providerHostState{}

func (s *Syncer) hosts() *providerHostState {
	if state := s.hostState.Load(); state != nil {
		return state
	}
	return emptyProviderHostState
}

// updateHosts publishes a copy of the current host state changed by update.
// Writers serialize on hostStateMu; readers never block.
func (s *Syncer) updateHosts(update func(*providerHostState)) {
	s.hostStateMu.Lock()
	defer s.hostStateMu.Unlock()
	next := *s.hosts()
	update(&next)
	s.hostState.Store(&next)
}

// ProviderHostWiring is the provider state a host contributes once it starts.
type ProviderHostWiring struct {
	Providers            []platform.Provider
	RateTrackers         map[string]*RateTracker
	WriteRateTrackers    map[string]*RateTracker
	WriteGQLRateTrackers map[string]*RateTracker
	Budgets              map[string]*SyncBudget
	Fetchers             map[string]*GraphQLFetcher
	Routers              map[string]*HostRouter
	RatePrincipalLabels  map[string]string
}

// AddProviderHost registers a provider host that failed to start with the
// rest and has since recovered, and clears its outage. Entries that already
// exist are an error: a host is added at most once.
func (s *Syncer) AddProviderHost(kind platform.Kind, host string, wiring ProviderHostWiring) error {
	s.hostStateMu.Lock()
	defer s.hostStateMu.Unlock()
	current := s.hosts()
	for name, shared := range map[string]string{
		"rate tracker":               sharedKey(current.rateTrackers, wiring.RateTrackers),
		"write rate tracker":         sharedKey(current.writeRateTrackers, wiring.WriteRateTrackers),
		"write GraphQL rate tracker": sharedKey(current.writeGQLRateTrackers, wiring.WriteGQLRateTrackers),
		"sync budget":                sharedKey(current.budgets, wiring.Budgets),
		"GraphQL fetcher":            sharedKey(current.fetchers, wiring.Fetchers),
		"credential router":          sharedKey(current.routers, wiring.Routers),
	} {
		if shared != "" {
			return fmt.Errorf("add provider host %s/%s: %s %q already exists", kind, host, name, shared)
		}
	}
	for _, provider := range wiring.Providers {
		if err := s.directClients.Register(provider); err != nil {
			return fmt.Errorf("add provider host %s/%s: %w", kind, host, err)
		}
	}
	for key, budget := range wiring.Budgets {
		if budget == nil {
			continue
		}
		budget.setPaceLead(s.interval)
		if rt := wiring.RateTrackers[key]; rt != nil {
			rt.SetOnWindowReset(budget.Reset)
		}
	}
	next := *current
	next.rateTrackers = mergedMap(current.rateTrackers, wiring.RateTrackers)
	next.writeRateTrackers = mergedMap(current.writeRateTrackers, wiring.WriteRateTrackers)
	next.writeGQLRateTrackers = mergedMap(current.writeGQLRateTrackers, wiring.WriteGQLRateTrackers)
	next.budgets = mergedMap(current.budgets, wiring.Budgets)
	next.fetchers = mergedMap(current.fetchers, wiring.Fetchers)
	next.routers = mergedMap(current.routers, wiring.Routers)
	next.ratePrincipalLabels = mergedMap(current.ratePrincipalLabels, wiring.RatePrincipalLabels)
	next.outages = maps.Clone(current.outages)
	delete(next.outages, newProviderHostKey(kind, host))
	s.hostState.Store(&next)
	return nil
}

// SetProviderHostOutage records why a configured provider host is not
// registered and when it is next retried.
func (s *Syncer) SetProviderHostOutage(kind platform.Kind, host string, outage ProviderHostOutage) {
	s.updateHosts(func(state *providerHostState) {
		state.outages = maps.Clone(state.outages)
		if state.outages == nil {
			state.outages = make(map[providerHostKey]ProviderHostOutage)
		}
		state.outages[newProviderHostKey(kind, host)] = outage
	})
}

// ProviderHostOutage reports why a configured provider host is unavailable.
// It reports false for registered hosts and hosts that were never configured.
func (s *Syncer) ProviderHostOutage(kind platform.Kind, host string) (ProviderHostOutage, bool) {
	if s == nil {
		return ProviderHostOutage{}, false
	}
	outage, ok := s.hosts().outages[newProviderHostKey(kind, host)]
	return outage, ok
}

func mergedMap[V any](current, added map[string]V) map[string]V {
	if len(added) == 0 {
		return current
	}
	merged := make(map[string]V, len(current)+len(added))
	maps.Copy(merged, current)
	maps.Copy(merged, added)
	return merged
}

// sharedKey returns a key present in both maps, or "" when none is.
func sharedKey[V any](current, added map[string]V) string {
	for key := range added {
		if _, ok := current[key]; ok {
			return key
		}
	}
	return ""
}

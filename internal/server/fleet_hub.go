package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/fleet"
)

// buildFleetSnapshot builds the enriched snapshot. When includePeers
// is true it fans out to every configured peer's raw snapshot
// concurrently — HTTP peers over their base URLs, SSH peers over the
// CLI relay. All peers merge through the same path and enrich once.
// The hub never asks peers to fan out, so federation cannot loop; the
// raw endpoint never includes peers.
func (s *Server) buildFleetSnapshot(ctx context.Context, includePeers bool) (fleet.Snapshot, error) {
	local, err := s.buildLocalRaw(ctx)
	if err != nil {
		return fleet.Snapshot{}, err
	}

	var fleetCfg config.Fleet
	if s.cfg != nil {
		fleetCfg = s.cfg.Fleet
	}
	selfKey := s.fleetSelfKey(local.Host.Hostname)

	var results []fleet.PeerResult
	if includePeers && len(fleetCfg.Peers) > 0 {
		results = append(results, s.fetchPeerResults(ctx, fleetCfg)...)
	}
	if includePeers {
		results = append(results, s.fetchSSHPeerResults(ctx)...)
	}

	merged := fleet.Merge(local, selfKey, results)
	return fleet.BuildEnriched(merged, selfKey,
		s.sshFleet.connectionState,
		s.fleetAvailabilityPolicy(),
		fleet.DefaultIdentity()), nil
}

// fetchPeerResults fans out to each configured HTTP peer's /raw concurrently,
// returning one result per peer (degraded results for unreachable peers).
func (s *Server) fetchPeerResults(ctx context.Context, fleetCfg config.Fleet) []fleet.PeerResult {
	timeout := fleetCfg.PeerTimeoutOrDefault()
	results := make([]fleet.PeerResult, len(fleetCfg.Peers))
	var wg sync.WaitGroup
	for i, p := range fleetCfg.Peers {
		wg.Add(1)
		go func(i int, p config.FleetPeer) {
			defer wg.Done()
			results[i] = s.fetchPeerRaw(ctx, p, timeout)
		}(i, p)
	}
	wg.Wait()
	return results
}

// fleetAvailabilityPolicy reflects actual routing per host. The local host
// and configured peers take every operation over the local API or the
// fleet proxies, so they fall through to real capabilities. A host the
// hub cannot route to (e.g. one that appeared in a peer's snapshot but
// is not configured here) is read-only: advertising mutations would
// 404 at dispatch, so they are suppressed here.
func (s *Server) fleetAvailabilityPolicy() fleet.AvailabilityPolicy {
	return hubRoutabilityPolicy{s: s}
}

type hubRoutabilityPolicy struct {
	s *Server
}

// Apply is the uniform (local-host) pass: the local host serves
// every operation directly, so no overrides apply. Remote hosts go
// through ForHost instead.
func (hubRoutabilityPolicy) Apply(map[string]fleet.HostOperationAvailability, bool) {}

// ForHost suppresses unroutable operations for hosts the hub cannot reach
// over a fleet proxy route.
func (p hubRoutabilityPolicy) ForHost(hostKey string) fleet.AvailabilityPolicy {
	if _, ok := p.s.resolveFleetHostTarget(hostKey); ok {
		return fleet.RealCapabilityPolicy{}
	}
	return fleet.HubReadOnlyPolicy{
		Ops:    fleet.DefaultMutationOps(),
		Reason: "This host is read-only from here: the hub has no route to carry this operation to it.",
	}
}

// fetchPeerRaw fetches a single peer's raw snapshot over HTTP. Any failure
// (request, transport, non-2xx, decode, schema mismatch) is captured as a
// degraded result with Reachable=false and Err set — never an error return,
// so one bad peer cannot fail the whole fan-out.
func (s *Server) fetchPeerRaw(ctx context.Context, p config.FleetPeer, timeout time.Duration) fleet.PeerResult {
	res := fleet.PeerResult{
		Key:        p.Key,
		Name:       p.Name,
		BaseURL:    p.BaseURL,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, p.BaseURL+"/api/v1/snapshot/raw", nil)
	if err != nil {
		res.Err = errPtr(err)
		return res
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		res.Err = errPtr(err)
		return res
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		res.Err = new(fmt.Sprintf("peer returned HTTP %d", resp.StatusCode))
		return res
	}
	var raw fleet.RawSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		res.Err = new("decode raw snapshot: " + err.Error())
		return res
	}
	if raw.SchemaVersion != fleet.SchemaVersion {
		res.Err = new(fmt.Sprintf("unsupported schemaVersion %d", raw.SchemaVersion))
		return res
	}
	res.Reachable = true
	res.Platform = raw.Host.Platform
	res.Raw = &raw
	return res
}

func errPtr(e error) *string { s := e.Error(); return &s }

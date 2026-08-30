package fleetapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/fleet"
)

const maxFederationSnapshotBytes = 32 << 20

// buildFleetSnapshot projects one observer-relative view. Hubs build
// the neutral aggregate from member raw snapshots; nodes consume that one
// aggregate and replace their own entries with fresh local authority.
func (s *Handler) buildFleetSnapshot(
	ctx context.Context,
	includePeers bool,
) (fleet.Snapshot, error) {
	local, err := s.buildLocalRaw(ctx)
	if err != nil {
		return fleet.Snapshot{}, err
	}
	fleetConfig := s.configSnapshot().Fleet
	role := fleet.RoleHub
	aggregate := fleet.BuildNeutralAggregate(local, nil)
	aggregateIncomplete := false

	if fleetConfig.RoleOrDefault() == config.FleetRoleHub {
		aggregate, err = s.buildHubAggregate(
			ctx, local, includePeers, fleetConfig.PeerTimeoutOrDefault(),
		)
		if err != nil {
			return fleet.Snapshot{}, err
		}
	} else {
		role = fleet.RoleSpoke
		if includePeers && fleetConfig.Enabled && fleetConfig.Hub != nil {
			if !s.federationActive {
				aggregateIncomplete = true
				message := strings.TrimSpace(s.federationUnavailableReason)
				if message == "" {
					message = "hub activation required"
				}
				aggregate = fleet.BuildNeutralAggregate(local, []fleet.PeerResult{{
					NodeID:     fleet.NodeID(fleetConfig.Hub.NodeID),
					Name:       fleetConfig.Hub.Name,
					BaseURL:    fleetConfig.Hub.BaseURL,
					Role:       fleet.RoleHub,
					ObservedAt: s.now().UTC().Format(time.RFC3339),
					Err:        &message,
				}})
			} else {
				memberTimeout := fleetConfig.PeerTimeoutOrDefault()
				aggregate, err = s.fetchHubAggregate(
					ctx, *fleetConfig.Hub,
					hubAggregateTimeout(memberTimeout), memberTimeout,
				)
				if err != nil {
					aggregateIncomplete = true
					message := "hub aggregate unavailable: " + err.Error()
					aggregate = fleet.BuildNeutralAggregate(local, []fleet.PeerResult{{
						NodeID:     fleet.NodeID(fleetConfig.Hub.NodeID),
						Name:       fleetConfig.Hub.Name,
						BaseURL:    fleetConfig.Hub.BaseURL,
						Role:       fleet.RoleHub,
						ObservedAt: s.now().UTC().Format(time.RFC3339),
						Err:        &message,
					}})
				}
			}
		}
	}

	snapshot := fleet.ProjectForObserver(
		aggregate,
		local,
		fleet.Observer{NodeID: local.NodeID, Role: role},
	)
	snapshot.AggregateIncomplete = aggregateIncomplete
	return snapshot, nil
}

func hubAggregateTimeout(memberTimeout time.Duration) time.Duration {
	return 2 * memberTimeout
}

func (s *Handler) buildHubAggregate(
	ctx context.Context,
	local fleet.RawSnapshot,
	includeMembers bool,
	memberTimeout time.Duration,
) (fleet.NeutralSnapshot, error) {
	fleetConfig := s.configSnapshot().Fleet
	var results []fleet.PeerResult
	if includeMembers && fleetConfig.Enabled && len(fleetConfig.Members) > 0 {
		results = s.fetchMemberResults(ctx, fleetConfig, memberTimeout)
	}
	aggregate := fleet.BuildNeutralAggregate(local, results)
	return fleet.EnrichProviderState(ctx, s.db, aggregate)
}

// fetchMemberResults fans out to each active member's raw endpoint.
func (s *Handler) fetchMemberResults(
	ctx context.Context,
	fleetConfig config.Fleet,
	timeout time.Duration,
) []fleet.PeerResult {
	results := make([]fleet.PeerResult, len(fleetConfig.Members))
	var wait sync.WaitGroup
	for index, member := range fleetConfig.Members {
		wait.Add(1)
		go func(index int, member config.FleetMember) {
			defer wait.Done()
			results[index] = s.fetchMemberRaw(ctx, member, timeout)
		}(index, member)
	}
	wait.Wait()
	return results
}

// fetchMemberRaw captures every peer failure as a degraded result so one bad
// member cannot fail the hub's aggregate.
func (s *Handler) fetchMemberRaw(
	ctx context.Context,
	member config.FleetMember,
	timeout time.Duration,
) fleet.PeerResult {
	result := fleet.PeerResult{
		NodeID: fleet.NodeID(member.NodeID), Name: member.Name,
		BaseURL:    member.BaseURL,
		Role:       fleet.RoleSpoke,
		ObservedAt: s.now().UTC().Format(time.RFC3339),
	}
	target, ok := s.resolveEnrolledMember(member)
	if !ok {
		result.Err = new("federation credential unavailable")
		return result
	}
	raw, err := s.fetchRawSnapshot(ctx, target, timeout)
	if err != nil {
		result.Err = errPtr(err)
		return result
	}
	if raw.NodeID != result.NodeID {
		result.Err = new("member snapshot node ID does not match enrollment")
		return result
	}
	result.Reachable = true
	result.Platform = raw.Host.Platform
	result.Raw = &raw
	return result
}

func (s *Handler) fetchRawSnapshot(
	ctx context.Context,
	target fleetHostTarget,
	timeout time.Duration,
) (fleet.RawSnapshot, error) {
	var raw fleet.RawSnapshot
	err := s.fetchFederationJSON(
		ctx, target, target.clients.rest, timeout, "/api/v1/snapshot/raw", &raw,
	)
	if err != nil {
		return fleet.RawSnapshot{}, err
	}
	if raw.ProtocolVersion != federation.ProtocolVersion {
		return fleet.RawSnapshot{}, fmt.Errorf(
			"unsupported protocolVersion %d", raw.ProtocolVersion,
		)
	}
	return raw, nil
}

func (s *Handler) fetchHubAggregate(
	ctx context.Context,
	hub config.FleetHub,
	timeout time.Duration,
	memberTimeout time.Duration,
) (fleet.NeutralSnapshot, error) {
	member := config.FleetMember{
		NodeID: hub.NodeID, Name: hub.Name,
		BaseURL: hub.BaseURL, State: federation.EnrollmentActive,
	}
	target, ok := s.resolveEnrolledMember(member)
	if !ok {
		return fleet.NeutralSnapshot{}, fmt.Errorf("federation credential unavailable")
	}
	var aggregate fleet.NeutralSnapshot
	query := url.Values{"member_timeout": {memberTimeout.String()}}
	if err := s.fetchFederationJSON(
		ctx, target, target.clients.proxy, timeout,
		"/api/v1/snapshot/aggregate?"+query.Encode(), &aggregate,
	); err != nil {
		return fleet.NeutralSnapshot{}, err
	}
	if aggregate.ProtocolVersion != federation.ProtocolVersion {
		return fleet.NeutralSnapshot{}, fmt.Errorf(
			"unsupported protocolVersion %d", aggregate.ProtocolVersion,
		)
	}
	if !neutralSnapshotContainsNode(aggregate, fleet.NodeID(hub.NodeID)) {
		return fleet.NeutralSnapshot{}, fmt.Errorf(
			"hub aggregate does not contain its enrolled node ID",
		)
	}
	return aggregate, nil
}

func (s *Handler) fetchFederationJSON(
	ctx context.Context,
	target fleetHostTarget,
	client *http.Client,
	timeout time.Duration,
	path string,
	destination any,
) error {
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext, http.MethodGet, target.member.BaseURL+path, nil,
	)
	if err != nil {
		return err
	}
	s.authorizeFederationRequest(request.Header, target.credential)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("peer returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(
		response.Body, maxFederationSnapshotBytes+1,
	))
	if err != nil {
		return fmt.Errorf("read federation snapshot: %w", err)
	}
	if len(body) > maxFederationSnapshotBytes {
		return fmt.Errorf("federation snapshot exceeds response limit")
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("decode federation snapshot: %w", err)
	}
	return nil
}

func neutralSnapshotContainsNode(
	aggregate fleet.NeutralSnapshot,
	nodeID fleet.NodeID,
) bool {
	for _, host := range aggregate.Hosts {
		if host.NodeID == nodeID {
			return true
		}
	}
	return false
}

func errPtr(err error) *string {
	message := err.Error()
	return &message
}

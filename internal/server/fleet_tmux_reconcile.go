package server

import (
	"slices"
	"time"

	"go.kenn.io/middleman/internal/fleet"
)

const fleetSessionStatusExited = "exited"

type fleetOwnedTmuxSession struct {
	Name             string
	WorktreeKey      string
	SessionScopedKey string
	RuntimeKind      string
	SessionKind      string
	Role             string
	Status           string
	Label            string
	CreatedAt        time.Time
}

func rawSessionsForOwnedTmux(
	owned []fleetOwnedTmuxSession,
) []fleet.RawSession {
	out := make([]fleet.RawSession, 0, len(owned))
	for _, session := range owned {
		if session.Name == "" || session.SessionScopedKey == "" {
			continue
		}
		status := session.Status
		if status == "" {
			status = "running"
		}
		runtimeKind := session.RuntimeKind
		if runtimeKind == "" {
			runtimeKind = "tmux"
		}
		out = append(out, fleet.RawSession{
			ScopedKey:   session.SessionScopedKey,
			WorktreeKey: session.WorktreeKey,
			Status:      status,
			RuntimeKind: runtimeKind,
			SessionKind: session.SessionKind,
			Role:        session.Role,
			Label:       session.Label,
		})
	}
	return out
}

func reconcileFleetTmuxSnapshot(
	raw *fleet.RawSnapshot,
	owned []fleetOwnedTmuxSession,
	snapshot fleetTmuxMonitorSnapshot,
) {
	ownedByName := make(map[string]fleetOwnedTmuxSession, len(owned))
	for _, session := range owned {
		if session.Name == "" {
			continue
		}
		ownedByName[session.Name] = session
	}
	sessionIndexes := make(map[string]int, len(raw.Sessions))
	for i := range raw.Sessions {
		sessionIndexes[raw.Sessions[i].ScopedKey] = i
	}

	raw.Host.TmuxSessions = []fleet.TmuxSessionInfo{}
	current := snapshot.CurrentInventory
	if current == nil || !current.Succeeded {
		raw.Host.TmuxProbeError = snapshot.InventoryError
		return
	}
	raw.Host.TmuxLastPolledAt = current.PolledAt.UTC().Format(time.RFC3339)
	raw.Host.TmuxProbeError = snapshot.InventoryError
	if snapshot.Metrics != nil {
		raw.Host.TmuxMetricsError = snapshot.Metrics.Error
	}

	names := make([]string, 0, len(current.Sessions))
	for name := range current.Sessions {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		live := current.Sessions[name]
		ownedSession, managed := ownedByName[name]
		info := tmuxInfoFromLive(live)
		if managed {
			info.Managed = true
			info.WorktreeKey = ownedSession.WorktreeKey
			info.SessionScopedKey = ownedSession.SessionScopedKey
			reconcileLiveOwnedRawSession(
				raw, sessionIndexes, ownedSession, live, snapshot,
			)
		} else if !snapshot.IncludeUnmanagedDetail {
			info.Windows = []fleet.TmuxWindowInfo{}
		}
		raw.Host.TmuxSessions = append(raw.Host.TmuxSessions, info)
	}

	for _, session := range owned {
		if session.Name == "" {
			continue
		}
		if _, ok := current.Sessions[session.Name]; ok {
			continue
		}
		if !ownedSessionExitedByDrift(session, current, snapshot.PreviousInventory) {
			continue
		}
		if idx, ok := sessionIndexes[session.SessionScopedKey]; ok {
			raw.Sessions[idx].Status = fleetSessionStatusExited
		}
	}
}

func tmuxInfoFromLive(live fleetTmuxLiveSession) fleet.TmuxSessionInfo {
	info := fleet.TmuxSessionInfo{
		Name:        live.Name,
		Managed:     false,
		Windows:     slices.Clone(live.Windows),
		WindowCount: live.WindowCount,
	}
	if info.WindowCount < len(info.Windows) {
		info.WindowCount = len(info.Windows)
	}
	if live.CreatedAt != nil {
		info.CreatedAt = live.CreatedAt.UTC().Format(time.RFC3339)
	}
	return info
}

func reconcileLiveOwnedRawSession(
	raw *fleet.RawSnapshot,
	sessionIndexes map[string]int,
	owned fleetOwnedTmuxSession,
	live fleetTmuxLiveSession,
	snapshot fleetTmuxMonitorSnapshot,
) {
	idx, ok := sessionIndexes[owned.SessionScopedKey]
	if !ok {
		return
	}
	raw.Sessions[idx].Status = liveOwnedStatus(raw.Sessions[idx].Status)
	metrics, ok := metricsForLiveOwnedSession(live.Name, snapshot)
	if !ok {
		return
	}
	raw.Sessions[idx].CPUPercent = new(metrics.CPUPercent)
	raw.Sessions[idx].ResidentMB = new(metrics.ResidentMB)
	raw.Sessions[idx].ProcessCount = new(metrics.ProcessCount)
	raw.Sessions[idx].ExecutableName = metrics.ExecutableName
	if metrics.LastOutputAt != nil {
		s := metrics.LastOutputAt.UTC().Format(time.RFC3339)
		raw.Sessions[idx].LastOutputAt = &s
	}
	if metrics.LastActiveAt != nil {
		s := metrics.LastActiveAt.UTC().Format(time.RFC3339)
		raw.Sessions[idx].LastActiveAt = &s
	}
}

func liveOwnedStatus(existing string) string {
	switch existing {
	case "starting", "error":
		return existing
	default:
		return "running"
	}
}

func metricsForLiveOwnedSession(
	name string,
	snapshot fleetTmuxMonitorSnapshot,
) (fleetTmuxSessionMetrics, bool) {
	if snapshot.CurrentInventory == nil || snapshot.Metrics == nil ||
		snapshot.Metrics.Error != "" {
		return fleetTmuxSessionMetrics{}, false
	}
	if _, ok := snapshot.CurrentInventory.Sessions[name]; !ok {
		return fleetTmuxSessionMetrics{}, false
	}
	if snapshot.Metrics.SampledAt.IsZero() ||
		snapshot.CurrentInventory.PolledAt.Sub(snapshot.Metrics.SampledAt) >
			fleetTmuxStaleThreshold {
		return fleetTmuxSessionMetrics{}, false
	}
	metrics, ok := snapshot.Metrics.Sessions[name]
	return metrics, ok
}

func ownedSessionExitedByDrift(
	owned fleetOwnedTmuxSession,
	current *fleetTmuxInventorySample,
	previous *fleetTmuxInventorySample,
) bool {
	if current == nil || previous == nil || !current.Succeeded || !previous.Succeeded {
		return false
	}
	if owned.CreatedAt.IsZero() ||
		!owned.CreatedAt.Before(current.PolledAt) ||
		!owned.CreatedAt.Before(previous.PolledAt) {
		return false
	}
	if _, ok := current.Sessions[owned.Name]; ok {
		return false
	}
	if _, ok := previous.Sessions[owned.Name]; ok {
		return false
	}
	return true
}

package github

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"log/slog"
	"slices"
	"strings"

	"go.kenn.io/forge/internal/db"
	platformgithub "go.kenn.io/forge/platform/github"
)

// withObsoleteMetadata records whether a commit event's commit is still
// reachable from the merge request head. Unchanged input returns the original
// JSON so callers can skip rewriting untouched rows.
func withObsoleteMetadata(metadataJSON string, obsolete bool) (string, bool) {
	metadata := map[string]any{}
	if metadataJSON != "" {
		var existing map[string]any
		if err := json.Unmarshal([]byte(metadataJSON), &existing); err == nil && existing != nil {
			metadata = existing
		}
	}
	value, present := metadata["obsolete"]
	if obsolete {
		if value == true {
			return metadataJSON, false
		}
		metadata["obsolete"] = true
	} else {
		if !present {
			return metadataJSON, false
		}
		delete(metadata, "obsolete")
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return metadataJSON, false
	}
	return string(encoded), true
}

func commitMetadataOrder(metadataJSON string) int {
	if metadataJSON == "" {
		return 0
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return 0
	}
	if order := metadataInt(metadata["commit_order_key"]); order > 0 {
		return order
	}
	return metadataInt(metadata["commit_order"])
}

func metadataInt(value any) int {
	switch v := value.(type) {
	case float64:
		if v > 0 && v == float64(int(v)) {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	case int64:
		if v > 0 {
			return int(v)
		}
	}
	return 0
}

type commitOrderAssigner struct {
	bySHA map[string]int
	next  int
}

func newCommitOrderAssigner(events []db.MREvent) commitOrderAssigner {
	assigner := commitOrderAssigner{bySHA: map[string]int{}}
	for _, event := range events {
		if event.EventType != "commit" {
			continue
		}
		order := commitMetadataOrder(event.MetadataJSON)
		if order == 0 && event.ID > 0 {
			order = int(event.ID)
		}
		if order > assigner.next {
			assigner.next = order
		}
		sha := commitOrderSHA(event.Summary)
		if sha != "" && order > 0 {
			assigner.bySHA[sha] = order
		}
	}
	return assigner
}

func (a *commitOrderAssigner) apply(event *db.MREvent, listOrder int) {
	sha := commitOrderSHA(event.Summary)
	stableOrder := a.bySHA[sha]
	if stableOrder == 0 {
		a.next++
		stableOrder = a.next
		if sha != "" {
			a.bySHA[sha] = stableOrder
		}
	}
	event.MetadataJSON = platformgithub.WithCommitOrderMetadata(event.MetadataJSON, listOrder, stableOrder)
}

func (s *Syncer) commitOrderAssigner(ctx context.Context, mrID int64) (commitOrderAssigner, error) {
	events, err := s.db.ListMREvents(ctx, mrID)
	if err != nil {
		return commitOrderAssigner{}, err
	}
	return newCommitOrderAssigner(events), nil
}

// commitEventSHA returns the event's full commit SHA, or "" when neither
// provider field contains one. PlatformExternalID covers Gitealike events,
// whose Summary contains the commit message instead of the SHA.
func commitEventSHA(event db.MREvent) string {
	candidates := []string{
		strings.ToLower(event.PlatformExternalID),
		commitOrderSHA(event.Summary),
	}
	for _, sha := range candidates {
		if len(sha) != 40 && len(sha) != 64 {
			continue
		}
		valid := true
		for _, r := range sha {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				valid = false
				break
			}
		}
		if valid {
			return sha
		}
	}
	return ""
}

// stampableCommitEvent reports whether liveness computation may touch this
// event: it must be a commit event with a representable full SHA and, when
// metadata is present, that metadata must parse to a JSON object (corrupt
// metadata is left untouched rather than clobbered).
func stampableCommitEvent(event db.MREvent) (string, bool) {
	if event.EventType != "commit" {
		return "", false
	}
	sha := commitEventSHA(event)
	if sha == "" {
		return "", false
	}
	if event.MetadataJSON != "" {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(event.MetadataJSON), &metadata); err != nil || metadata == nil {
			return "", false
		}
	}
	return sha, true
}

// commitLivenessMemo caches one verified reachability answer per MR.
// Reachability from a fixed head over a fixed candidate set is a pure
// function of immutable git history, so an entry never needs invalidation:
// a changed head or candidate set simply misses the key and recomputes, and
// concurrent rounds writing the same key write the same value. Only verified
// walks are memoized — an unverifiable head proves nothing durable.
type commitLivenessMemo struct {
	key  string
	live map[string]bool
}

// commitLivenessMemoEntry is the LRU list payload for one MR's memo.
type commitLivenessMemoEntry struct {
	mrID int64
	memo commitLivenessMemo
}

// The memo retains provider-controlled input (candidate sets grow with every
// force push), so it is bounded on both axes: at most defaultLivenessMemoLimit
// MR entries with least-recently-used eviction, and results for candidate
// sets larger than defaultLivenessCandidateLimit are computed normally but
// never memoized. Neither bound affects flag correctness — a miss or an
// unmemoized round only costs one budget-capped walk.
const (
	defaultLivenessCandidateLimit = 4096
	defaultLivenessMemoLimit      = 1024
)

// computeCommitLiveness derives commit-obsolescence metadata for a sync round
// BEFORE the round commits, so the results ride the round's own
// revision-guarded snapshot write and a stale round is inert by construction.
// It mutates the incoming batch's commit events in place (their flags travel
// inside the normal upsert) and returns metadata updates for stored commit
// events the round does not re-list. Unverifiable heads return no updates and
// the affected events keep their last verified flags.
//
// A memo hit replaces only the ancestry walk: the verdicts still flow through
// the same injection code, so a hit can never skip a needed write — re-listed
// incoming events always receive their flags, and stored rows that drifted
// are repaired.
func (s *Syncer) computeCommitLiveness(
	ctx context.Context,
	repo RepoRef,
	mrID int64,
	headSHA string,
	incoming []db.MREvent,
) map[string]string {
	if s.clones == nil || headSHA == "" {
		return nil
	}
	stored, err := s.db.ListMREvents(ctx, mrID)
	if err != nil {
		slog.Warn("commit liveness: list events failed",
			"repo", repo.Owner+"/"+repo.Name, "mr", mrID, "err", err)
		return nil
	}
	return s.computeCommitLivenessForEvents(ctx, repo, mrID, headSHA, incoming, stored)
}

// computeCommitLivenessForEvents is the computation core behind
// computeCommitLiveness for callers that already hold the stored events —
// in particular the terminal-transition finalizer, which receives them from
// the parent snapshot's own transaction so the walk runs on tx-consistent
// data.
func (s *Syncer) computeCommitLivenessForEvents(
	ctx context.Context,
	repo RepoRef,
	mrID int64,
	headSHA string,
	incoming []db.MREvent,
	stored []db.MREvent,
) map[string]string {
	if s.clones == nil || headSHA == "" {
		return nil
	}
	headSHA = strings.ToLower(headSHA)

	candidateSHAs := make([]string, 0, len(stored)+len(incoming))
	seen := make(map[string]bool, len(stored)+len(incoming))
	addCandidate := func(sha string) {
		if !seen[sha] {
			seen[sha] = true
			candidateSHAs = append(candidateSHAs, sha)
		}
	}
	for _, event := range stored {
		if sha, ok := stampableCommitEvent(event); ok {
			addCandidate(sha)
		}
	}
	for i := range incoming {
		if sha, ok := stampableCommitEvent(incoming[i]); ok {
			addCandidate(sha)
		}
	}
	if len(candidateSHAs) == 0 {
		return nil
	}
	candidateLimit := s.livenessCandidateLimit
	if candidateLimit <= 0 {
		candidateLimit = defaultLivenessCandidateLimit
	}
	memoizable := len(candidateSHAs) <= candidateLimit

	memoKey := livenessMemoKey(headSHA, candidateSHAs)
	live, hit := s.livenessMemoLookup(mrID, memoKey)
	if !hit {
		reach, err := s.clones.CommitsReachableFrom(
			ctx, string(repoPlatform(repo)), repoHost(repo),
			repo.Owner, repo.Name, headSHA, candidateSHAs,
		)
		if err != nil {
			slog.Warn("commit liveness: reachability failed",
				"repo", repo.Owner+"/"+repo.Name, "mr", mrID, "err", err)
			return nil
		}
		if !reach.HeadVerified {
			return nil
		}
		live = reach.Live
		if memoizable {
			s.livenessMemoStore(mrID, memoKey, live)
		}
	}

	incomingKeys := make(map[string]bool, len(incoming))
	for i := range incoming {
		sha, ok := stampableCommitEvent(incoming[i])
		if !ok {
			continue
		}
		incomingKeys[incoming[i].DedupeKey] = true
		isLive, evaluated := live[sha]
		if !evaluated {
			continue
		}
		if metadataJSON, changed := withObsoleteMetadata(
			incoming[i].MetadataJSON, !isLive,
		); changed {
			incoming[i].MetadataJSON = metadataJSON
		}
	}

	updates := make(map[string]string)
	for _, event := range stored {
		sha, ok := stampableCommitEvent(event)
		if !ok || incomingKeys[event.DedupeKey] {
			continue
		}
		isLive, evaluated := live[sha]
		if !evaluated {
			continue
		}
		if metadataJSON, changed := withObsoleteMetadata(
			event.MetadataJSON, !isLive,
		); changed {
			updates[event.DedupeKey] = metadataJSON
		}
	}
	return updates
}

// livenessMemoKey hashes the walk's exact inputs: the head plus the sorted,
// deduplicated candidate set. Any change to either produces a different key.
func livenessMemoKey(headSHA string, candidateSHAs []string) string {
	sorted := make([]string, len(candidateSHAs))
	copy(sorted, candidateSHAs)
	slices.Sort(sorted)
	digest := sha256.New()
	digest.Write([]byte(headSHA))
	for _, sha := range sorted {
		digest.Write([]byte{0})
		digest.Write([]byte(sha))
	}
	return string(digest.Sum(nil))
}

func (s *Syncer) livenessMemoLookup(mrID int64, key string) (map[string]bool, bool) {
	s.livenessMemoMu.Lock()
	defer s.livenessMemoMu.Unlock()
	elem, ok := s.livenessMemos[mrID]
	if !ok {
		return nil, false
	}
	entry := elem.Value.(*commitLivenessMemoEntry)
	if entry.memo.key != key {
		return nil, false
	}
	s.livenessMemoLRU.MoveToFront(elem)
	return entry.memo.live, true
}

func (s *Syncer) livenessMemoStore(mrID int64, key string, live map[string]bool) {
	s.livenessMemoMu.Lock()
	defer s.livenessMemoMu.Unlock()
	if s.livenessMemos == nil {
		s.livenessMemos = make(map[int64]*list.Element)
		s.livenessMemoLRU = list.New()
	}
	memo := commitLivenessMemo{key: key, live: live}
	if elem, exists := s.livenessMemos[mrID]; exists {
		elem.Value.(*commitLivenessMemoEntry).memo = memo
		s.livenessMemoLRU.MoveToFront(elem)
		return
	}
	limit := s.livenessMemoLimit
	if limit <= 0 {
		limit = defaultLivenessMemoLimit
	}
	for s.livenessMemoLRU.Len() >= limit {
		oldest := s.livenessMemoLRU.Back()
		if oldest == nil {
			break
		}
		s.livenessMemoLRU.Remove(oldest)
		delete(s.livenessMemos, oldest.Value.(*commitLivenessMemoEntry).mrID)
	}
	s.livenessMemos[mrID] = s.livenessMemoLRU.PushFront(
		&commitLivenessMemoEntry{mrID: mrID, memo: memo},
	)
}

func commitOrderSHA(summary string) string {
	return strings.ToLower(strings.TrimSpace(summary))
}

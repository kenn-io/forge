package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// mergeQueueDepartureLookupWindow bounds how long a departure without a
// matching removal event keeps being looked up before it is dropped.
const mergeQueueDepartureLookupWindow = 30 * time.Minute

// mergeQueuePageSize bounds one merge queue page. Queues are short, so one
// page normally covers the whole queue.
const mergeQueuePageSize = 100

// errMergeQueueUnsupported reports that the host's GraphQL schema has no
// merge queue, as on GitHub Enterprise Server releases without the feature.
var errMergeQueueUnsupported = errors.New("GitHub merge queue is not supported by this host")

type gqlMergeQueueQuery struct {
	Repository struct {
		DefaultBranchRef *struct {
			Name string
		}
		// MergeQueue without a branch argument is the default branch's
		// queue. It is null when that branch has no merge queue.
		MergeQueue *struct {
			Configuration *struct {
				MergeMethod string
			}
			Entries struct {
				Nodes    []gqlMergeQueueEntry
				PageInfo platformgithub.GraphQLPageInfo
			} `graphql:"entries(first: $pageSize, after: $cursor)"`
		}
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type gqlMergeQueueEntry struct {
	Position             int
	State                string
	EnqueuedAt           time.Time
	EstimatedTimeToMerge *int
	PullRequest          *struct {
		Number int
	}
}

type gqlMergeQueueRemovalQuery struct {
	Repository struct {
		PullRequest *struct {
			State         string
			TimelineItems struct {
				Nodes []struct {
					RemovedFromMergeQueueEvent struct {
						CreatedAt time.Time
						Reason    string
					} `graphql:"... on RemovedFromMergeQueueEvent"`
				}
			} `graphql:"timelineItems(last: 1, itemTypes: [REMOVED_FROM_MERGE_QUEUE_EVENT])"`
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// FetchMergeQueue observes the repository's default-branch merge queue. A
// repository without a merge queue returns an unconfigured snapshot. Hosts
// whose schema has no merge queue return errMergeQueueUnsupported without
// another request once rejected.
func (g *GraphQLFetcher) FetchMergeQueue(
	ctx context.Context, owner, name string,
) (db.MergeQueueSnapshot, error) {
	if g.mergeQueueRejected.Load() {
		return db.MergeQueueSnapshot{}, errMergeQueueUnsupported
	}
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	var snapshot db.MergeQueueSnapshot
	nodes, err := fetchAllPages(ctx, func(
		ctx context.Context, cursor *string,
	) ([]gqlMergeQueueEntry, platformgithub.GraphQLPageInfo, error) {
		var q gqlMergeQueueQuery
		if err := g.client.Query(ctx, &q, map[string]any{
			"owner":    githubv4.String(owner),
			"name":     githubv4.String(name),
			"pageSize": githubv4.Int(mergeQueuePageSize),
			"cursor":   cursorVar(cursor),
		}); err != nil {
			return nil, platformgithub.GraphQLPageInfo{}, err
		}
		queue := q.Repository.MergeQueue
		if queue == nil || q.Repository.DefaultBranchRef == nil {
			snapshot.Configured = false
			return nil, platformgithub.GraphQLPageInfo{}, nil
		}
		snapshot.Configured = true
		snapshot.Branch = q.Repository.DefaultBranchRef.Name
		if queue.Configuration != nil {
			snapshot.MergeMethod = strings.ToLower(queue.Configuration.MergeMethod)
		}
		return queue.Entries.Nodes, queue.Entries.PageInfo, nil
	})
	if err != nil {
		if isMergeQueueSchemaRejection(err) {
			slog.Warn("GraphQL rejected merge queue fields, disabling merge queue sync for host",
				"host", g.host, "err", err,
			)
			g.mergeQueueRejected.Store(true)
			return db.MergeQueueSnapshot{}, errMergeQueueUnsupported
		}
		return db.MergeQueueSnapshot{}, fmt.Errorf("fetch merge queue for %s/%s: %w", owner, name, err)
	}
	if !snapshot.Configured {
		return db.MergeQueueSnapshot{}, nil
	}
	snapshot.Entries = make([]db.MergeQueueEntry, 0, len(nodes))
	for _, node := range nodes {
		if node.PullRequest == nil || node.PullRequest.Number <= 0 {
			continue
		}
		entry := db.MergeQueueEntry{
			Number:     node.PullRequest.Number,
			Position:   node.Position,
			State:      node.State,
			EnqueuedAt: node.EnqueuedAt.UTC(),
		}
		if node.EstimatedTimeToMerge != nil {
			entry.EstimatedTimeToMerge = new(time.Duration(*node.EstimatedTimeToMerge) * time.Second)
		}
		snapshot.Entries = append(snapshot.Entries, entry)
	}
	return snapshot, nil
}

// FetchMergeQueueRemoval reads a pull request's state and its most recent
// removal from the merge queue. removal is nil when GitHub reports none.
func (g *GraphQLFetcher) FetchMergeQueueRemoval(
	ctx context.Context, owner, name string, number int,
) (state string, removal *db.MergeQueueRemoval, err error) {
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	var q gqlMergeQueueRemovalQuery
	if err := g.client.Query(ctx, &q, map[string]any{
		"owner":  githubv4.String(owner),
		"name":   githubv4.String(name),
		"number": githubv4.Int(number),
	}); err != nil {
		return "", nil, fmt.Errorf("fetch merge queue removal for %s/%s#%d: %w", owner, name, number, err)
	}
	pr := q.Repository.PullRequest
	if pr == nil {
		return "", nil, nil
	}
	if len(pr.TimelineItems.Nodes) == 0 {
		return pr.State, nil, nil
	}
	event := pr.TimelineItems.Nodes[len(pr.TimelineItems.Nodes)-1].RemovedFromMergeQueueEvent
	if event.CreatedAt.IsZero() {
		return pr.State, nil, nil
	}
	return pr.State, &db.MergeQueueRemoval{
		Number:    number,
		RemovedAt: event.CreatedAt.UTC(),
		Reason:    event.Reason,
	}, nil
}

// isMergeQueueSchemaRejection reports whether err is GraphQL schema validation
// rejecting the merge queue fields rather than a transient failure.
func isMergeQueueSchemaRejection(err error) bool {
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "mergequeue") {
		return false
	}
	for _, phrase := range []string{
		"doesn't exist on type",
		"does not exist on type",
		"cannot query field",
		"unknown field",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

// RefreshMergeQueue replaces the stored merge queue snapshot for a GitHub
// repository. Entering or leaving the queue does not change a pull request's
// updated_at, so the ETag-gated pull list never observes it; repository sync
// runs this read on every pass instead, and merge handlers run it after
// enqueueing. A pull request that left the queue while still open records
// GitHub's removal reason so the failure stays visible after it happened. It
// reports whether any pull request's queue membership, position, or state
// changed, and announces each changed pull request.
func (s *Syncer) RefreshMergeQueue(ctx context.Context, repo RepoRef, repoID int64) bool {
	if repoID <= 0 || repoPlatform(repo) != platform.KindGitHub {
		return false
	}
	fetcher := s.fetcherForContext(ctx, repo)
	if fetcher == nil || !s.graphQLReadAllowed(ctx, repo, fetcher) {
		return false
	}
	observedAt := s.now().UTC()
	snapshot, err := fetcher.FetchMergeQueue(ctx, repo.Owner, repo.Name)
	if errors.Is(err, errMergeQueueUnsupported) {
		return false
	}
	if err != nil {
		slog.Warn("merge queue sync failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return false
	}
	snapshot.ObservedAt = observedAt
	changes, err := s.db.ReplaceMergeQueue(ctx, repoID, snapshot)
	if err != nil {
		slog.Error("store merge queue failed",
			"repo", repo.Owner+"/"+repo.Name,
			"err", err,
		)
		return false
	}
	changed := changes.Changed
	resolved := s.resolveMergeQueueDepartures(ctx, fetcher, repo, repoID, observedAt)
	changed = append(changed, resolved...)
	slices.Sort(changed)
	changed = slices.Compact(changed)
	if s.onMergeQueueChanged != nil {
		for _, number := range changed {
			s.onMergeQueueChanged(ctx, repoID, number)
		}
	}
	return len(changed) > 0
}

// RefreshMergeQueueForRepository refreshes the merge queue of a tracked
// repository whose last read found one, for an explicit user refresh of one
// of its pull requests. Scheduled sync may be paused, so this is the only
// way a manual refresh can show that a pull request left the queue.
// Repositories with neither a merge queue nor a pending departure cost no
// request.
func (s *Syncer) RefreshMergeQueueForRepository(ctx context.Context, repoID int64) bool {
	queue, err := s.db.GetRepoMergeQueue(ctx, repoID)
	if err != nil {
		return false
	}
	if queue == nil {
		// A removed queue configuration can still leave departures whose
		// reason lookup failed; a manual refresh retries those too.
		pending, err := s.db.ListPendingMergeQueueDepartures(ctx, repoID)
		if err != nil || len(pending) == 0 {
			return false
		}
	}
	stored, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil || stored == nil {
		return false
	}
	repo, ok := s.trackedRepoByProviderKey(
		platform.Kind(stored.Platform), stored.PlatformHost, stored.Key,
	)
	// Archived repositories are collected for history only; live queue
	// reads stay with live sync.
	if !ok || repo.Archived {
		return false
	}
	return s.RefreshMergeQueue(ctx, repoRefFromStoredIdentity(repo, *stored), repoID)
}

// resolveMergeQueueDepartures looks up GitHub's removal reason for each
// pending departure, including ones an earlier pass failed to resolve. An open
// pull request removed without merging becomes a visible removal; a merged or
// closed one is dropped. A departure GitHub has recorded no removal for since
// it was queued is retried until mergeQueueDepartureLookupWindow passes. It
// returns the pull requests whose removal became visible.
func (s *Syncer) resolveMergeQueueDepartures(
	ctx context.Context,
	fetcher *GraphQLFetcher,
	repo RepoRef,
	repoID int64,
	now time.Time,
) []int {
	departures, err := s.db.ListPendingMergeQueueDepartures(ctx, repoID)
	if err != nil {
		slog.Error("load pending merge queue departures failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
		return nil
	}
	var resolved []int
	for _, departure := range departures {
		state, removal, err := fetcher.FetchMergeQueueRemoval(ctx, repo.Owner, repo.Name, departure.Number)
		if err != nil {
			slog.Warn("load merge queue removal failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", departure.Number,
				"err", err,
			)
			continue
		}
		var storeErr error
		switch {
		case !strings.EqualFold(state, "OPEN"):
			storeErr = s.db.DropMergeQueueDeparture(ctx, repoID, departure)
		case removal != nil && !removal.RemovedAt.Before(departure.EnqueuedAt):
			if strings.EqualFold(removal.Reason, "merged") {
				storeErr = s.db.DropMergeQueueDeparture(ctx, repoID, departure)
				break
			}
			storeErr = s.db.ResolveMergeQueueDeparture(ctx, repoID, departure, *removal)
			if storeErr == nil {
				resolved = append(resolved, departure.Number)
			}
		case now.Sub(departure.DepartedAt) > mergeQueueDepartureLookupWindow:
			storeErr = s.db.DropMergeQueueDeparture(ctx, repoID, departure)
		}
		if storeErr != nil {
			slog.Error("store merge queue departure failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", departure.Number,
				"err", storeErr,
			)
		}
	}
	return resolved
}

// refreshWatchedMergeQueues refreshes the merge queue of each repository with
// a watched pull request that is currently queued, so a queue the user is
// following advances on the fast watched cadence instead of the repository
// pass. It reports whether any queue changed.
func (s *Syncer) refreshWatchedMergeQueues(ctx context.Context, mrs []WatchedMR) bool {
	refreshed := make(map[int64]bool)
	changed := false
	for _, mr := range mrs {
		kind := watchedMRPlatform(mr)
		if kind != platform.KindGitHub {
			continue
		}
		repo, ok := s.trackedRepoByIdentity(kind, mr.Owner, mr.Name, mr.PlatformHost)
		if !ok {
			continue
		}
		stored, err := s.db.GetRepoByIdentity(ctx, db.RepoIdentity{
			Platform:     string(kind),
			PlatformHost: repoHost(repo),
			Owner:        repo.Owner,
			Name:         repo.Name,
			RepoPath:     repo.RepoPath,
		})
		if err != nil || stored == nil || refreshed[stored.ID] {
			continue
		}
		entry, err := s.db.GetMergeQueueEntry(ctx, stored.ID, mr.Number)
		if err != nil {
			slog.Warn("load watched merge queue entry",
				"repo", mr.Owner+"/"+mr.Name, "number", mr.Number, "err", err,
			)
			continue
		}
		if entry == nil {
			continue
		}
		refreshed[stored.ID] = true
		if s.RefreshMergeQueue(ctx, repo, stored.ID) {
			changed = true
		}
	}
	return changed
}

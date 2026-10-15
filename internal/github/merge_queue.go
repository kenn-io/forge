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

// gqlEnqueueCount counts a pull request's enqueue events. Each attempt adds
// one, so the count identifies an attempt in GitHub's own order, with no clock
// comparison.
type gqlEnqueueCount struct {
	TotalCount int
}

type gqlMergeQueueEntry struct {
	Position             int
	State                string
	EnqueuedAt           time.Time
	EstimatedTimeToMerge *int
	PullRequest          *struct {
		Number   int
		Enqueues gqlEnqueueCount `graphql:"enqueues: timelineItems(itemTypes: [ADDED_TO_MERGE_QUEUE_EVENT])"`
	}
}

type gqlMergeQueueOutcomeQuery struct {
	Repository struct {
		PullRequest *struct {
			State    string
			Enqueues gqlEnqueueCount `graphql:"enqueues: timelineItems(itemTypes: [ADDED_TO_MERGE_QUEUE_EVENT])"`
			// LatestQueueEvent is the last enqueue or removal: a removal ends
			// the latest attempt, an enqueue means it has not ended yet.
			LatestQueueEvent struct {
				Nodes []struct {
					Typename                   string `graphql:"__typename"`
					RemovedFromMergeQueueEvent struct {
						CreatedAt time.Time
						Reason    string
					} `graphql:"... on RemovedFromMergeQueueEvent"`
				}
			} `graphql:"latestQueueEvent: timelineItems(last: 1, itemTypes: [ADDED_TO_MERGE_QUEUE_EVENT, REMOVED_FROM_MERGE_QUEUE_EVENT])"`
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
			Attempt:    node.PullRequest.Enqueues.TotalCount,
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

// FetchMergeQueueOutcome reads a pull request's state, its enqueue count, and
// the removal that ended its latest queue attempt, if that attempt has ended.
// found is false when GitHub reports no such pull request.
func (g *GraphQLFetcher) FetchMergeQueueOutcome(
	ctx context.Context, owner, name string, number int,
) (outcome db.MergeQueueOutcome, found bool, err error) {
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	var q gqlMergeQueueOutcomeQuery
	if err := g.client.Query(ctx, &q, map[string]any{
		"owner":  githubv4.String(owner),
		"name":   githubv4.String(name),
		"number": githubv4.Int(number),
	}); err != nil {
		return db.MergeQueueOutcome{}, false, fmt.Errorf(
			"fetch merge queue outcome for %s/%s#%d: %w", owner, name, number, err,
		)
	}
	pr := q.Repository.PullRequest
	if pr == nil {
		return db.MergeQueueOutcome{}, false, nil
	}
	outcome = db.MergeQueueOutcome{State: pr.State, Attempts: pr.Enqueues.TotalCount}
	if nodes := pr.LatestQueueEvent.Nodes; len(nodes) > 0 &&
		nodes[len(nodes)-1].Typename == "RemovedFromMergeQueueEvent" {
		event := nodes[len(nodes)-1].RemovedFromMergeQueueEvent
		outcome.LatestRemoval = &db.MergeQueueRemoval{
			Number:    number,
			RemovedAt: event.CreatedAt.UTC(),
			Reason:    event.Reason,
		}
	}
	return outcome, true, nil
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
	observedAt := s.now().UTC()
	var snapshot db.MergeQueueSnapshot
	fetcher, err := s.mergeQueueRead(ctx, repo, func(fetcher *GraphQLFetcher) error {
		var fetchErr error
		snapshot, fetchErr = fetcher.FetchMergeQueue(ctx, repo.Owner, repo.Name)
		return fetchErr
	})
	if errors.Is(err, errNoMergeQueueFetcher) || errors.Is(err, errMergeQueueUnsupported) {
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

// mergeQueueAbsentCheckInterval is how often a repository pass re-reads a
// repository whose last read found no merge queue, so a newly enabled queue is
// noticed. Merge queues are rare and their configuration changes rarely, so
// reading every repository on every pass would spend quota on empty answers.
const mergeQueueAbsentCheckInterval = 6 * time.Hour

// refreshMergeQueueIfDue reads the repository's merge queue on a repository
// pass when it is worth a request: every pass for a repository with a known
// queue or a departure still being resolved, and otherwise once per
// mergeQueueAbsentCheckInterval. A merge GitHub refuses because the branch
// requires the queue refreshes it immediately through RecordEnqueue.
func (s *Syncer) refreshMergeQueueIfDue(ctx context.Context, repo RepoRef, repoID int64) {
	if repoID <= 0 || repoPlatform(repo) != platform.KindGitHub {
		return
	}
	key := repoFailKey(repo)
	queue, err := s.db.GetRepoMergeQueue(ctx, repoID)
	if err != nil {
		return
	}
	if queue == nil {
		pending, err := s.db.ListPendingMergeQueueDepartures(ctx, repoID)
		if err != nil {
			return
		}
		if checkedAt, ok := s.mergeQueueCheckedAt.Load(key); ok && len(pending) == 0 {
			if when, _ := checkedAt.(time.Time); s.now().Sub(when) < mergeQueueAbsentCheckInterval {
				return
			}
		}
	}
	s.mergeQueueCheckedAt.Store(key, s.now())
	s.RefreshMergeQueue(ctx, repo, repoID)
}

// mergeQueueReadRecheck is how long merge queue reads stay on the write
// credential after the read credential was refused, before the read credential
// is tried again. A permission granted later is picked up within this window.
const mergeQueueReadRecheck = time.Hour

// errNoMergeQueueFetcher reports that no credential may read the merge queue
// right now, because none is configured or quota holds them back.
var errNoMergeQueueFetcher = errors.New("no credential may read the merge queue now")

// mergeQueueRead runs read with the repository's read credential, and retries
// with the write (merge) credential when the read credential is refused the
// merge queue. A sync GitHub App without the Merge queues permission is
// refused ("Resource not accessible by integration") while the user who merges
// can read it. Granting the App that permission keeps reads on the App's own
// quota; without it, reads still work through the merge credential, which is
// then used directly until mergeQueueReadRecheck passes. It returns the
// fetcher that served the read.
func (s *Syncer) mergeQueueRead(
	ctx context.Context, repo RepoRef, read func(*GraphQLFetcher) error,
) (*GraphQLFetcher, error) {
	if repoPlatform(repo) != platform.KindGitHub {
		return nil, errNoMergeQueueFetcher
	}
	writeFetcher := s.mergeQueueWriteFetcher(ctx, repo)
	key := repoFailKey(repo)
	if deniedAt, ok := s.mergeQueueReadDenied.Load(key); ok && writeFetcher != nil {
		if when, _ := deniedAt.(time.Time); s.now().Sub(when) < mergeQueueReadRecheck {
			return writeFetcher, read(writeFetcher)
		}
	}
	readFetcher := s.fetcherForContext(ctx, repo)
	if readFetcher == nil || !s.graphQLReadAllowed(ctx, repo, readFetcher) {
		if writeFetcher == nil {
			return nil, errNoMergeQueueFetcher
		}
		return writeFetcher, read(writeFetcher)
	}
	err := read(readFetcher)
	if err == nil || writeFetcher == nil || !isMergeQueueReadRefused(err) {
		if err == nil {
			s.mergeQueueReadDenied.Delete(key)
		}
		return readFetcher, err
	}
	s.mergeQueueReadDenied.Store(key, s.now())
	slog.Info("read credential refused the merge queue; reading it as the merge credential",
		"repo", repo.Owner+"/"+repo.Name,
	)
	return writeFetcher, read(writeFetcher)
}

// mergeQueueWriteFetcher returns the repository's write-credential fetcher
// when its route has one separate from the read credential and its GraphQL
// quota allows a read, or nil.
func (s *Syncer) mergeQueueWriteFetcher(ctx context.Context, repo RepoRef) *GraphQLFetcher {
	router := s.routers[repoHost(repo)]
	if router == nil {
		return nil
	}
	fetcher, err := router.WriteFetcherForRepo(repo.Owner, repo.Name)
	if err != nil || fetcher == nil {
		return nil
	}
	verdict := s.reserveVerdictForContext(
		ctx, repo, QuotaResourceGraphQL, true, IsSyncBudgetContext(ctx),
	)
	if verdict.known {
		if verdict.exhausted {
			return nil
		}
		return fetcher
	}
	if backoff, _ := fetcher.ShouldBackoff(); backoff {
		return nil
	}
	return fetcher
}

// isMergeQueueReadRefused reports whether GitHub refused a merge queue read
// for lack of permission, as it does for a GitHub App without the Merge
// queues permission.
func isMergeQueueReadRefused(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "resource not accessible by integration") //nolint:kennlint // githubv4 exposes GraphQL errors only as text
}

// RecordEnqueue tracks a pull request Forge just added to the merge queue.
// It records the attempt provisionally first and then reads the queue, so a
// read that shows the pull request sets the attempt from GitHub's count, and a
// read that misses it records a departure for this attempt. Recording after
// the read would overwrite an attempt the read had just identified. attempt is
// the number from before the request, or 0 when unknown; record is false for
// an enqueue that started no attempt Forge can identify, which only reads the
// queue.
func (s *Syncer) RecordEnqueue(
	ctx context.Context, repo RepoRef, repoID int64, number, attempt int, record bool, requestedAt time.Time,
) {
	if record {
		if err := s.db.RecordProvisionalMergeQueueEntry(ctx, repoID, number, attempt, requestedAt); err != nil {
			slog.Warn("record provisional merge queue entry",
				"repo", repo.Owner+"/"+repo.Name, "number", number, "err", err)
		}
	}
	s.RefreshMergeQueue(ctx, repo, repoID)
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

// resolveMergeQueueDepartures looks up how each pending departure ended,
// including ones an earlier pass failed to resolve. Attempts are matched in
// GitHub's own event order, never by comparing Forge's clock with GitHub's:
// a departure resolves once GitHub reports at least its attempt and the latest
// attempt has ended. A departure of an unknown attempt (0) cannot be told apart
// from earlier attempts, so it is never resolved and expires instead. An open pull request removed without merging then shows
// that removal; a merged or closed one is dropped. A departure GitHub has not
// caught up with is retried until mergeQueueDepartureLookupWindow passes. It
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
		outcome, found, err := fetcher.FetchMergeQueueOutcome(ctx, repo.Owner, repo.Name, departure.Number)
		if err != nil {
			slog.Warn("load merge queue outcome failed",
				"repo", repo.Owner+"/"+repo.Name,
				"number", departure.Number,
				"err", err,
			)
			continue
		}
		var storeErr error
		switch {
		case !found || !strings.EqualFold(outcome.State, "OPEN"):
			storeErr = s.db.DropMergeQueueDeparture(ctx, repoID, departure)
		case departure.Attempt > 0 && outcome.Attempts >= departure.Attempt &&
			outcome.LatestRemoval != nil:
			if strings.EqualFold(outcome.LatestRemoval.Reason, "merged") {
				storeErr = s.db.DropMergeQueueDeparture(ctx, repoID, departure)
				break
			}
			storeErr = s.db.ResolveMergeQueueDeparture(ctx, repoID, departure, *outcome.LatestRemoval)
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

// MergeQueueAttempts returns how many times a pull request has been added to
// a merge queue, read before Forge enqueues it so the new attempt can be told
// apart from earlier ones. ok is false when the count cannot be read.
func (s *Syncer) MergeQueueAttempts(ctx context.Context, repo RepoRef, number int) (int, bool) {
	if repoPlatform(repo) != platform.KindGitHub {
		return 0, false
	}
	var outcome db.MergeQueueOutcome
	var found bool
	_, err := s.mergeQueueRead(ctx, repo, func(fetcher *GraphQLFetcher) error {
		var fetchErr error
		outcome, found, fetchErr = fetcher.FetchMergeQueueOutcome(ctx, repo.Owner, repo.Name, number)
		return fetchErr
	})
	if err != nil || !found {
		return 0, false
	}
	return outcome.Attempts, true
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

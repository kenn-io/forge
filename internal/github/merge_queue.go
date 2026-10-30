package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

// errMergeQueueUnsupported reports that the host's GraphQL schema has no
// merge queue, as on GitHub Enterprise Server releases without the feature.
var errMergeQueueUnsupported = errors.New("GitHub merge queue is not supported by this host")

// errNoMergeQueueFetcher reports that no credential may read merge queue state
// right now, because none is configured or quota holds them back.
var errNoMergeQueueFetcher = errors.New("no credential may read the merge queue now")

// mergeQueueRecheck bounds how long known merge queue facts are reused: the
// merge method of a branch's queue, and a pull request whose base branch had
// no queue.
const mergeQueueRecheck = 6 * time.Hour

// mergeQueueReadRecheck is how long merge queue reads stay on the write
// credential after the read credential was refused, before the read credential
// is tried again. A permission granted later is picked up within this window.
const mergeQueueReadRecheck = time.Hour

// gqlPullRequestMergeQueueQuery reads one pull request's merge queue state, as
// the GitHub Pull Requests extension for VS Code does: the queue is a property
// of the pull request, not a repository-wide list. The queue configuration is
// read separately because GitHub refuses it to an App that only has the Merge
// queues permission, and one refused field fails the whole query.
type gqlPullRequestMergeQueueQuery struct {
	Repository struct {
		PullRequest *struct {
			State               string
			BaseRefName         string
			IsMergeQueueEnabled bool
			MergeQueueEntry     *struct {
				Position             int
				State                string
				EnqueuedAt           time.Time
				EstimatedTimeToMerge *int
			}
			// LatestQueueEvent is the last enqueue, removal, or reopening.
			// With no entry, an enqueue means the queue has not listed the
			// pull request yet, and a removal says why it left; a reopening
			// after a removal means that removal belongs to before the close.
			LatestQueueEvent struct {
				Nodes []struct {
					Typename                   string `graphql:"__typename"`
					RemovedFromMergeQueueEvent struct {
						CreatedAt time.Time
						Reason    string
					} `graphql:"... on RemovedFromMergeQueueEvent"`
				}
			} `graphql:"latestQueueEvent: timelineItems(last: 1, itemTypes: [ADDED_TO_MERGE_QUEUE_EVENT, REMOVED_FROM_MERGE_QUEUE_EVENT, REOPENED_EVENT])"`
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type gqlMergeQueueConfigurationQuery struct {
	Repository struct {
		MergeQueue *struct {
			Configuration *struct {
				MergeMethod string
			}
		} `graphql:"mergeQueue(branch: $branch)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// FetchPullRequestMergeQueue reads a pull request's merge queue state. open is
// false for a closed or merged pull request, and found is false when GitHub
// reports no such pull request. Hosts whose schema has no merge queue return
// errMergeQueueUnsupported without another request once rejected.
func (g *GraphQLFetcher) FetchPullRequestMergeQueue(
	ctx context.Context, owner, name string, number int,
) (state db.MergeQueueState, open, found bool, err error) {
	if g.mergeQueueRejected.Load() {
		return db.MergeQueueState{}, false, false, errMergeQueueUnsupported
	}
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	var q gqlPullRequestMergeQueueQuery
	if err := g.client.Query(ctx, &q, map[string]any{
		"owner":  githubv4.String(owner),
		"name":   githubv4.String(name),
		"number": githubv4.Int(number),
	}); err != nil {
		if isMergeQueueSchemaRejection(err) {
			slog.Warn("GraphQL rejected merge queue fields, disabling merge queue reads for host",
				"host", g.host, "err", err,
			)
			g.mergeQueueRejected.Store(true)
			return db.MergeQueueState{}, false, false, errMergeQueueUnsupported
		}
		return db.MergeQueueState{}, false, false, fmt.Errorf(
			"fetch merge queue state for %s/%s#%d: %w", owner, name, number, err,
		)
	}
	pr := q.Repository.PullRequest
	if pr == nil {
		return db.MergeQueueState{}, false, false, nil
	}
	state = db.MergeQueueState{
		Number:     number,
		BaseBranch: pr.BaseRefName,
		Enabled:    pr.IsMergeQueueEnabled,
	}
	nodes := pr.LatestQueueEvent.Nodes
	var latest string
	if len(nodes) > 0 {
		latest = nodes[len(nodes)-1].Typename
	}
	switch {
	case pr.MergeQueueEntry != nil:
		entry := pr.MergeQueueEntry
		state.Queued = true
		state.Position = entry.Position
		state.State = entry.State
		state.EnqueuedAt = new(entry.EnqueuedAt.UTC())
		if entry.EstimatedTimeToMerge != nil {
			state.EstimatedTimeToMerge = new(time.Duration(*entry.EstimatedTimeToMerge) * time.Second)
		}
	case latest == "AddedToMergeQueueEvent":
		// Enqueued, but the queue does not list it yet.
		state.Queued = true
		state.State = "QUEUED"
	case latest == "RemovedFromMergeQueueEvent":
		event := nodes[len(nodes)-1].RemovedFromMergeQueueEvent
		if !strings.EqualFold(event.Reason, "merged") {
			state.RemovedAt = new(event.CreatedAt.UTC())
			state.RemovalReason = event.Reason
		}
	}
	return state, strings.EqualFold(pr.State, "OPEN"), true, nil
}

// FetchMergeQueueMergeMethod reads the merge method of a branch's merge
// queue, lowercased, or "" when the branch has no queue.
func (g *GraphQLFetcher) FetchMergeQueueMergeMethod(
	ctx context.Context, owner, name, branch string,
) (string, error) {
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	var q gqlMergeQueueConfigurationQuery
	if err := g.client.Query(ctx, &q, map[string]any{
		"owner":  githubv4.String(owner),
		"name":   githubv4.String(name),
		"branch": githubv4.String(branch),
	}); err != nil {
		return "", fmt.Errorf("fetch merge queue configuration for %s/%s@%s: %w", owner, name, branch, err)
	}
	if q.Repository.MergeQueue == nil || q.Repository.MergeQueue.Configuration == nil {
		return "", nil
	}
	return strings.ToLower(q.Repository.MergeQueue.Configuration.MergeMethod), nil
}

// isMergeQueueSchemaRejection reports whether err is GraphQL schema validation
// rejecting the merge queue fields rather than a transient failure.
func isMergeQueueSchemaRejection(err error) bool {
	return isSchemaRejectionOf(err, "mergequeue")
}

// isMergeQueueReadRefused reports whether GitHub refused a merge queue read
// for lack of permission, as it does for a GitHub App without the Merge
// queues permission.
func isMergeQueueReadRefused(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "resource not accessible by integration") //nolint:kennlint // errtext: githubv4 exposes GraphQL errors only as text
}

// RefreshPullRequestMergeQueue reads a pull request's merge queue state and
// stores it. Entering or leaving the queue does not change the pull request's
// updated_at, so ordinary pull request sync never notices it; relay hints,
// the watched lane, manual refreshes, merges, and repository passes for queued
// pull requests call this instead. It reports whether anything shown changed,
// and announces the pull request when it did.
func (s *Syncer) RefreshPullRequestMergeQueue(
	ctx context.Context, repo RepoRef, repoID int64, number int,
) bool {
	changed, _ := s.refreshPullRequestMergeQueue(ctx, repo, repoID, number)
	return changed
}

// RecordPullRequestEnqueued reads the queue state of a pull request Forge just
// added to its merge queue. When that read cannot run or fails, it stores the
// pull request as queued with an unknown position, so the view shows the
// enqueue and the next repository pass, which re-reads queued pull requests,
// replaces it with GitHub's state.
func (s *Syncer) RecordPullRequestEnqueued(
	ctx context.Context, repo RepoRef, repoID int64, number int, baseBranch string,
) {
	if _, read := s.refreshPullRequestMergeQueue(ctx, repo, repoID, number); read {
		return
	}
	// GitHub already accepted the enqueue, so record it even if the request
	// that made it was cancelled during the read.
	ctx = context.WithoutCancel(ctx)
	changed, err := s.db.UpsertMergeQueueState(ctx, repoID, db.MergeQueueState{
		Number:     number,
		BaseBranch: baseBranch,
		Enabled:    true,
		Queued:     true,
		State:      "QUEUED",
		ObservedAt: s.now().UTC(),
	})
	if err != nil {
		slog.Error("store enqueued merge queue state failed",
			"repo", repo.Owner+"/"+repo.Name, "number", number, "err", err)
		return
	}
	if changed && s.onMergeQueueChanged != nil {
		s.onMergeQueueChanged(ctx, repoID, number)
	}
}

// refreshPullRequestMergeQueue is RefreshPullRequestMergeQueue that also
// reports whether GitHub was read, so callers can tell a failed read apart.
func (s *Syncer) refreshPullRequestMergeQueue(
	ctx context.Context, repo RepoRef, repoID int64, number int,
) (changed, read bool) {
	if repoID <= 0 || repoPlatform(repo) != platform.KindGitHub {
		return false, false
	}
	observedAt := s.now().UTC()
	var state db.MergeQueueState
	var open, found bool
	_, err := s.mergeQueueRead(ctx, repo, func(fetcher *GraphQLFetcher) error {
		var fetchErr error
		state, open, found, fetchErr = fetcher.FetchPullRequestMergeQueue(ctx, repo.Owner, repo.Name, number)
		return fetchErr
	})
	if errors.Is(err, errNoMergeQueueFetcher) || errors.Is(err, errMergeQueueUnsupported) {
		return false, false
	}
	if err != nil {
		logSyncFailure(ctx, slog.LevelWarn, "merge queue read failed", err,
			"repo", repo.Owner+"/"+repo.Name, "number", number)
		return false, false
	}
	if !found || !open {
		// GitHub reports the pull request closed or gone even if ordinary
		// sync has not recorded it yet; its queue state no longer applies.
		deleted, err := s.db.DeleteMergeQueueState(ctx, repoID, number)
		if err != nil {
			slog.Error("clear merge queue state failed",
				"repo", repo.Owner+"/"+repo.Name, "number", number, "err", err)
			return false, true
		}
		if deleted && s.onMergeQueueChanged != nil {
			s.onMergeQueueChanged(ctx, repoID, number)
		}
		return deleted, true
	}
	branchKey := mergeQueueRepoKey(repo) + "\x00" + state.BaseBranch
	if state.Enabled {
		s.mergeQueueBranchAbsent.Delete(branchKey)
		state.MergeMethod = s.mergeQueueMergeMethod(ctx, repo, state.BaseBranch)
	} else {
		s.mergeQueueBranchAbsent.Store(branchKey, s.now())
	}
	state.ObservedAt = observedAt
	changed, err = s.db.UpsertMergeQueueState(ctx, repoID, state)
	if err != nil {
		slog.Error("store merge queue state failed",
			"repo", repo.Owner+"/"+repo.Name, "number", number, "err", err)
		return false, false
	}
	if changed && s.onMergeQueueChanged != nil {
		s.onMergeQueueChanged(ctx, repoID, number)
	}
	return changed, true
}

// RefreshPullRequestMergeQueueForRepository refreshes a pull request's merge
// queue state by repository ID, for an explicit user refresh: scheduled sync
// may be paused, and the pull request itself does not change when the queue
// adds or removes it. Archived repositories get no live read.
func (s *Syncer) RefreshPullRequestMergeQueueForRepository(
	ctx context.Context, repoID int64, number int,
) bool {
	stored, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil || stored == nil {
		return false
	}
	repo, ok := s.trackedRepoByProviderKey(
		platform.Kind(stored.Platform), stored.PlatformHost, stored.Key,
	)
	if !ok || repo.Archived {
		return false
	}
	return s.RefreshPullRequestMergeQueue(ctx, repoRefFromStoredIdentity(repo, *stored), repoID, number)
}

// refreshMergeQueueAfterDetailSync checks a pull request's merge queue state
// after its detail sync, because queue membership never changes the pull
// request itself. Every live detail sync calls it once the repository is
// resolved and the archived and removed-upstream checks have passed: watched,
// relay, and manual syncs, and the scheduled refresh of dormant pull
// requests. A sync that failed for anything but its diff checks nothing.
func (s *Syncer) refreshMergeQueueAfterDetailSync(
	ctx context.Context, repo RepoRef, repoID int64, number int, syncErr *error,
) {
	ctx = withoutDegradedSyncTracking(ctx)
	if !detailSyncAdmitsFollowUpReads(ctx, repo, syncErr) {
		return
	}
	s.refreshPullRequestMergeQueueIfDue(ctx, repo, repoID, number)
}

// mergeQueueBulkRecheck bounds how often bulk pull request hydration re-reads
// a pull request's queue state. Bulk hydration runs whenever anything in the
// repository changes and covers every open pull request, so reading each one
// every time would cost a request per pull request on a queued branch.
const mergeQueueBulkRecheck = time.Hour

// refreshMergeQueueAfterBulkDetail checks a pull request's queue state after
// bulk hydration marked its detail fresh. That postpones the scheduled detail
// refresh, which is where a pull request enqueued without a relay hint is
// otherwise noticed. A pull request read, or tried, within
// mergeQueueBulkRecheck is skipped, on top of the usual due rules, so reads
// that keep failing do not cost a request per pull request on every pass.
func (s *Syncer) refreshMergeQueueAfterBulkDetail(
	ctx context.Context, repo RepoRef, repoID int64, number int,
) {
	if repo.Archived || IsArchiveSyncBudgetContext(ctx) {
		return
	}
	stored, err := s.db.GetMergeQueueState(ctx, repoID, number)
	if err != nil {
		return
	}
	now := s.now()
	if stored != nil && now.Sub(stored.ObservedAt) < mergeQueueBulkRecheck {
		return
	}
	key := mergeQueueRepoKey(repo) + "\x00" + strconv.Itoa(number)
	if triedAt, ok := s.mergeQueueBulkChecked.Load(key); ok {
		if when, _ := triedAt.(time.Time); now.Sub(when) < mergeQueueBulkRecheck {
			return
		}
	}
	s.mergeQueueBulkChecked.Store(key, now)
	s.refreshPullRequestMergeQueueIfDue(ctx, repo, repoID, number)
}

// refreshPullRequestMergeQueueIfDue refreshes an open pull request whose
// queue state may have moved: one whose base branch has a queue, one with
// queue state to show, one retargeted since its last read, or one Forge has
// not read. A pull request on a branch read without a queue within
// mergeQueueRecheck is skipped, whether that read was its own or another pull
// request's, so detail refreshes, relay hints, and watched refreshes for
// branches without merge queues cost no request.
func (s *Syncer) refreshPullRequestMergeQueueIfDue(
	ctx context.Context, repo RepoRef, repoID int64, number int,
) bool {
	if repoPlatform(repo) != platform.KindGitHub {
		return false
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
	if err != nil || mr == nil || mr.State != db.MergeRequestStateOpen {
		return false
	}
	stored, err := s.db.GetMergeQueueState(ctx, repoID, number)
	if err != nil {
		return false
	}
	quiet := stored == nil ||
		(!stored.Enabled && !stored.Queued && stored.RemovalReason == "" && stored.BaseBranch == mr.BaseBranch)
	// A blocked pull request may be blocked by a merge queue its branch just
	// started requiring. The view disables merge for a blocked pull request
	// unless it knows about the queue, so it must not wait out the recheck.
	if quiet && mr.MergeableState != "blocked" && s.mergeQueueBranchAbsentRecently(repo, mr.BaseBranch) {
		return false
	}
	return s.RefreshPullRequestMergeQueue(ctx, repo, repoID, number)
}

// mergeQueueRepoKey identifies a repository for the merge queue caches by its
// stable provider key, so another repository that takes over the owner/name
// route never inherits them. Refs not yet resolved to a key fall back to the
// route.
func mergeQueueRepoKey(repo RepoRef) string {
	if repo.Key.IsZero() {
		return repoFailKey(repo)
	}
	return string(repoPlatform(repo)) + "\x00" + repoHost(repo) + "\x00" + repo.Key.String()
}

// mergeQueueBranchAbsentRecently reports whether a read within
// mergeQueueRecheck found branch without a merge queue.
func (s *Syncer) mergeQueueBranchAbsentRecently(repo RepoRef, branch string) bool {
	readAt, ok := s.mergeQueueBranchAbsent.Load(mergeQueueRepoKey(repo) + "\x00" + branch)
	if !ok {
		return false
	}
	when, _ := readAt.(time.Time)
	return s.now().Sub(when) < mergeQueueRecheck
}

// refreshQueuedPullRequests re-reads the pull requests Forge last saw in a
// merge queue, so a repository pass notices them leave even without the
// activity relay. Other pull requests cost nothing here.
func (s *Syncer) refreshQueuedPullRequests(ctx context.Context, repo RepoRef, repoID int64) {
	if repoID <= 0 || repoPlatform(repo) != platform.KindGitHub {
		return
	}
	numbers, err := s.db.ListQueuedMergeRequestNumbers(ctx, repoID)
	if err != nil {
		slog.Warn("list queued pull requests failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err)
		return
	}
	for _, number := range numbers {
		s.RefreshPullRequestMergeQueue(ctx, repo, repoID, number)
	}
}

// cachedMergeMethod is a branch queue's merge method and when it was read.
type cachedMergeMethod struct {
	method string
	readAt time.Time
}

// mergeQueueMergeMethod returns the merge method of a branch's queue, read at
// most once per mergeQueueRecheck and shared by every pull request targeting
// that branch, or "" when it cannot be read so the stored method is kept.
// GitHub refuses the queue configuration to an App that only has the Merge
// queues permission, so a refused read retries with the merge credential
// without moving queue reads off the read credential.
func (s *Syncer) mergeQueueMergeMethod(ctx context.Context, repo RepoRef, branch string) string {
	key := mergeQueueRepoKey(repo) + "\x00" + branch
	if cached, ok := s.mergeQueueMethods.Load(key); ok {
		if entry, _ := cached.(cachedMergeMethod); s.now().Sub(entry.readAt) < mergeQueueRecheck {
			return entry.method
		}
	}
	read := func(fetcher *GraphQLFetcher) (string, error) {
		return fetcher.FetchMergeQueueMergeMethod(ctx, repo.Owner, repo.Name, branch)
	}
	var method string
	err := errNoMergeQueueFetcher
	if fetcher := s.fetcherForContext(ctx, repo); fetcher != nil && s.graphQLReadAllowed(ctx, repo, fetcher) {
		method, err = read(fetcher)
	}
	if err != nil && (errors.Is(err, errNoMergeQueueFetcher) || isMergeQueueReadRefused(err)) {
		if fetcher := s.mergeQueueWriteFetcher(ctx, repo); fetcher != nil {
			method, err = read(fetcher)
		}
	}
	if err != nil {
		// Keep the last known method and wait out the interval before asking
		// again, so a refused configuration read is not repeated on every
		// refresh.
		var known string
		if cached, ok := s.mergeQueueMethods.Load(key); ok {
			if entry, ok := cached.(cachedMergeMethod); ok {
				known = entry.method
			}
		}
		logSyncFailure(ctx, slog.LevelWarn, "merge queue merge method read failed", err,
			"repo", repo.Owner+"/"+repo.Name, "branch", branch)
		s.mergeQueueMethods.Store(key, cachedMergeMethod{method: known, readAt: s.now()})
		return known
	}
	s.mergeQueueMethods.Store(key, cachedMergeMethod{method: method, readAt: s.now()})
	return method
}

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
	key := mergeQueueRepoKey(repo)
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

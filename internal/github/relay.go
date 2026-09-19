package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v7"
	"go.kenn.io/forge/internal/activityrelay"
	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// RelayStatus is a process-local view of the subscription, carried with sync status.
type RelayStatus struct {
	Connected bool            `json:"connected"`
	Recent    []RelayActivity `json:"recent"`
}

type RelayActivity struct {
	ID         int64     `json:"id"`
	Repository string    `json:"repository"`
	Target     string    `json:"target" enum:"pull_request,pull_request_checks,workflow_runs,issue,repository_refs,repository"`
	Number     int       `json:"number"`
	ReceivedAt time.Time `json:"received_at"`
}

// RelayOptions configures one relay subscription owned by RunRelay.
type RelayOptions struct {
	URL    string
	Client *http.Client
	// Backoff paces reconnect attempts. Nil selects jittered exponential
	// backoff from one second to a 30-second ceiling.
	Backoff backoff.BackOff
}

const (
	maxRelayRetryDelay = 30 * time.Second
	// relayStableAfter is how long a subscription must stay open before its
	// reconnect backoff resets.
	relayStableAfter = maxRelayRetryDelay
	maxRelayRecent   = 20
	// relayQueueLimit bounds refresh work waiting on provider budget; hints
	// beyond it are dropped and covered by ordinary syncing.
	relayQueueLimit = 1024
)

func (s *Syncer) updateRelayStatus(update func(*RelayStatus)) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	status := *s.Status()
	relay := RelayStatus{Recent: []RelayActivity{}}
	if status.Relay != nil {
		relay = *status.Relay
	}
	update(&relay)
	status.Relay = &relay
	s.publishStatusLocked(&status)
}

// SetOnRelayRefresh installs the callback before the subscription starts.
func (s *Syncer) SetOnRelayRefresh(fn func(context.Context, int64, string, int)) {
	s.onRelayRefresh = fn
}

// RunRelay keeps one subscription open until ctx ends, reconnecting with
// backoff. Hints are refreshed in arrival order by a single worker so a slow
// provider call never stalls the connection.
func (s *Syncer) RunRelay(ctx context.Context, options RelayOptions) {
	policy := options.Backoff
	if policy == nil {
		exponential := backoff.NewExponentialBackOff()
		exponential.InitialInterval = time.Second
		exponential.MaxInterval = maxRelayRetryDelay
		exponential.RandomizationFactor = 0.2
		policy = exponential
	}
	policy.Reset()
	queue := &relayQueue{signal: make(chan struct{}, 1)}
	var workers sync.WaitGroup
	workers.Go(func() { s.drainRelayQueue(ctx, queue) })
	defer workers.Wait()
	s.updateRelayStatus(func(status *RelayStatus) { status.Connected = false })
	var sequence int64
	for ctx.Err() == nil {
		stream, err := activityrelay.Open(ctx, options.Client, options.URL)
		if err == nil {
			opened := time.Now()
			s.updateRelayStatus(func(status *RelayStatus) { status.Connected = true })
			err = stream.Read(func(hint activityrelay.Hint) {
				sequence++
				s.receiveRelayHint(hint, sequence, queue)
			})
			_ = stream.Close()
			s.updateRelayStatus(func(status *RelayStatus) { status.Connected = false })
			// An accepted stream that dies at once is still a failure; only a
			// stream that stayed up resets the backoff, so a relay or proxy
			// that accepts and immediately drops cannot cause a reconnect storm.
			if time.Since(opened) >= relayStableAfter {
				policy.Reset()
			}
		}
		if ctx.Err() != nil {
			return
		}
		delay := policy.NextBackOff()
		if delay == backoff.Stop {
			delay = maxRelayRetryDelay
		}
		slog.Debug("retrying relay subscription", "next", delay, "err", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Syncer) receiveRelayHint(hint activityrelay.Hint, id int64, queue *relayQueue) {
	if !s.SyncEnabled() {
		return
	}
	repo, ok := s.trackedRepoByProviderID(platform.KindGitHub, hint.Host, hint.RepositoryID)
	if !ok || repo.Archived {
		return
	}
	activity := RelayActivity{
		ID: id, Repository: repo.Owner + "/" + repo.Name,
		Target: hint.Target, Number: hint.Number, ReceivedAt: s.now().UTC(),
	}
	s.updateRelayStatus(func(status *RelayStatus) {
		status.Recent = append([]RelayActivity{activity}, status.Recent[:min(maxRelayRecent-1, len(status.Recent))]...)
	})
	queue.push(hint)
}

// relayQueue coalesces repeated hints for the same target while a refresh waits.
type relayQueue struct {
	mu      sync.Mutex
	order   []activityrelay.Hint
	pending map[activityrelay.Hint]struct{}
	signal  chan struct{}
}

func (q *relayQueue) push(hint activityrelay.Hint) {
	q.mu.Lock()
	if _, waiting := q.pending[hint]; !waiting && len(q.order) < relayQueueLimit {
		if q.pending == nil {
			q.pending = make(map[activityrelay.Hint]struct{})
		}
		q.pending[hint] = struct{}{}
		q.order = append(q.order, hint)
	}
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *relayQueue) pop() (activityrelay.Hint, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) == 0 {
		return activityrelay.Hint{}, false
	}
	hint := q.order[0]
	q.order = q.order[1:]
	delete(q.pending, hint)
	return hint, true
}

func (s *Syncer) drainRelayQueue(ctx context.Context, queue *relayQueue) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-queue.signal:
		}
		for hint, ok := queue.pop(); ok; hint, ok = queue.pop() {
			if ctx.Err() != nil {
				return
			}
			if !s.SyncEnabled() {
				continue
			}
			if err := s.refreshRelayHint(WithSyncBudget(ctx), hint); err != nil && ctx.Err() == nil {
				slog.Warn("relay refresh failed", "repository_id", hint.RepositoryID, "target", hint.Target, "number", hint.Number, "err", err)
			}
		}
	}
}

// refreshRelayHint performs one provider refresh. A hint that cannot be
// served now, because budget, cooldown, or catalog state forbids it, is
// dropped: ordinary syncing covers it later.
func (s *Syncer) refreshRelayHint(ctx context.Context, hint activityrelay.Hint) error {
	repo, tracked := s.trackedRepoByProviderID(platform.KindGitHub, hint.Host, hint.RepositoryID)
	if !tracked || repo.Archived {
		return nil
	}
	bucket, err := s.bucketKeyForRepo(repo, false)
	if err != nil {
		return err
	}
	if hint.Target != activityrelay.WorkflowRuns && s.backgroundReserveExhausted(repo, QuotaResourceREST, false) {
		return nil
	}
	stored, err := s.db.GetRepositoryByProviderID(ctx, hint.Provider, hint.Host, repo.PlatformExternalID)
	if err != nil {
		return err
	}
	if stored == nil || stored.Lifecycle != db.RepositoryLifecycleActive {
		return nil
	}
	repoID := stored.Repository.ID
	// Resolve the current route from the provider-verified catalog, preserving
	// the configured credentials and stable provider identity.
	repo.Owner, repo.Name = stored.Repository.Owner, stored.Repository.Name
	target := hint.Target
	if target == activityrelay.PullRequestChecks {
		mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, hint.Number)
		if err != nil {
			return err
		}
		if mr == nil || mr.State != db.MergeRequestStateOpen || mr.PlatformHeadSHA == "" {
			return nil
		}
		if budget := s.budgets[bucket]; budget != nil && !budget.CanSpend(2*wireAttemptsPerRequest) {
			return nil
		}
		defer s.beginProviderWork(ctx, bucket, archive.PriorityActiveDetail)()
		warnings, err := s.RefreshMRCIStatusForRepository(ctx, repo, repoID, hint.Number, mr.PlatformHeadSHA)
		if err != nil || len(warnings) != 0 {
			return errors.Join(err, errors.New("relay CI refresh incomplete"))
		}
	}
	cost := PRDetailWorstCase * wireAttemptsPerRequest
	if target == activityrelay.Issue {
		cost = IssueDetailWorstCase * wireAttemptsPerRequest
	}
	switch target {
	case activityrelay.WorkflowRuns:
		// Notify open Actions views; only a visible view reads workflow runs.
	case activityrelay.PullRequest, activityrelay.Issue:
		if budget := s.budgets[bucket]; budget != nil && !budget.CanSpend(cost) {
			return nil
		}
		feature := platform.RepositoryFeatureMergeRequests
		if target == activityrelay.Issue {
			feature = platform.RepositoryFeatureIssues
		}
		probe, due := s.beginRepositoryFeatureProbe(ctx, repo, feature)
		if !due {
			return nil
		}
		providerAttempted := false
		if target == activityrelay.PullRequest {
			err = s.syncMRForRepoResolved(ctx, repo, hint.Number, false, &providerAttempted, nil, nil, nil, &repoID)
			if _, onlyDiffFailed := err.(*DiffSyncError); onlyDiffFailed { //nolint:errorlint // joined hard failures must surface
				err = nil
			}
		} else {
			err = s.syncIssueForRepo(platformgithub.WithUnconditionalRead(ctx), repo, hint.Number, &providerAttempted)
		}
		disabled := err != nil && s.recordGitHubRepositoryFeatureDisabled(repo, feature, err)
		if providerAttempted {
			probe.release()
		} else {
			probe.abandon()
		}
		if disabled {
			return nil
		}
	case activityrelay.Repository:
		if budget := s.budgets[bucket]; budget != nil && !budget.CanSpend(cost) {
			return nil
		}
		err = s.syncRepo(ctx, repo)
	case activityrelay.RepositoryRefs:
		if budget := s.budgets[bucket]; budget != nil && !budget.CanSpend(cost) {
			return nil
		}
		err = s.refreshRelayRefs(ctx, repo)
	}
	if err != nil {
		return fmt.Errorf("refresh %s/%s/%d: %w", hint.RepositoryID, target, hint.Number, err)
	}
	if s.onRelayRefresh != nil {
		s.onRelayRefresh(ctx, repoID, target, hint.Number)
	}
	return nil
}

func (s *Syncer) refreshRelayRefs(ctx context.Context, repo RepoRef) error {
	bucket, err := s.bucketKeyForRepo(repo, false)
	if err != nil {
		return err
	}
	defer s.beginProviderWork(ctx, bucket, archive.PriorityNormalIndex)()
	repo, repoID, fence, found, err := s.reconcileRepoForDirectSync(ctx, repo)
	if err != nil || !found {
		return err
	}
	ctx = s.db.WithRepositoryRouteFence(ctx, platformdb.DBRepoIdentity(platformRepoRef(repo)), fence)
	ctx = withCloneRepositoryIdentity(ctx, repo)
	if s.clones != nil {
		branch := s.defaultBranchForActivity(ctx, repoID, repo)
		previous, err := s.db.GetBranchTip(ctx, repoID, branch)
		if err != nil {
			return err
		}
		if err := s.ensureCloneForRoute(ctx, repo, repoID, fence); err != nil {
			return err
		}
		s.syncDefaultBranchActivity(ctx, repo, repoID, branch, previous)
	}
	client, err := s.clientFor(repo)
	if err != nil {
		return err
	}
	prs, err := client.ListOpenPullRequests(ctx, repo.Owner, repo.Name)
	if platformgithub.IsNotModified(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, pr := range prs {
		if err := s.indexUpsertMR(ctx, client, repo, repoID, pr); err != nil {
			return err
		}
	}
	return nil
}

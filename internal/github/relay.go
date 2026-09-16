package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.kenn.io/forge/internal/activityrelay"
	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// RelayStatus is a process-local view of the feed, carried with sync status.
type RelayStatus struct {
	LastPollAt  time.Time       `json:"last_poll_at,omitzero"`
	Unavailable bool            `json:"unavailable"`
	Recent      []RelayActivity `json:"recent"`
}

type RelayActivity struct {
	Cursor     string    `json:"cursor"`
	Repository string    `json:"repository"`
	Target     string    `json:"target" enum:"pull_request,pull_request_checks,issue,repository_refs,repository"`
	Number     int       `json:"number"`
	ReceivedAt time.Time `json:"received_at"`
}

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

// SetOnRelayRefresh installs the callback before the consumer starts.
func (s *Syncer) SetOnRelayRefresh(fn func(context.Context, int64, string, int)) {
	s.onRelayRefresh = fn
}

// PollRelay is owned by one daemon background loop. New deliveries received
// during provider work remain on the relay for the next poll, so completing a
// saved target cannot consume a newer event that has not been read yet.
func (s *Syncer) PollRelay(ctx context.Context, relayURL string, client *http.Client) error {
	if !s.SyncEnabled() {
		return nil
	}
	pollErr := s.readRelayPages(ctx, relayURL, client)
	s.updateRelayStatus(func(status *RelayStatus) {
		status.LastPollAt = s.now().UTC()
		status.Unavailable = pollErr != nil
	})
	// Pending work must also progress while the relay is unavailable.
	return errors.Join(pollErr, s.drainRelayHints(ctx, relayURL))
}

func (s *Syncer) readRelayPages(ctx context.Context, relayURL string, client *http.Client) error {
	cursor, err := s.db.RelayCursor(ctx, relayURL)
	if err != nil {
		return err
	}
	for ctx.Err() == nil && s.SyncEnabled() {
		page, err := activityrelay.Fetch(ctx, client, relayURL, cursor)
		if err != nil {
			return err
		}
		hints := []activityrelay.Hint{}
		var recent []RelayActivity
		if page.ResyncRequired || page.Code == "cursor_expired" {
			for _, repo := range s.TrackedRepos() {
				if repoPlatform(repo) != platform.KindGitHub || repoHost(repo) != "github.com" || repo.Archived {
					continue
				}
				if repo.PlatformExternalID != "" {
					hints = append(hints, activityrelay.Hint{
						Provider: "github", Host: "github.com", RepositoryID: repo.PlatformExternalID, Target: activityrelay.Repository,
					})
				}
			}
			if page.Code == "cursor_expired" {
				page.NextCursor = page.ResetCursor
			}
		} else {
			for _, event := range page.Events {
				if repo, ok := s.trackedRepoByProviderID(platform.KindGitHub, event.Host, event.RepositoryID); ok && !repo.Archived {
					hints = append(hints, event.Hint)
					recent = append(recent, RelayActivity{
						Cursor: event.Cursor, Repository: repo.Owner + "/" + repo.Name,
						Target: event.Target, Number: event.Number, ReceivedAt: s.now().UTC(),
					})
				}
			}
		}
		if err := s.db.SaveRelayPage(ctx, relayURL, page.NextCursor, hints); err != nil {
			return err
		}
		if len(recent) > 0 {
			s.updateRelayStatus(func(status *RelayStatus) {
				for _, event := range recent {
					status.Recent = append([]RelayActivity{event}, status.Recent[:min(19, len(status.Recent))]...)
				}
			})
		}
		cursor = page.NextCursor
		if !page.HasMore {
			return nil
		}
	}
	return ctx.Err()
}

func (s *Syncer) drainRelayHints(ctx context.Context, relayURL string) error {
	hints, err := s.db.PendingRelayHints(ctx, relayURL)
	if err != nil {
		return err
	}
	var failures error
	for _, hint := range hints {
		if ctx.Err() != nil || !s.SyncEnabled() {
			return errors.Join(failures, ctx.Err())
		}
		done, err := s.refreshRelayHint(WithSyncBudget(ctx), relayURL, &hint)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("refresh relay target %s/%s/%d: %w", hint.RepositoryID, hint.Target, hint.Number, err))
			continue
		}
		if done {
			if err := s.db.CompleteRelayHint(ctx, relayURL, hint); err != nil {
				return errors.Join(failures, err)
			}
		}
	}
	return failures
}

func (s *Syncer) refreshRelayHint(ctx context.Context, relayURL string, hint *activityrelay.Hint) (bool, error) {
	repo, tracked := s.trackedRepoByProviderID(platform.KindGitHub, hint.Host, hint.RepositoryID)
	if !tracked || repo.Archived {
		return true, nil
	}
	bucket, err := s.bucketKeyForRepo(repo, false)
	if err != nil {
		return false, err
	}
	cost := PRDetailWorstCase * wireAttemptsPerRequest
	switch hint.Target {
	case activityrelay.PullRequestChecks:
		cost = 2 * wireAttemptsPerRequest
	case activityrelay.Issue:
		cost = IssueDetailWorstCase * wireAttemptsPerRequest
	}
	if budget := s.budgets[bucket]; budget != nil && !budget.CanSpend(cost) {
		return false, nil
	}
	if s.backgroundReserveExhausted(repo, QuotaResourceREST, false) {
		return false, nil
	}
	stored, err := s.db.GetRepositoryByProviderID(ctx, hint.Provider, hint.Host, repo.PlatformExternalID)
	if err != nil {
		return false, err
	}
	if stored == nil || stored.Lifecycle != db.RepositoryLifecycleActive {
		return false, nil
	}
	repoID := stored.Repository.ID
	// Resolve the current route from the provider-verified catalog, preserving
	// the configured credentials and stable provider identity.
	repo.Owner, repo.Name = stored.Repository.Owner, stored.Repository.Name
	target := hint.Target
	if target == activityrelay.PullRequestChecks {
		mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, hint.Number)
		if err != nil {
			return false, err
		}
		if mr == nil || mr.PlatformHeadSHA == "" {
			target = activityrelay.PullRequest
			hint.Target = target
			cursor, err := s.db.RelayCursor(ctx, relayURL)
			if err != nil {
				return false, err
			}
			if err := s.db.SaveRelayPage(ctx, relayURL, cursor, []activityrelay.Hint{*hint}); err != nil {
				return false, err
			}
			if budget := s.budgets[bucket]; budget != nil && !budget.CanSpend(PRDetailWorstCase*wireAttemptsPerRequest) {
				return false, nil
			}
		} else {
			defer s.beginProviderWork(ctx, bucket, archive.PriorityActiveDetail)()
			warnings, err := s.RefreshMRCIStatusForRepository(ctx, repo, repoID, hint.Number, mr.PlatformHeadSHA)
			if err != nil || len(warnings) != 0 {
				return false, errors.Join(err, errors.New("relay CI refresh incomplete"))
			}
		}
	}
	switch target {
	case activityrelay.PullRequest, activityrelay.Issue:
		feature := platform.RepositoryFeatureMergeRequests
		if target == activityrelay.Issue {
			feature = platform.RepositoryFeatureIssues
		}
		probe, due := s.beginRepositoryFeatureProbe(ctx, repo, feature)
		if !due {
			return false, nil
		}
		providerAttempted := false
		if target == activityrelay.PullRequest {
			err = s.syncMRForRepoResolved(ctx, repo, hint.Number, false, &providerAttempted, nil, nil, nil, &repoID)
			if _, onlyDiffFailed := err.(*DiffSyncError); onlyDiffFailed { //nolint:errorlint // joined hard failures must remain pending
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
			return false, nil
		}
	case activityrelay.Repository:
		err = s.syncRepo(ctx, repo)
	case activityrelay.RepositoryRefs:
		err = s.refreshRelayRefs(ctx, repo)
	}
	if err != nil {
		return false, err
	}
	if s.onRelayRefresh != nil {
		s.onRelayRefresh(ctx, repoID, target, hint.Number)
	}
	return true, nil
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

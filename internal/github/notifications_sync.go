package github

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	gh "github.com/google/go-github/v88/github"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

const (
	defaultNotificationPropagationMaxAttempts = 10
	notificationSyncSinceOverlap              = 5 * time.Minute
	notificationFullSyncInterval              = time.Hour
)

type NotificationSyncStatus struct {
	Running        bool
	LastStartedAt  time.Time
	LastFinishedAt time.Time
	LastError      string
}

type notificationThreadGetter interface {
	GetNotificationThread(context.Context, string) (NotificationThread, error)
}

type notificationReadRateReserveBypasser interface {
	bypassNotificationReadRateReserve() bool
}

func notificationBypassesReadRateReserve(client notificationClient) bool {
	if bypasser, ok := client.(notificationReadRateReserveBypasser); ok {
		return bypasser.bypassNotificationReadRateReserve()
	}
	if legacy, ok := client.(interface{ GitHubClient() Client }); ok {
		if inner := legacy.GitHubClient(); inner != nil {
			if bypasser, ok := inner.(notificationReadRateReserveBypasser); ok {
				return bypasser.bypassNotificationReadRateReserve()
			}
		}
	}
	return false
}

// notificationThreadGetterFor resolves the optional thread-refetch
// capability used by the reopen-on-remote-activity check. The GitHub
// provider exposes it on its inner REST client; other providers may
// implement it directly once their notification support lands.
func notificationThreadGetterFor(client notificationClient) (notificationThreadGetter, bool) {
	if getter, ok := client.(notificationThreadGetter); ok {
		return getter, true
	}
	if legacy, ok := client.(interface{ GitHubClient() Client }); ok {
		if inner := legacy.GitHubClient(); inner != nil {
			if getter, ok := inner.(notificationThreadGetter); ok {
				return getter, true
			}
		}
	}
	return nil, false
}

func (s *Syncer) RunNotificationSync(ctx context.Context) error {
	if !s.BeginNotificationSync() {
		return nil
	}
	err := s.SyncNotifications(ctx)
	s.FinishNotificationSync(err)
	// Nudge listeners (the SSE hub) even on a partial error: a run that
	// errored on one host can still have inserted rows for another, and the
	// reload it triggers is idempotent.
	if s.onNotificationSyncComplete != nil {
		s.onNotificationSyncComplete()
	}
	return err
}

func (s *Syncer) BeginNotificationSync() bool {
	s.notificationSyncMu.Lock()
	defer s.notificationSyncMu.Unlock()
	if s.notificationSync.Running {
		return false
	}
	s.notificationSync.Running = true
	s.notificationSync.LastStartedAt = time.Now().UTC()
	s.notificationSync.LastError = ""
	return true
}

func (s *Syncer) FinishNotificationSync(err error) {
	s.notificationSyncMu.Lock()
	defer s.notificationSyncMu.Unlock()
	s.notificationSync.Running = false
	s.notificationSync.LastFinishedAt = time.Now().UTC()
	if err != nil {
		s.notificationSync.LastError = err.Error()
	}
}

func (s *Syncer) NotificationSyncStatus() NotificationSyncStatus {
	s.notificationSyncMu.RLock()
	defer s.notificationSyncMu.RUnlock()
	return s.notificationSync
}

func (s *Syncer) SyncNotifications(ctx context.Context) error {
	ctx = WithSyncBudget(ctx)
	repos := s.TrackedRepos()
	tracked := make(map[string]RepoRef, len(repos))
	for _, repo := range repos {
		platformName := string(repoPlatform(repo))
		host := normalizedPlatformHost(repo.PlatformHost)
		trackedRepo := RepoRef{
			Platform:     repoPlatform(repo),
			Owner:        strings.ToLower(repo.Owner),
			Name:         strings.ToLower(repo.Name),
			PlatformHost: host,
		}
		dbRepo, err := s.db.GetRepoByIdentity(ctx, db.RepoIdentity{
			Platform:     platformName,
			PlatformHost: host,
			Owner:        repo.Owner,
			Name:         repo.Name,
			RepoPath:     repo.RepoPath,
		})
		if err != nil {
			return fmt.Errorf("load notification repo identity for %s/%s on %s/%s: %w", repo.Owner, repo.Name, platformName, host, err)
		}
		if dbRepo != nil {
			trackedRepo.RepoID = dbRepo.ID
		}
		tracked[notificationRepoKey(platformName, host, repo.Owner, repo.Name)] = trackedRepo
	}
	clients := s.notificationClients()
	var errs []error
	for _, entry := range clients {
		if err := s.syncNotificationsForHost(ctx, entry.platform, entry.host, entry.client, tracked); err != nil {
			errs = append(errs, err)
		}
	}
	if err := s.db.MarkClosedLinkedNotificationsDone(ctx, time.Now().UTC()); err != nil {
		errs = append(errs, fmt.Errorf("mark closed linked notifications done: %w", err))
	}
	return errors.Join(errs...)
}

// notificationClient is the provider surface the notification sync
// engine needs: list threads and propagate read acks. Providers gate
// support through Capabilities().ReadNotifications and
// NotificationMutation; non-supporting providers ship stubs that
// return unsupported_capability errors until filled in.
type notificationClient interface {
	platform.NotificationReader
	platform.NotificationMutator
}

type notificationHostClient struct {
	platform platform.Kind
	host     string
	client   notificationClient
}

func (s *Syncer) notificationClients() []notificationHostClient {
	providers := s.clients.Providers()
	clients := make([]notificationHostClient, 0, len(providers))
	for _, provider := range providers {
		caps := provider.Capabilities()
		if !caps.ReadNotifications || !caps.NotificationMutation {
			continue
		}
		client, ok := provider.(notificationClient)
		if !ok {
			continue
		}
		clients = append(clients, notificationHostClient{platform: provider.Platform(), host: normalizedPlatformHost(provider.Host()), client: client})
	}
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].platform != clients[j].platform {
			return clients[i].platform < clients[j].platform
		}
		return clients[i].host < clients[j].host
	})
	return clients
}

func (s *Syncer) notificationClientForHost(kind platform.Kind, host string) (notificationClient, bool) {
	provider, err := s.clients.Provider(kind, normalizedPlatformHost(host))
	if err != nil {
		return nil, false
	}
	caps := provider.Capabilities()
	if !caps.ReadNotifications || !caps.NotificationMutation {
		return nil, false
	}
	client, ok := provider.(notificationClient)
	if !ok {
		return nil, false
	}
	return client, true
}

func (s *Syncer) syncNotificationsForHost(ctx context.Context, kind platform.Kind, host string, client notificationClient, tracked map[string]RepoRef) error {
	startedAt := time.Now().UTC()
	platformName := string(kind)
	trackedReposKey := notificationTrackedReposKey(platformName, host, tracked)
	trackedRepos := notificationTrackedRepos(platformName, host, tracked)
	if len(trackedRepos) == 0 {
		return nil
	}
	watermark, err := s.db.GetNotificationSyncWatermark(ctx, platformName, host, trackedReposKey)
	if err != nil {
		return fmt.Errorf("load notification sync watermark for %s: %w", host, err)
	}
	var since *time.Time
	fullSync := shouldFullSyncNotifications(startedAt, watermark)
	if watermark != nil && !fullSync {
		value := watermark.LastSuccessfulSyncAt.Add(-notificationSyncSinceOverlap).UTC()
		since = &value
	}
	participatingIDs, err := s.listParticipatingNotificationIDs(ctx, host, client, trackedRepos, since)
	if err != nil {
		return err
	}
	for _, repo := range trackedRepos {
		for page := 1; ; page++ {
			if err := s.ensureNotificationPageBudget(host, client); err != nil {
				return err
			}
			threads, hasNext, err := client.ListNotifications(ctx, NotificationListOptions{
				All:       true,
				Since:     since,
				Page:      page,
				RepoOwner: repo.Owner,
				RepoName:  repo.Name,
			})
			if err != nil {
				return fmt.Errorf("list notifications for %s/%s on %s page %d: %w", repo.Owner, repo.Name, host, page, err)
			}
			notifications := make([]db.Notification, 0, len(threads))
			now := time.Now().UTC()
			for _, thread := range threads {
				if thread.RepoOwner == "" {
					thread.RepoOwner = repo.Owner
				}
				if thread.RepoName == "" {
					thread.RepoName = repo.Name
				}
				if participatingIDs[thread.ID] {
					thread.Participating = true
				}
				key := notificationRepoKey(platformName, host, thread.RepoOwner, thread.RepoName)
				repo, ok := tracked[key]
				if !ok {
					continue
				}
				// Only notifications anchored to a PR or issue have an in-app
				// destination and meaningful triage. CI/check-suite, discussion,
				// release, and other subjects are worthless in middleman, so do
				// not persist them.
				if (thread.ItemType != "pr" && thread.ItemType != "issue") || thread.ItemNumber == nil {
					continue
				}
				// "author" notifications fire for any activity on a thread the
				// user opened ("Your thread"); the triggering comment/review/state
				// change is already its own row in the feed, so they are pure
				// duplication. Drop them while keeping comment, subscribed, and
				// the attention-requesting reasons (mention, review_requested, ...).
				if thread.Reason == "author" {
					continue
				}
				notification, err := s.notificationToDB(ctx, host, repo, thread, now)
				if err != nil {
					return fmt.Errorf("normalize notification %s for %s/%s on %s page %d: %w", thread.ID, repo.Owner, repo.Name, host, page, err)
				}
				notifications = append(notifications, notification)
			}
			if err := s.db.UpsertNotifications(ctx, notifications); err != nil {
				return fmt.Errorf("upsert notifications for %s/%s on %s page %d: %w", repo.Owner, repo.Name, host, page, err)
			}
			if !hasNext {
				break
			}
		}
	}
	lastFullSyncAt := watermarkLastFullSyncAt(watermark, startedAt, fullSync)
	if err := s.db.UpdateNotificationSyncWatermark(ctx, platformName, host, startedAt, lastFullSyncAt, "", trackedReposKey); err != nil {
		return fmt.Errorf("store notification sync watermark for %s: %w", host, err)
	}
	return nil
}

func (s *Syncer) listParticipatingNotificationIDs(
	ctx context.Context,
	host string,
	client notificationClient,
	trackedRepos []RepoRef,
	since *time.Time,
) (map[string]bool, error) {
	participating := map[string]bool{}
	for _, repo := range trackedRepos {
		for page := 1; ; page++ {
			if err := s.ensureNotificationPageBudget(host, client); err != nil {
				return nil, err
			}
			threads, hasNext, err := client.ListNotifications(ctx, NotificationListOptions{
				All:           true,
				Participating: true,
				Since:         since,
				Page:          page,
				RepoOwner:     repo.Owner,
				RepoName:      repo.Name,
			})
			if err != nil {
				return nil, fmt.Errorf("list participating notifications for %s/%s on %s page %d: %w", repo.Owner, repo.Name, host, page, err)
			}
			for _, thread := range threads {
				if thread.ID != "" {
					participating[thread.ID] = true
				}
			}
			if !hasNext {
				break
			}
		}
	}
	return participating, nil
}

func (s *Syncer) ensureNotificationPageBudget(host string, client notificationClient) error {
	if budget := s.budgets[host]; budget != nil && !budget.CanSpend(1) {
		return fmt.Errorf("notification sync paused for %s: sync budget exhausted", host)
	}
	if notificationBypassesReadRateReserve(client) {
		return nil
	}
	if rateTracker := s.rateTrackers[host]; rateTracker != nil && rateTracker.IsPaused() {
		return fmt.Errorf("notification sync paused for %s: rate reserve exhausted", host)
	}
	return nil
}

func shouldFullSyncNotifications(now time.Time, watermark *db.NotificationSyncWatermark) bool {
	if watermark == nil || watermark.LastFullSyncAt == nil {
		return true
	}
	return !watermark.LastFullSyncAt.Add(notificationFullSyncInterval).After(now)
}

func watermarkLastFullSyncAt(watermark *db.NotificationSyncWatermark, startedAt time.Time, fullSync bool) *time.Time {
	if fullSync {
		value := startedAt.UTC()
		return &value
	}
	if watermark == nil || watermark.LastFullSyncAt == nil {
		return nil
	}
	value := watermark.LastFullSyncAt.UTC()
	return &value
}

func notificationTrackedReposKey(platformName, host string, tracked map[string]RepoRef) string {
	prefix := platformName + "/" + normalizedPlatformHost(host) + "/"
	keys := make([]string, 0, len(tracked))
	for key := range tracked {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, "\n")
}

func notificationTrackedRepos(platformName, host string, tracked map[string]RepoRef) []RepoRef {
	prefix := platformName + "/" + normalizedPlatformHost(host) + "/"
	keys := make([]string, 0, len(tracked))
	for key := range tracked {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	repos := make([]RepoRef, 0, len(keys))
	for _, key := range keys {
		repos = append(repos, tracked[key])
	}
	return repos
}

func notificationRepoKey(platformName, host, owner, name string) string {
	return strings.ToLower(platformName) + "/" + normalizedPlatformHost(host) + "/" + strings.ToLower(owner) + "/" + strings.ToLower(name)
}

func (s *Syncer) notificationToDB(ctx context.Context, host string, repo RepoRef, thread NotificationThread, syncedAt time.Time) (db.Notification, error) {
	notification := notificationToDB(host, repo, thread, syncedAt)
	if notification.ItemAuthor != "" || notification.ItemNumber == nil {
		return notification, nil
	}
	if repo.RepoID == 0 {
		return notification, nil
	}
	switch notification.ItemType {
	case "pr":
		mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.RepoID, *notification.ItemNumber)
		if err != nil || mr == nil {
			return notification, err
		}
		notification.ItemAuthor = mr.Author
	case "issue":
		issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.RepoID, *notification.ItemNumber)
		if err != nil || issue == nil {
			return notification, err
		}
		notification.ItemAuthor = issue.Author
	}
	return notification, nil
}

func notificationToDB(host string, repo RepoRef, thread NotificationThread, syncedAt time.Time) db.Notification {
	var repoID *int64
	if repo.RepoID != 0 {
		repoID = &repo.RepoID
	}
	return db.Notification{
		Platform:                 string(repoPlatform(repo)),
		PlatformHost:             normalizedPlatformHost(host),
		PlatformNotificationID:   thread.ID,
		RepoID:                   repoID,
		RepoOwner:                strings.ToLower(repo.Owner),
		RepoName:                 strings.ToLower(repo.Name),
		SubjectType:              thread.SubjectType,
		SubjectTitle:             thread.SubjectTitle,
		SubjectURL:               thread.SubjectURL,
		SubjectLatestCommentURL:  thread.SubjectLatestCommentURL,
		WebURL:                   thread.WebURL,
		ItemNumber:               thread.ItemNumber,
		ItemType:                 thread.ItemType,
		ItemAuthor:               thread.ItemAuthor,
		Reason:                   thread.Reason,
		Unread:                   thread.Unread,
		Participating:            thread.Participating,
		SourceUpdatedAt:          thread.UpdatedAt,
		SourceLastAcknowledgedAt: thread.LastReadAt,
		SyncedAt:                 syncedAt,
	}
}

func (s *Syncer) ProcessQueuedNotificationReads(ctx context.Context, kind platform.Kind, host string, batchSize int) error {
	ctx = WithSyncBudget(ctx)
	if batchSize <= 0 {
		batchSize = 25
	}
	host = normalizedPlatformHost(host)
	client, ok := s.notificationClientForHost(kind, host)
	if !ok {
		return fmt.Errorf("%s notification client for host %s not configured", kind, host)
	}
	queued, err := s.db.ListQueuedNotificationAcks(ctx, string(kind), host, batchSize, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, notification := range queued {
		current, err := s.db.NotificationAckPropagationCurrent(ctx, notification.ID, notification.SourceAckQueuedAt, notification.SourceUpdatedAt)
		if err != nil {
			return err
		}
		if !current {
			continue
		}
		remote, advanced, err := s.fetchAdvancedNotificationThread(ctx, host, client, notification)
		if err != nil {
			// The pre-ack refetch spends the same upstream budget as the
			// mark-read call, so a rate limit here must defer the queued ack
			// rather than retry the same due row every tick. Only this refetch
			// API error routes through backoff; the persistence error below
			// surfaces normally so a failed local refresh is not hidden.
			if deferErr := s.deferQueuedNotificationAckOnError(ctx, kind, host, notification, err); deferErr != nil {
				return deferErr
			}
			continue
		}
		if advanced {
			// New activity arrived since the read was queued. Refresh local
			// state from the upstream thread, preserving its read/unread flag
			// so a thread the user already read upstream is not resurrected,
			// and skip the mark-read so we never ack unseen activity.
			if err := s.persistReopenedNotification(ctx, host, notification, remote, false); err != nil {
				return err
			}
			continue
		}
		if err := client.MarkNotificationThreadRead(ctx, notification.PlatformNotificationID); err != nil {
			if deferErr := s.deferQueuedNotificationAckOnError(ctx, kind, host, notification, err); deferErr != nil {
				return deferErr
			}
			continue
		}
		// Reconciliation refetch: if the thread advanced between the pre-ack
		// refetch and the mark-read, our PATCH may have read newer activity
		// the user never saw, so force it back to unread. Do not clear the
		// queued ack until this refetch proves there was no newer activity.
		remote, advanced, err = s.fetchAdvancedNotificationThread(ctx, host, client, notification)
		if err != nil {
			if deferErr := s.reopenNotificationAfterPostAckRefetchError(ctx, kind, host, notification, err); deferErr != nil {
				return deferErr
			}
			continue
		}
		if advanced {
			if err := s.persistReopenedNotification(ctx, host, notification, remote, true); err != nil {
				return err
			}
			continue
		}
		syncedAt := time.Now().UTC()
		if err := s.db.MarkNotificationAckPropagationResult(ctx, notification.ID, notification.SourceAckQueuedAt, notification.SourceUpdatedAt, &syncedAt, "", nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) reopenNotificationAfterPostAckRefetchError(
	ctx context.Context,
	kind platform.Kind,
	host string,
	notification db.Notification,
	cause error,
) error {
	if err := s.db.ReopenNotificationAckPropagation(ctx, notification.ID, notification.SourceAckQueuedAt, notification.SourceUpdatedAt); err != nil {
		return err
	}
	if nextAttemptAt, ok := notificationReadRateLimitNextAttempt(cause, time.Now().UTC()); ok {
		if recordErr := s.db.DeferQueuedNotificationAcks(ctx, string(kind), host, nextAttemptAt, "rate_limited"); recordErr != nil {
			return recordErr
		}
		return fmt.Errorf("notification read propagation rate limited for host %s: %w", host, cause)
	}
	return nil
}

// deferQueuedNotificationAckOnError records backoff after a propagation step
// (thread refetch or mark-read) fails for a queued ack. Rate-limit errors
// defer every queued ack for the host and return an error so the batch stops
// without burning the shared upstream budget on a row that cannot make
// progress; any other error records a per-row next-attempt time so only this
// row backs off. A nil return means the ack was deferred and the caller should
// advance to the next queued row.
func (s *Syncer) deferQueuedNotificationAckOnError(
	ctx context.Context,
	kind platform.Kind,
	host string,
	notification db.Notification,
	cause error,
) error {
	now := time.Now().UTC()
	if nextAttemptAt, ok := notificationReadRateLimitNextAttempt(cause, now); ok {
		if recordErr := s.db.DeferQueuedNotificationAcks(ctx, string(kind), host, nextAttemptAt, "rate_limited"); recordErr != nil {
			return recordErr
		}
		return fmt.Errorf("notification read propagation rate limited for host %s: %w", host, cause)
	}
	errText := cause.Error()
	var nextAttemptAt *time.Time
	if notification.SourceAckAttempts+1 >= defaultNotificationPropagationMaxAttempts {
		errText = "max_attempts_exceeded"
	} else {
		next := now.Add(notificationReadBackoff(notification.SourceAckAttempts + 1))
		nextAttemptAt = &next
	}
	if recordErr := s.db.MarkNotificationAckPropagationResult(ctx, notification.ID, notification.SourceAckQueuedAt, notification.SourceUpdatedAt, nil, errText, nextAttemptAt); recordErr != nil {
		return recordErr
	}
	return nil
}

// fetchAdvancedNotificationThread refetches the upstream thread and reports
// whether it advanced past the locally recorded source_updated_at. A provider
// without the refetch capability or an unchanged thread reports advanced=false
// with no error. The returned error is always the refetch API error, so
// callers can route it through ack backoff; local persistence is handled
// separately by persistReopenedNotification so its failures are not mistaken
// for an upstream/ack failure.
func (s *Syncer) fetchAdvancedNotificationThread(
	ctx context.Context,
	host string,
	client notificationClient,
	notification db.Notification,
) (NotificationThread, bool, error) {
	getter, ok := notificationThreadGetterFor(client)
	if !ok {
		return NotificationThread{}, false, nil
	}
	remote, err := getter.GetNotificationThread(ctx, notification.PlatformNotificationID)
	if err != nil {
		return NotificationThread{}, false, fmt.Errorf("get notification thread %s for %s: %w", notification.PlatformNotificationID, host, err)
	}
	if !remote.UpdatedAt.After(notification.SourceUpdatedAt) {
		return NotificationThread{}, false, nil
	}
	return remote, true, nil
}

// persistReopenedNotification refreshes local state from an advanced upstream
// thread. forceUnread marks the row unread regardless of the refreshed flag:
// the post-ack reconciliation path sets it because our own mark-read has
// already flipped the upstream thread to read, so the refetch can no longer
// report the unseen activity as unread. The pre-ack path passes false so a
// thread the user already read upstream is not resurrected as unread.
func (s *Syncer) persistReopenedNotification(
	ctx context.Context,
	host string,
	notification db.Notification,
	remote NotificationThread,
	forceUnread bool,
) error {
	if remote.ID == "" {
		remote.ID = notification.PlatformNotificationID
	}
	if remote.RepoOwner == "" {
		remote.RepoOwner = notification.RepoOwner
	}
	if remote.RepoName == "" {
		remote.RepoName = notification.RepoName
	}
	if forceUnread {
		remote.Unread = true
	}
	// Preserve the original provider identity: notificationToDB keys off
	// repoPlatform(repo), so dropping Platform here would re-upsert the
	// refreshed notification under GitHub for any non-GitHub provider.
	repo := RepoRef{
		Platform:     platform.Kind(notification.Platform),
		Owner:        remote.RepoOwner,
		Name:         remote.RepoName,
		PlatformHost: host,
	}
	if notification.RepoID != nil {
		repo.RepoID = *notification.RepoID
	}
	refreshed, err := s.notificationToDB(ctx, host, repo, remote, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("normalize refreshed notification %s for %s: %w", notification.PlatformNotificationID, host, err)
	}
	if err := s.db.UpsertNotifications(ctx, []db.Notification{refreshed}); err != nil {
		return fmt.Errorf("upsert refreshed notification %s for %s: %w", notification.PlatformNotificationID, host, err)
	}
	return nil
}

func notificationReadRateLimitNextAttempt(err error, now time.Time) (time.Time, bool) {
	var rateLimitErr *gh.RateLimitError
	if errors.As(err, &rateLimitErr) {
		resetAt := rateLimitErr.Rate.Reset.UTC()
		if resetAt.After(now) {
			return resetAt, true
		}
		return now.Add(notificationReadBackoff(1)), true
	}
	var abuseRateLimitErr *gh.AbuseRateLimitError
	if errors.As(err, &abuseRateLimitErr) {
		if abuseRateLimitErr.RetryAfter != nil && *abuseRateLimitErr.RetryAfter > 0 {
			return now.Add(*abuseRateLimitErr.RetryAfter), true
		}
		return now.Add(notificationReadBackoff(1)), true
	}
	return time.Time{}, false
}

func (s *Syncer) ProcessQueuedNotificationReadsForAllHosts(ctx context.Context, batchSize int) error {
	var errs []error
	for _, entry := range s.notificationClients() {
		if err := s.ProcessQueuedNotificationReads(ctx, entry.platform, entry.host, batchSize); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func notificationReadBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 6 {
		attempts = 6
	}
	return time.Duration(1<<uint(attempts-1)) * time.Minute
}

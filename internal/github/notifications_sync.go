package github

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	gh "github.com/google/go-github/v84/github"
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
	repos := s.TrackedRepos()
	tracked := make(map[string]RepoRef, len(repos))
	for _, repo := range repos {
		platformName := string(repoPlatform(repo))
		host := normalizedPlatformHost(repo.PlatformHost)
		tracked[notificationRepoKey(platformName, host, repo.Owner, repo.Name)] = RepoRef{
			Platform:     repoPlatform(repo),
			Owner:        strings.ToLower(repo.Owner),
			Name:         strings.ToLower(repo.Name),
			PlatformHost: host,
		}
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
	participatingIDs, err := listParticipatingNotificationIDs(ctx, host, client, since)
	if err != nil {
		return err
	}
	page := 1
	for {
		threads, hasNext, err := client.ListNotifications(ctx, NotificationListOptions{All: true, Since: since, Page: page})
		if err != nil {
			return fmt.Errorf("list notifications for %s page %d: %w", host, page, err)
		}
		notifications := make([]db.Notification, 0, len(threads))
		now := time.Now().UTC()
		for _, thread := range threads {
			if participatingIDs[thread.ID] {
				thread.Participating = true
			}
			key := notificationRepoKey(platformName, host, thread.RepoOwner, thread.RepoName)
			repo, ok := tracked[key]
			if !ok {
				continue
			}
			notification, err := s.notificationToDB(ctx, host, repo, thread, now)
			if err != nil {
				return fmt.Errorf("normalize notification %s for %s page %d: %w", thread.ID, host, page, err)
			}
			notifications = append(notifications, notification)
		}
		if err := s.db.UpsertNotifications(ctx, notifications); err != nil {
			return fmt.Errorf("upsert notifications for %s page %d: %w", host, page, err)
		}
		if !hasNext {
			lastFullSyncAt := watermarkLastFullSyncAt(watermark, startedAt, fullSync)
			if err := s.db.UpdateNotificationSyncWatermark(ctx, platformName, host, startedAt, lastFullSyncAt, "", trackedReposKey); err != nil {
				return fmt.Errorf("store notification sync watermark for %s: %w", host, err)
			}
			return nil
		}
		page++
	}
}

func listParticipatingNotificationIDs(ctx context.Context, host string, client notificationClient, since *time.Time) (map[string]bool, error) {
	participating := map[string]bool{}
	page := 1
	for {
		threads, hasNext, err := client.ListNotifications(ctx, NotificationListOptions{All: true, Participating: true, Since: since, Page: page})
		if err != nil {
			return nil, fmt.Errorf("list participating notifications for %s page %d: %w", host, page, err)
		}
		for _, thread := range threads {
			if thread.ID != "" {
				participating[thread.ID] = true
			}
		}
		if !hasNext {
			return participating, nil
		}
		page++
	}
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

func notificationRepoKey(platformName, host, owner, name string) string {
	return strings.ToLower(platformName) + "/" + normalizedPlatformHost(host) + "/" + strings.ToLower(owner) + "/" + strings.ToLower(name)
}

func (s *Syncer) notificationToDB(ctx context.Context, host string, repo RepoRef, thread NotificationThread, syncedAt time.Time) (db.Notification, error) {
	notification := notificationToDB(host, repo, thread, syncedAt)
	if notification.ItemAuthor != "" || notification.ItemNumber == nil {
		return notification, nil
	}
	dbRepo, err := s.db.GetRepoByHostOwnerName(ctx, host, repo.Owner, repo.Name)
	if err != nil || dbRepo == nil {
		return notification, err
	}
	switch notification.ItemType {
	case "pr":
		mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, dbRepo.ID, *notification.ItemNumber)
		if err != nil || mr == nil {
			return notification, err
		}
		notification.ItemAuthor = mr.Author
	case "issue":
		issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, dbRepo.ID, *notification.ItemNumber)
		if err != nil || issue == nil {
			return notification, err
		}
		notification.ItemAuthor = issue.Author
	}
	return notification, nil
}

func notificationToDB(host string, repo RepoRef, thread NotificationThread, syncedAt time.Time) db.Notification {
	return db.Notification{
		Platform:                 string(repoPlatform(repo)),
		PlatformHost:             normalizedPlatformHost(host),
		PlatformNotificationID:   thread.ID,
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
		reopened, err := s.reopenIfNotificationThreadAdvanced(ctx, host, client, notification)
		if err != nil {
			return err
		}
		if reopened {
			continue
		}
		if err := client.MarkNotificationThreadRead(ctx, notification.PlatformNotificationID); err != nil {
			if nextAttemptAt, ok := notificationReadRateLimitNextAttempt(err, time.Now().UTC()); ok {
				if recordErr := s.db.DeferQueuedNotificationAcks(ctx, string(kind), host, nextAttemptAt, "rate_limited"); recordErr != nil {
					return recordErr
				}
				return fmt.Errorf("notification read propagation rate limited for host %s: %w", host, err)
			}
			errText := err.Error()
			var nextAttemptAt *time.Time
			if notification.SourceAckAttempts+1 >= defaultNotificationPropagationMaxAttempts {
				errText = "max_attempts_exceeded"
			} else {
				next := time.Now().UTC().Add(notificationReadBackoff(notification.SourceAckAttempts + 1))
				nextAttemptAt = &next
			}
			if recordErr := s.db.MarkNotificationAckPropagationResult(ctx, notification.ID, notification.SourceAckQueuedAt, notification.SourceUpdatedAt, nil, errText, nextAttemptAt); recordErr != nil {
				return recordErr
			}
			continue
		}
		syncedAt := time.Now().UTC()
		if err := s.db.MarkNotificationAckPropagationResult(ctx, notification.ID, notification.SourceAckQueuedAt, notification.SourceUpdatedAt, &syncedAt, "", nil); err != nil {
			return err
		}
		if _, err := s.reopenIfNotificationThreadAdvanced(ctx, host, client, notification); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) reopenIfNotificationThreadAdvanced(ctx context.Context, host string, client notificationClient, notification db.Notification) (bool, error) {
	getter, ok := notificationThreadGetterFor(client)
	if !ok {
		return false, nil
	}
	remote, err := getter.GetNotificationThread(ctx, notification.PlatformNotificationID)
	if err != nil {
		return false, fmt.Errorf("get notification thread %s for %s: %w", notification.PlatformNotificationID, host, err)
	}
	if !remote.UpdatedAt.After(notification.SourceUpdatedAt) {
		return false, nil
	}
	if remote.ID == "" {
		remote.ID = notification.PlatformNotificationID
	}
	if remote.RepoOwner == "" {
		remote.RepoOwner = notification.RepoOwner
	}
	if remote.RepoName == "" {
		remote.RepoName = notification.RepoName
	}
	remote.Unread = true
	// Preserve the original provider identity: notificationToDB keys off
	// repoPlatform(repo), so dropping Platform here would re-upsert the
	// refreshed notification under GitHub for any non-GitHub provider.
	repo := RepoRef{
		Platform:     platform.Kind(notification.Platform),
		Owner:        remote.RepoOwner,
		Name:         remote.RepoName,
		PlatformHost: host,
	}
	refreshed, err := s.notificationToDB(ctx, host, repo, remote, time.Now().UTC())
	if err != nil {
		return false, fmt.Errorf("normalize refreshed notification %s for %s: %w", notification.PlatformNotificationID, host, err)
	}
	if err := s.db.UpsertNotifications(ctx, []db.Notification{refreshed}); err != nil {
		return false, fmt.Errorf("upsert refreshed notification %s for %s: %w", notification.PlatformNotificationID, host, err)
	}
	return true, nil
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

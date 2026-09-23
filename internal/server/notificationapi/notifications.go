package notificationapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/itemapi"
)

const maxNotificationBulkIDs = 200

func (s *Handlers) ListNotifications(ctx context.Context, input *itemapi.ListNotificationsInput) (*itemapi.ListNotificationsOutput, error) {
	if !s.NotificationsEnabled() {
		return nil, huma.Error403Forbidden("notifications are disabled")
	}
	opts := db.ListNotificationsOpts{
		State:     input.State,
		Reasons:   input.Reason,
		ItemTypes: input.Type,
		Search:    input.Search,
		Sort:      input.Sort,
		Limit:     input.Limit,
		Offset:    input.Offset,
	}
	trackedRepos, err := s.trackedNotificationRepoFilters()
	if err != nil {
		return nil, huma.Error500InternalServerError("notification repo provider missing")
	}
	opts.Repos = trackedRepos
	if input.Repo != "" {
		platform, host, owner, name, ok := db.ParseNotificationRepo(input.Repo)
		if !ok {
			return nil, huma.Error400BadRequest("repo must be provider|platform_host/owner/name")
		}
		opts.Platform = platform
		opts.PlatformHost = host
		opts.RepoOwner = owner
		opts.RepoName = name
	}
	items, err := s.Db.ListNotifications(ctx, opts)
	if err != nil {
		return nil, huma.Error500InternalServerError("list notifications failed")
	}
	summary, err := s.Db.NotificationSummary(ctx, opts)
	if err != nil {
		return nil, huma.Error500InternalServerError("notification summary failed")
	}
	body := itemapi.NotificationsResponse{
		Items:   make([]itemapi.NotificationResponse, 0, len(items)),
		Summary: toNotificationSummaryResponse(summary),
		Sync:    s.currentNotificationSyncStatus(),
	}
	repoCache := map[int64]*db.Repo{}
	for _, item := range items {
		resp, err := s.ToNotificationResponse(ctx, item, repoCache)
		if err != nil {
			return nil, huma.Error500InternalServerError("notification repo lookup failed")
		}
		body.Items = append(body.Items, resp)
	}
	return &itemapi.ListNotificationsOutput{Body: body}, nil
}

func NotificationRepoFilters(repos []ghclient.RepoRef) ([]db.NotificationRepoFilter, error) {
	if len(repos) == 0 {
		return []db.NotificationRepoFilter{{}}, nil
	}
	filters := make([]db.NotificationRepoFilter, 0, len(repos))
	for _, repo := range repos {
		platformName := strings.TrimSpace(string(repo.Platform))
		if platformName == "" {
			return nil, errors.New("notification repo provider is required")
		}
		filters = append(filters, db.NotificationRepoFilter{
			Platform:     platformName,
			PlatformHost: repo.PlatformHost,
			RepoOwner:    repo.Owner,
			RepoName:     repo.Name,
		})
	}
	return filters, nil
}

func (s *Handlers) trackedNotificationRepoFilters() ([]db.NotificationRepoFilter, error) {
	if (*s.Syncer) == nil {
		return nil, nil
	}
	return NotificationRepoFilters((*s.Syncer).TrackedRepos())
}

func (s *Handlers) SyncNotifications(ctx context.Context, _ *struct{}) (*itemapi.AcceptedOutput, error) {
	if !s.NotificationsEnabled() {
		return nil, huma.Error403Forbidden("notifications are disabled")
	}
	if (*s.Syncer) == nil {
		return nil, huma.Error503ServiceUnavailable("syncer is not configured")
	}
	if ok := s.RunBackground(func(bgCtx context.Context) {
		_ = (*s.Syncer).RunNotificationSync(bgCtx)
	}); !ok {
		return nil, huma.Error503ServiceUnavailable("server is shutting down")
	}
	return &itemapi.AcceptedOutput{Status: http.StatusAccepted}, nil
}

func (s *Handlers) NotificationsEnabled() bool {
	return (*s.Cfg) != nil && (*s.Cfg).NotificationsEnabled()
}

func (s *Handlers) scopedNotificationIDs(ctx context.Context, ids []int64) ([]int64, error) {
	repos, err := s.trackedNotificationRepoFilters()
	if err != nil {
		return nil, err
	}
	return s.Db.FilterNotificationIDs(ctx, ids, repos)
}

func (s *Handlers) MarkClosedLinkedNotificationsDone(ctx context.Context) {
	if err := s.Db.MarkClosedLinkedNotificationsDone(ctx, (*s.Now)().UTC()); err != nil {
		slog.Warn("mark closed linked notifications done", "err", err)
	}
}

func (s *Handlers) currentNotificationSyncStatus() itemapi.NotificationSyncStatusResponse {
	if (*s.Syncer) == nil {
		return itemapi.NotificationSyncStatusResponse{}
	}
	syncStatus := (*s.Syncer).NotificationSyncStatus()
	status := itemapi.NotificationSyncStatusResponse{
		Running:   syncStatus.Running,
		LastError: syncStatus.LastError,
	}
	if !syncStatus.LastStartedAt.IsZero() {
		status.LastStartedAt = itemapi.FormatUTCRFC3339(syncStatus.LastStartedAt)
	}
	if !syncStatus.LastFinishedAt.IsZero() {
		status.LastFinishedAt = itemapi.FormatUTCRFC3339(syncStatus.LastFinishedAt)
	}
	return status
}

func (s *Handlers) MarkNotificationsRead(ctx context.Context, input *itemapi.NotificationBulkInput) (*itemapi.NotificationBulkOutput, error) {
	if !s.NotificationsEnabled() {
		return nil, huma.Error403Forbidden("notifications are disabled")
	}
	ids, err := validatedNotificationIDs(input.Body.IDs)
	if err != nil {
		return nil, err
	}
	now := (*s.Now)().UTC()
	scopedIDs, err := s.scopedNotificationIDs(ctx, ids)
	if err != nil {
		return nil, huma.Error500InternalServerError("mark notifications read failed")
	}
	succeeded, err := s.Db.QueueNotificationIDsRead(ctx, scopedIDs, now)
	if err != nil {
		return nil, huma.Error500InternalServerError("mark notifications read failed")
	}
	return &itemapi.NotificationBulkOutput{Body: notificationBulkResult(ids, succeeded, true)}, nil
}

func (s *Handlers) MarkNotificationsDone(ctx context.Context, input *itemapi.NotificationBulkInput) (*itemapi.NotificationBulkOutput, error) {
	if !s.NotificationsEnabled() {
		return nil, huma.Error403Forbidden("notifications are disabled")
	}
	ids, err := validatedNotificationIDs(input.Body.IDs)
	if err != nil {
		return nil, err
	}
	markRead := true
	if input.Body.MarkRead != nil {
		markRead = *input.Body.MarkRead
	}
	scopedIDs, err := s.scopedNotificationIDs(ctx, ids)
	if err != nil {
		return nil, huma.Error500InternalServerError("mark notifications done failed")
	}
	succeeded, err := s.Db.MarkNotificationsDone(ctx, scopedIDs, (*s.Now)().UTC(), markRead)
	if err != nil {
		return nil, huma.Error500InternalServerError("mark notifications done failed")
	}
	return &itemapi.NotificationBulkOutput{Body: notificationBulkResult(ids, succeeded, markRead)}, nil
}

func (s *Handlers) MarkNotificationsUndone(ctx context.Context, input *itemapi.NotificationBulkInput) (*itemapi.NotificationBulkOutput, error) {
	if !s.NotificationsEnabled() {
		return nil, huma.Error403Forbidden("notifications are disabled")
	}
	ids, err := validatedNotificationIDs(input.Body.IDs)
	if err != nil {
		return nil, err
	}
	scopedIDs, err := s.scopedNotificationIDs(ctx, ids)
	if err != nil {
		return nil, huma.Error500InternalServerError("mark notifications undone failed")
	}
	succeeded, err := s.Db.MarkNotificationsUndone(ctx, scopedIDs)
	if err != nil {
		return nil, huma.Error500InternalServerError("mark notifications undone failed")
	}
	if err := s.Db.MarkClosedLinkedNotificationsDone(ctx, (*s.Now)().UTC()); err != nil {
		return nil, huma.Error500InternalServerError("mark closed linked notifications done failed")
	}
	return &itemapi.NotificationBulkOutput{Body: notificationBulkResult(ids, succeeded, false)}, nil
}

func notificationBulkResult(requested, mutated []int64, queueRead bool) itemapi.NotificationBulkResponse {
	mutatedSet := make(map[int64]struct{}, len(mutated))
	for _, id := range mutated {
		mutatedSet[id] = struct{}{}
	}
	resp := itemapi.NotificationBulkResponse{
		Succeeded: make([]int64, 0, len(mutatedSet)),
		Failed:    []itemapi.NotificationBulkFailure{},
	}
	if queueRead {
		resp.Queued = make([]int64, 0, len(mutatedSet))
	}
	for _, id := range requested {
		if _, ok := mutatedSet[id]; ok {
			resp.Succeeded = append(resp.Succeeded, id)
			if queueRead {
				resp.Queued = append(resp.Queued, id)
			}
			continue
		}
		resp.Failed = append(resp.Failed, itemapi.NotificationBulkFailure{ID: id, Error: "notification not found"})
	}
	return resp
}

func validatedNotificationIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, huma.Error400BadRequest("ids must not be empty")
	}
	if len(ids) > maxNotificationBulkIDs {
		return nil, huma.Error400BadRequest("ids must contain at most 200 items")
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, huma.Error400BadRequest("ids must be positive")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func (s *Handlers) ToNotificationResponse(ctx context.Context, n db.Notification, repoCache map[int64]*db.Repo) (itemapi.NotificationResponse, error) {
	resp := toNotificationResponse(n)
	if n.RepoID != nil {
		if _, ok := repoCache[*n.RepoID]; !ok {
			repo, err := s.Db.GetRepoByID(ctx, *n.RepoID)
			if err != nil {
				return itemapi.NotificationResponse{}, err
			}
			repoCache[*n.RepoID] = repo
		}
		if repo := repoCache[*n.RepoID]; repo != nil {
			resp.Provider = repo.Platform
			resp.PlatformHost = repo.PlatformHost
			resp.RepoOwner = repo.Owner
			resp.RepoName = repo.Name
			resp.RepoPath = repo.RepoPath
		}
	}
	if strings.TrimSpace(resp.Provider) == "" {
		return itemapi.NotificationResponse{}, errors.New("notification provider is required")
	}
	if resp.RepoPath == "" {
		resp.RepoPath = strings.Trim(resp.RepoOwner+"/"+resp.RepoName, "/")
	}
	return resp, nil
}

func toNotificationResponse(n db.Notification) itemapi.NotificationResponse {
	resp := itemapi.NotificationResponse{
		ID:                      n.ID,
		PlatformHost:            n.PlatformHost,
		Provider:                n.Platform,
		RepoPath:                strings.Trim(n.RepoOwner+"/"+n.RepoName, "/"),
		PlatformThreadID:        n.PlatformNotificationID,
		RepoOwner:               n.RepoOwner,
		RepoName:                n.RepoName,
		SubjectType:             n.SubjectType,
		SubjectTitle:            n.SubjectTitle,
		SubjectURL:              n.SubjectURL,
		SubjectLatestCommentURL: n.SubjectLatestCommentURL,
		WebURL:                  n.WebURL,
		ItemNumber:              n.ItemNumber,
		ItemType:                n.ItemType,
		ItemAuthor:              n.ItemAuthor,
		Reason:                  n.Reason,
		Unread:                  n.Unread,
		Participating:           n.Participating,
		GitHubUpdatedAt:         itemapi.FormatUTCRFC3339(n.SourceUpdatedAt),
		DoneReason:              n.DoneReason,
		GitHubReadError:         n.SourceAckError,
		GitHubReadAttempts:      n.SourceAckAttempts,
	}
	assignTime := func(value *time.Time) string {
		if value == nil {
			return ""
		}
		return itemapi.FormatUTCRFC3339(*value)
	}
	resp.GitHubLastReadAt = assignTime(n.SourceLastAcknowledgedAt)
	resp.DoneAt = assignTime(n.DoneAt)
	resp.GitHubReadQueuedAt = assignTime(n.SourceAckQueuedAt)
	resp.GitHubReadSyncedAt = assignTime(n.SourceAckSyncedAt)
	resp.GitHubReadLastAttemptAt = assignTime(n.SourceAckLastAttemptAt)
	resp.GitHubReadNextAttemptAt = assignTime(n.SourceAckNextAttemptAt)
	return resp
}

func toNotificationSummaryResponse(summary db.NotificationSummary) itemapi.NotificationSummaryResponse {
	return itemapi.NotificationSummaryResponse{
		TotalActive: summary.TotalActive,
		Unread:      summary.Unread,
		Done:        summary.Done,
		ByReason:    cloneIntMap(summary.ByReason),
		ByRepo:      cloneIntMap(summary.ByRepo),
	}
}

func cloneIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[strings.ToLower(key)] = value
	}
	return out
}

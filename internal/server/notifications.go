package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
)

const maxNotificationBulkIDs = 200

func (s *Server) listNotifications(ctx context.Context, input *listNotificationsInput) (*listNotificationsOutput, error) {
	if !s.notificationsEnabled() {
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
	items, err := s.db.ListNotifications(ctx, opts)
	if err != nil {
		return nil, huma.Error500InternalServerError("list notifications failed")
	}
	summary, err := s.db.NotificationSummary(ctx, opts)
	if err != nil {
		return nil, huma.Error500InternalServerError("notification summary failed")
	}
	body := notificationsResponse{
		Items:   make([]notificationResponse, 0, len(items)),
		Summary: toNotificationSummaryResponse(summary),
		Sync:    s.currentNotificationSyncStatus(),
	}
	repoCache := map[int64]*db.Repo{}
	for _, item := range items {
		resp, err := s.toNotificationResponse(ctx, item, repoCache)
		if err != nil {
			return nil, huma.Error500InternalServerError("notification repo lookup failed")
		}
		body.Items = append(body.Items, resp)
	}
	return &listNotificationsOutput{Body: body}, nil
}

func notificationRepoFilters(repos []ghclient.RepoRef) ([]db.NotificationRepoFilter, error) {
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

func (s *Server) trackedNotificationRepoFilters() ([]db.NotificationRepoFilter, error) {
	if s.syncer == nil {
		return nil, nil
	}
	return notificationRepoFilters(s.syncer.TrackedRepos())
}

func (s *Server) syncNotifications(ctx context.Context, _ *struct{}) (*acceptedOutput, error) {
	if !s.notificationsEnabled() {
		return nil, huma.Error403Forbidden("notifications are disabled")
	}
	if s.syncer == nil {
		return nil, huma.Error503ServiceUnavailable("syncer is not configured")
	}
	if ok := s.runBackground(func(bgCtx context.Context) {
		_ = s.syncer.RunNotificationSync(bgCtx)
	}); !ok {
		return nil, huma.Error503ServiceUnavailable("server is shutting down")
	}
	return &acceptedOutput{Status: http.StatusAccepted}, nil
}

func (s *Server) notificationsEnabled() bool {
	return s.cfg != nil && s.cfg.NotificationsEnabled()
}

func (s *Server) scopedNotificationIDs(ctx context.Context, ids []int64) ([]int64, error) {
	repos, err := s.trackedNotificationRepoFilters()
	if err != nil {
		return nil, err
	}
	return s.db.FilterNotificationIDs(ctx, ids, repos)
}

func (s *Server) markClosedLinkedNotificationsDone(ctx context.Context) {
	if err := s.db.MarkClosedLinkedNotificationsDone(ctx, s.now().UTC()); err != nil {
		slog.Warn("mark closed linked notifications done", "err", err)
	}
}

func (s *Server) currentNotificationSyncStatus() notificationSyncStatusResponse {
	if s.syncer == nil {
		return notificationSyncStatusResponse{}
	}
	syncStatus := s.syncer.NotificationSyncStatus()
	status := notificationSyncStatusResponse{
		Running:   syncStatus.Running,
		LastError: syncStatus.LastError,
	}
	if !syncStatus.LastStartedAt.IsZero() {
		status.LastStartedAt = formatUTCRFC3339(syncStatus.LastStartedAt)
	}
	if !syncStatus.LastFinishedAt.IsZero() {
		status.LastFinishedAt = formatUTCRFC3339(syncStatus.LastFinishedAt)
	}
	return status
}

func (s *Server) markNotificationsRead(ctx context.Context, input *notificationBulkInput) (*notificationBulkOutput, error) {
	if !s.notificationsEnabled() {
		return nil, huma.Error403Forbidden("notifications are disabled")
	}
	ids, err := validatedNotificationIDs(input.Body.IDs)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	scopedIDs, err := s.scopedNotificationIDs(ctx, ids)
	if err != nil {
		return nil, huma.Error500InternalServerError("mark notifications read failed")
	}
	succeeded, err := s.db.QueueNotificationIDsRead(ctx, scopedIDs, now)
	if err != nil {
		return nil, huma.Error500InternalServerError("mark notifications read failed")
	}
	return &notificationBulkOutput{Body: notificationBulkResult(ids, succeeded, true)}, nil
}

func (s *Server) markNotificationsDone(ctx context.Context, input *notificationBulkInput) (*notificationBulkOutput, error) {
	if !s.notificationsEnabled() {
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
	succeeded, err := s.db.MarkNotificationsDone(ctx, scopedIDs, s.now().UTC(), markRead)
	if err != nil {
		return nil, huma.Error500InternalServerError("mark notifications done failed")
	}
	return &notificationBulkOutput{Body: notificationBulkResult(ids, succeeded, markRead)}, nil
}

func (s *Server) markNotificationsUndone(ctx context.Context, input *notificationBulkInput) (*notificationBulkOutput, error) {
	if !s.notificationsEnabled() {
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
	succeeded, err := s.db.MarkNotificationsUndone(ctx, scopedIDs)
	if err != nil {
		return nil, huma.Error500InternalServerError("mark notifications undone failed")
	}
	if err := s.db.MarkClosedLinkedNotificationsDone(ctx, s.now().UTC()); err != nil {
		return nil, huma.Error500InternalServerError("mark closed linked notifications done failed")
	}
	return &notificationBulkOutput{Body: notificationBulkResult(ids, succeeded, false)}, nil
}

func notificationBulkResult(requested, mutated []int64, queueRead bool) notificationBulkResponse {
	mutatedSet := make(map[int64]struct{}, len(mutated))
	for _, id := range mutated {
		mutatedSet[id] = struct{}{}
	}
	resp := notificationBulkResponse{
		Succeeded: make([]int64, 0, len(mutatedSet)),
		Failed:    []notificationBulkFailure{},
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
		resp.Failed = append(resp.Failed, notificationBulkFailure{ID: id, Error: "notification not found"})
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

func (s *Server) toNotificationResponse(ctx context.Context, n db.Notification, repoCache map[int64]*db.Repo) (notificationResponse, error) {
	resp := toNotificationResponse(n)
	if n.RepoID != nil {
		if _, ok := repoCache[*n.RepoID]; !ok {
			repo, err := s.db.GetRepoByID(ctx, *n.RepoID)
			if err != nil {
				return notificationResponse{}, err
			}
			repoCache[*n.RepoID] = repo
		}
		if repo := repoCache[*n.RepoID]; repo != nil {
			resp.Provider = repo.Platform
			resp.PlatformHost = repo.PlatformHost
			resp.RepoPath = repo.RepoPath
		}
	}
	if strings.TrimSpace(resp.Provider) == "" {
		return notificationResponse{}, errors.New("notification provider is required")
	}
	if resp.RepoPath == "" {
		resp.RepoPath = strings.Trim(resp.RepoOwner+"/"+resp.RepoName, "/")
	}
	return resp, nil
}

func toNotificationResponse(n db.Notification) notificationResponse {
	resp := notificationResponse{
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
		GitHubUpdatedAt:         formatUTCRFC3339(n.SourceUpdatedAt),
		DoneReason:              n.DoneReason,
		GitHubReadError:         n.SourceAckError,
		GitHubReadAttempts:      n.SourceAckAttempts,
	}
	assignTime := func(value *time.Time) string {
		if value == nil {
			return ""
		}
		return formatUTCRFC3339(*value)
	}
	resp.GitHubLastReadAt = assignTime(n.SourceLastAcknowledgedAt)
	resp.DoneAt = assignTime(n.DoneAt)
	resp.GitHubReadQueuedAt = assignTime(n.SourceAckQueuedAt)
	resp.GitHubReadSyncedAt = assignTime(n.SourceAckSyncedAt)
	resp.GitHubReadLastAttemptAt = assignTime(n.SourceAckLastAttemptAt)
	resp.GitHubReadNextAttemptAt = assignTime(n.SourceAckNextAttemptAt)
	return resp
}

func toNotificationSummaryResponse(summary db.NotificationSummary) notificationSummaryResponse {
	return notificationSummaryResponse{
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

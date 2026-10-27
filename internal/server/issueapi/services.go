package issueapi

import (
	"context"
	"log/slog"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/semantic"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/platform"
)

// ProviderSource supplies hub-owned issue data without spoke-local
// workspace fields.
type ProviderSource interface {
	ListIssues(context.Context, ListQuery) ([]IssueResponse, error)
	GetIssue(context.Context, ItemIdentity) (IssueDetailResponse, error)
}

type ListQuery struct {
	Repo           string
	State          string
	Starred        bool
	InvolvesMe     bool
	Unassigned     bool
	ReferencedByPR bool
	Text           string
	Semantic       bool
	Assignee       string
	Limit          int
	Offset         int
}

type ItemIdentity struct {
	Provider     string
	PlatformHost string
	Owner        string
	Name         string
	Number       int
}

func (s *Handler) ListService(ctx context.Context, req ListQuery) ([]IssueResponse, error) {
	var rows []IssueResponse
	var err error
	if s.providerSource != nil {
		rows, err = s.providerSource.ListIssues(ctx, req)
	} else {
		rows, err = s.ListProviderService(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	return s.OverlayLocalWorkspaces(ctx, rows)
}

// ListProviderService returns provider-owned rows in hub order.
func (s *Handler) ListProviderService(
	ctx context.Context, req ListQuery,
) ([]IssueResponse, error) {
	output, err := s.listIssuesRouteCore(ctx, &listIssuesInput{
		Repo: req.Repo, State: req.State, Starred: req.Starred,
		InvolvesMe: req.InvolvesMe, Unassigned: req.Unassigned, ReferencedByPR: req.ReferencedByPR,
		Q: req.Text, Semantic: req.Semantic, Assignee: req.Assignee, Limit: req.Limit, Offset: req.Offset,
	}, nil)
	if err != nil {
		return nil, err
	}
	rows := output.Body
	for i := range rows {
		rows[i].Workspace = nil
		rows[i].LastWorkspaceActivityAt = ""
	}
	return rows, nil
}

// RowsForItems returns list-issues rows for the named visible issues in any
// state, without workspace references. Rows for repositories the list hides
// are omitted.
func (s *Handler) RowsForItems(
	ctx context.Context, items []db.RepoItemNumber,
) ([]IssueResponse, error) {
	if len(items) == 0 {
		return nil, nil
	}
	output, err := s.listIssuesRouteCore(ctx, &listIssuesInput{State: "all"}, items)
	if err != nil {
		return nil, err
	}
	rows := output.Body
	for i := range rows {
		rows[i].Workspace = nil
		rows[i].LastWorkspaceActivityAt = ""
	}
	return rows, nil
}

func (s *Handler) GetService(
	ctx context.Context, item ItemIdentity,
) (IssueDetailResponse, error) {
	var detail IssueDetailResponse
	var err error
	if s.providerSource != nil {
		detail, err = s.providerSource.GetIssue(ctx, item)
	} else {
		detail, err = s.GetProviderService(ctx, item)
	}
	if err != nil {
		return IssueDetailResponse{}, err
	}
	detail.Workspace = nil
	return s.overlayLocalIssueDetail(ctx, detail), nil
}

// GetProviderService returns hub-owned issue detail without a
// workspace reference from the hub machine.
func (s *Handler) GetProviderService(
	ctx context.Context, item ItemIdentity,
) (IssueDetailResponse, error) {
	output, err := s.getIssueRouteCore(ctx, &issueRepoNumberInput{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		Owner: item.Owner, Name: item.Name, Number: item.Number,
	})
	if err != nil {
		return IssueDetailResponse{}, err
	}
	detail := output.Body
	detail.Workspace = nil
	return detail, nil
}

// OverlayLocalWorkspaces replaces workspace references on provider rows
// with this machine's own, since a reference from another node names a
// workspace this machine cannot open. It returns a copy.
func (s *Handler) OverlayLocalWorkspaces(
	ctx context.Context, rows []IssueResponse,
) ([]IssueResponse, error) {
	rows = append(make([]IssueResponse, 0, len(rows)), rows...)
	for i := range rows {
		rows[i].Workspace = nil
		rows[i].LastWorkspaceActivityAt = ""
	}
	if s.workspaceSubjects == nil {
		return rows, nil
	}
	snapshot, err := s.workspaceSubjects(ctx)
	if err != nil {
		return nil, httpapi.Internal("load workspace activity failed")
	}
	overlays := issueWorkspaceOverlays(snapshot)
	for i := range rows {
		activity, ok := overlays[issueResponseIdentity(rows[i])]
		if !ok {
			continue
		}
		workspace := activity.Workspace
		rows[i].Workspace = &workspace
		if activity.ActivityAt != nil {
			rows[i].LastWorkspaceActivityAt = formatUTCRFC3339(*activity.ActivityAt)
		}
	}
	return rows, nil
}

func (s *Handler) overlayLocalIssueDetail(
	ctx context.Context, detail IssueDetailResponse,
) IssueDetailResponse {
	if s.workspaceSubjects == nil || detail.Issue == nil {
		return detail
	}
	snapshot, err := s.workspaceSubjects(ctx)
	if err != nil {
		slog.Warn("load workspace activity for issue detail failed", "err", err)
		return detail
	}
	identity := providerplane.ItemIdentity{
		Repository: platform.RepositoryIdentity{
			Provider: detail.Repo.Provider, PlatformHost: detail.Repo.PlatformHost,
			Key: detail.Repo.Key,
		},
		ItemType: "issue", ItemNumber: detail.Issue.Number,
	}.Canonical()
	if activity, ok := issueWorkspaceOverlays(snapshot)[identity]; ok {
		workspace := activity.Workspace
		detail.Workspace = &workspace
	}
	return detail
}

func issueWorkspaceOverlays(
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
) map[providerplane.ItemIdentity]workspaceapi.SubjectActivity {
	overlays := make(map[providerplane.ItemIdentity]workspaceapi.SubjectActivity)
	for key, workspace := range snapshot.OwnReferences {
		if key.ItemType != db.WorkspaceItemTypeIssue {
			continue
		}
		activity, ok := snapshot.Subjects[key]
		if !ok {
			continue
		}
		identity := providerplane.ItemIdentity{
			Repository: platform.RepositoryIdentity{
				Provider:     activity.Subject.Platform,
				PlatformHost: activity.Subject.PlatformHost,
				Key:          activity.Subject.RepoKey,
			},
			ItemType: "issue", ItemNumber: key.ItemNumber,
		}.Canonical()
		if identity.Valid() {
			activity.Workspace = workspace
			overlays[identity] = activity
		}
	}
	return overlays
}

func issueResponseIdentity(row IssueResponse) providerplane.ItemIdentity {
	return providerplane.ItemIdentity{
		Repository: platform.RepositoryIdentity{
			Provider: row.Repo.Provider, PlatformHost: row.Repo.PlatformHost,
			Key: row.Repo.Key,
		},
		ItemType: "issue", ItemNumber: row.Number,
	}.Canonical()
}

// listIssuesByMeaning applies every list filter except q, then orders the
// matching issues by how closely their prose matches q before paging.
func (s *Handler) listIssuesByMeaning(
	ctx context.Context, input *listIssuesInput, items []db.RepoItemNumber,
) (*listIssuesOutput, error) {
	if err := httpapi.SemanticSearchAvailable(input.Q, s.semantic != nil); err != nil {
		return nil, err
	}
	filtered := *input
	filtered.Q, filtered.Semantic, filtered.Limit, filtered.Offset = "", false, 0, 0
	output, err := s.listIssuesRouteCore(ctx, &filtered, items)
	if err != nil {
		return nil, err
	}
	ranked, err := semantic.Rank(ctx, s.semantic, input.Q, "issue", output.Body,
		func(row IssueResponse) db.RepoItemNumber {
			return db.RepoItemNumber{RepoID: row.RepoID, Number: row.Number}
		}, input.Offset, input.Limit)
	if err != nil {
		return nil, httpapi.SemanticSearchFailed(err)
	}
	rows := make([]IssueResponse, 0, len(ranked))
	for _, match := range ranked {
		match.Row.SearchScore = &match.Score
		rows = append(rows, match.Row)
	}
	return &listIssuesOutput{Body: rows}, nil
}

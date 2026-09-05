package issueapi

import (
	"context"
	"log/slog"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
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
	return s.overlayLocalIssueWorkspaces(ctx, rows)
}

// ListProviderService returns provider-owned rows in hub order.
func (s *Handler) ListProviderService(
	ctx context.Context, req ListQuery,
) ([]IssueResponse, error) {
	output, err := s.listIssuesRouteCore(ctx, &listIssuesInput{
		Repo: req.Repo, State: req.State, Starred: req.Starred,
		InvolvesMe: req.InvolvesMe, Unassigned: req.Unassigned, ReferencedByPR: req.ReferencedByPR,
		Q: req.Text, Assignee: req.Assignee, Limit: req.Limit, Offset: req.Offset,
	})
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

func (s *Handler) overlayLocalIssueWorkspaces(
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
		Repository: providerplane.RepositoryIdentity{
			Provider: detail.Repo.Provider, PlatformHost: detail.Repo.PlatformHost,
			PlatformRepoID: detail.Repo.PlatformRepoID,
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
			Repository: providerplane.RepositoryIdentity{
				Provider:       activity.Subject.Platform,
				PlatformHost:   activity.Subject.PlatformHost,
				PlatformRepoID: activity.Subject.PlatformRepoID,
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
		Repository: providerplane.RepositoryIdentity{
			Provider: row.Repo.Provider, PlatformHost: row.Repo.PlatformHost,
			PlatformRepoID: row.Repo.PlatformRepoID,
		},
		ItemType: "issue", ItemNumber: row.Number,
	}.Canonical()
}

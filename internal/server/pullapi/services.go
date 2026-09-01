package pullapi

import (
	"context"
	"log/slog"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

// ProviderSource supplies hub-owned pull data without spoke-local
// workspace fields.
type ProviderSource interface {
	ListPulls(context.Context, ListQuery) ([]MergeRequestResponse, error)
	GetPull(context.Context, ItemIdentity) (MergeRequestDetailResponse, error)
	GetDiffDescriptor(context.Context, ItemIdentity) (providerplane.DiffDescriptor, error)
}

type ListQuery struct {
	Repo       string
	State      string
	Kanban     string
	Starred    bool
	InvolvesMe bool
	Text       string
	Limit      int
	Offset     int
}

type ItemIdentity struct {
	Provider     string
	PlatformHost string
	Owner        string
	Name         string
	Number       int
}

type StackContext struct {
	ID       int64
	Name     string
	Position int
	Size     int
	Health   string
	Members  []StackMember
}

type StackMember struct {
	Number         int
	Title          string
	State          string
	CIStatus       string
	ReviewDecision string
	MergeableState string
	Position       int
	IsDraft        bool
	BaseBranch     string
	BlockedBy      *int
}

type DiffQuery struct {
	Whitespace string
	Commit     string
	From       string
	To         string
}

func (s *Handler) ListService(ctx context.Context, req ListQuery) ([]MergeRequestResponse, error) {
	var rows []MergeRequestResponse
	var err error
	if s.providerSource != nil {
		rows, err = s.providerSource.ListPulls(ctx, req)
	} else {
		rows, err = s.ListProviderService(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	return s.overlayLocalPullWorkspaces(ctx, rows)
}

// ListProviderService returns provider-owned rows in hub order. It
// intentionally removes workspace fields before data crosses a spoke boundary.
func (s *Handler) ListProviderService(
	ctx context.Context, req ListQuery,
) ([]MergeRequestResponse, error) {
	output, err := s.listPullsRouteCore(ctx, &listPullsInput{
		Repo: req.Repo, State: req.State, Kanban: req.Kanban,
		Starred: req.Starred, InvolvesMe: req.InvolvesMe,
		Q: req.Text, Limit: req.Limit, Offset: req.Offset,
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
) (MergeRequestDetailResponse, error) {
	var detail MergeRequestDetailResponse
	var err error
	if s.providerSource != nil {
		detail, err = s.providerSource.GetPull(ctx, item)
	} else {
		detail, err = s.GetProviderService(ctx, item)
	}
	if err != nil {
		return MergeRequestDetailResponse{}, err
	}
	detail.Workspace = nil
	return s.overlayLocalPullDetail(ctx, detail), nil
}

// GetProviderService returns hub-owned pull detail without a
// workspace reference from the hub machine.
func (s *Handler) GetProviderService(
	ctx context.Context, item ItemIdentity,
) (MergeRequestDetailResponse, error) {
	output, err := s.getPullRouteCore(ctx, item.routeInput())
	if err != nil {
		return MergeRequestDetailResponse{}, err
	}
	detail := output.Body
	detail.Workspace = nil
	return detail, nil
}

func (s *Handler) overlayLocalPullWorkspaces(
	ctx context.Context, rows []MergeRequestResponse,
) ([]MergeRequestResponse, error) {
	rows = append(make([]MergeRequestResponse, 0, len(rows)), rows...)
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
	overlays := pullWorkspaceOverlays(snapshot)
	for i := range rows {
		identity := pullResponseIdentity(rows[i])
		activity, ok := overlays[identity]
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

func (s *Handler) overlayLocalPullDetail(
	ctx context.Context, detail MergeRequestDetailResponse,
) MergeRequestDetailResponse {
	if s.workspaceSubjects == nil || detail.MergeRequest == nil {
		return detail
	}
	snapshot, err := s.workspaceSubjects(ctx)
	if err != nil {
		slog.Warn("load workspace activity for pull detail failed", "err", err)
		return detail
	}
	identity := providerplane.ItemIdentity{
		Repository: providerplane.RepositoryIdentity{
			Provider: detail.Repo.Provider, PlatformHost: detail.Repo.PlatformHost,
			PlatformRepoID: detail.Repo.PlatformRepoID,
		},
		ItemType: "pr", ItemNumber: detail.MergeRequest.Number,
	}.Canonical()
	if activity, ok := pullWorkspaceOverlays(snapshot)[identity]; ok {
		workspace := activity.Workspace
		detail.Workspace = &workspace
	}
	return detail
}

func pullWorkspaceOverlays(
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
) map[providerplane.ItemIdentity]workspaceapi.SubjectActivity {
	overlays := make(map[providerplane.ItemIdentity]workspaceapi.SubjectActivity)
	for key, activity := range snapshot.Subjects {
		if key.ItemType != db.WorkspaceItemTypePullRequest {
			continue
		}
		identity := providerplane.ItemIdentity{
			Repository: providerplane.RepositoryIdentity{
				Provider:       activity.Subject.Platform,
				PlatformHost:   activity.Subject.PlatformHost,
				PlatformRepoID: activity.Subject.PlatformRepoID,
			},
			ItemType: "pr", ItemNumber: key.ItemNumber,
		}.Canonical()
		if identity.Valid() {
			overlays[identity] = activity
		}
	}
	return overlays
}

func pullResponseIdentity(row MergeRequestResponse) providerplane.ItemIdentity {
	return providerplane.ItemIdentity{
		Repository: providerplane.RepositoryIdentity{
			Provider: row.Repo.Provider, PlatformHost: row.Repo.PlatformHost,
			PlatformRepoID: row.Repo.PlatformRepoID,
		},
		ItemType: "pr", ItemNumber: row.Number,
	}.Canonical()
}

func (s *Handler) GetDiffService(
	ctx context.Context, item ItemIdentity, query DiffQuery,
) (httpapi.DiffResponse, error) {
	output, err := s.getDiffRouteCore(ctx, &getDiffInput{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		Owner: item.Owner, Name: item.Name, Number: item.Number,
		Whitespace: query.Whitespace, Commit: query.Commit, From: query.From, To: query.To,
	})
	if err != nil {
		return httpapi.DiffResponse{}, err
	}
	return output.Body, nil
}

func (s *Handler) GetFilesService(
	ctx context.Context, item ItemIdentity,
) (httpapi.FilesResponse, error) {
	output, err := s.getFilesRouteCore(ctx, &getFilesInput{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		Owner: item.Owner, Name: item.Name, Number: item.Number,
	})
	if err != nil {
		return httpapi.FilesResponse{}, err
	}
	return output.Body, nil
}

func (s *Handler) GetStackService(
	ctx context.Context, item ItemIdentity,
) (StackContext, error) {
	output, err := s.getStackForPRRouteCore(ctx, item.routeInput())
	if err != nil {
		return StackContext{}, err
	}
	stack := StackContext{
		ID: output.Body.StackID, Name: output.Body.StackName,
		Position: output.Body.Position, Size: output.Body.Size, Health: output.Body.Health,
		Members: make([]StackMember, 0, len(output.Body.Members)),
	}
	for _, member := range output.Body.Members {
		stack.Members = append(stack.Members, StackMember(member))
	}
	return stack, nil
}

func (stack StackContext) routeResponse() stackContextResponse {
	response := stackContextResponse{
		StackID: stack.ID, StackName: stack.Name, Position: stack.Position,
		Size: stack.Size, Health: stack.Health,
		Members: make([]stackMemberResponse, 0, len(stack.Members)),
	}
	for _, member := range stack.Members {
		response.Members = append(response.Members, stackMemberResponse(member))
	}
	return response
}

func (item ItemIdentity) routeInput() *repoNumberInput {
	return &repoNumberInput{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		Owner: item.Owner, Name: item.Name, Number: item.Number,
	}
}

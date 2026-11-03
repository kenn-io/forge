package pullapi

import (
	"context"
	"log/slog"
	"sync"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/platform"
)

// ProviderSource supplies hub-owned pull data without spoke-local
// workspace fields.
type ProviderSource interface {
	ListPulls(context.Context, ListQuery) ([]MergeRequestResponse, error)
	GetPull(context.Context, ItemIdentity) (MergeRequestDetailResponse, error)
	GetDiffDescriptor(context.Context, ItemIdentity) (providerplane.DiffDescriptor, error)
}

type ListQuery struct {
	Attributes string
	Label      string
	Repo       string
	State      string
	Kanban     string
	Starred    bool
	InvolvesMe bool
	Unassigned bool
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
	Native   bool
	// MidStackBlocker is the member a merge would leave behind; see
	// stackContextResponse.MidStackBlocker.
	MidStackBlocker int
	Members         []StackMember
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
	subjects := s.startWorkspaceSubjects(ctx)
	var rows []MergeRequestResponse
	var err error
	if s.providerSource != nil {
		rows, err = s.providerSource.ListPulls(ctx, req)
	} else {
		rows, err = s.ListProviderService(ctx, req)
	}
	if err != nil {
		_, _ = subjects()
		return nil, err
	}
	return s.overlayLocalWorkspaces(rows, subjects)
}

// ListProviderService returns provider-owned rows in hub order. It
// intentionally removes workspace fields before data crosses a spoke boundary.
func (s *Handler) ListProviderService(
	ctx context.Context, req ListQuery,
) ([]MergeRequestResponse, error) {
	output, err := s.listPullsRouteCore(ctx, &listPullsInput{
		Repo: req.Repo, State: req.State, Kanban: req.Kanban,
		Starred: req.Starred, InvolvesMe: req.InvolvesMe, Unassigned: req.Unassigned,
		Q: req.Text, Label: req.Label, Attributes: req.Attributes, Limit: req.Limit, Offset: req.Offset,
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

// RowsForItems returns list-pulls rows for the named visible pull requests in
// any state, without workspace references. Rows for repositories the list
// hides are omitted.
func (s *Handler) RowsForItems(
	ctx context.Context, items []db.RepoItemNumber,
) ([]MergeRequestResponse, error) {
	if len(items) == 0 {
		return nil, nil
	}
	output, err := s.listPullsRouteCore(ctx, &listPullsInput{State: "all"}, items)
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
	subjects := s.startWorkspaceSubjects(ctx)
	var detail MergeRequestDetailResponse
	var err error
	if s.providerSource != nil {
		detail, err = s.providerSource.GetPull(ctx, item)
	} else {
		detail, err = s.GetProviderService(ctx, item)
	}
	if err != nil {
		_, _ = subjects()
		return MergeRequestDetailResponse{}, err
	}
	detail.Workspace = nil
	return s.overlayLocalPullDetail(detail, subjects), nil
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

// OverlayLocalWorkspaces replaces workspace references on provider rows
// with this machine's own, since a reference from another node names a
// workspace this machine cannot open. It returns a copy.
func (s *Handler) OverlayLocalWorkspaces(
	ctx context.Context, rows []MergeRequestResponse,
) ([]MergeRequestResponse, error) {
	return s.overlayLocalWorkspaces(rows, s.startWorkspaceSubjects(ctx))
}

// startWorkspaceSubjects starts reading this machine's workspace snapshot,
// which needs nothing from the provider read it overlays, and returns a
// function that waits for the result. Callers must call it on every path.
func (s *Handler) startWorkspaceSubjects(
	ctx context.Context,
) func() (workspaceapi.WorkspaceSubjectSnapshot, error) {
	if s.workspaceSubjects == nil {
		return func() (workspaceapi.WorkspaceSubjectSnapshot, error) {
			return workspaceapi.WorkspaceSubjectSnapshot{}, nil
		}
	}
	var read sync.WaitGroup
	var snapshot workspaceapi.WorkspaceSubjectSnapshot
	var err error
	read.Go(func() { snapshot, err = s.workspaceSubjects(ctx) })
	return func() (workspaceapi.WorkspaceSubjectSnapshot, error) {
		read.Wait()
		return snapshot, err
	}
}

func (s *Handler) overlayLocalWorkspaces(
	rows []MergeRequestResponse, subjects func() (workspaceapi.WorkspaceSubjectSnapshot, error),
) ([]MergeRequestResponse, error) {
	rows = append(make([]MergeRequestResponse, 0, len(rows)), rows...)
	for i := range rows {
		rows[i].Workspace = nil
		rows[i].LastWorkspaceActivityAt = ""
	}
	snapshot, err := subjects()
	if s.workspaceSubjects == nil {
		return rows, nil
	}
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
	detail MergeRequestDetailResponse, subjects func() (workspaceapi.WorkspaceSubjectSnapshot, error),
) MergeRequestDetailResponse {
	snapshot, err := subjects()
	if s.workspaceSubjects == nil || detail.MergeRequest == nil {
		return detail
	}
	if err != nil {
		slog.Warn("load workspace activity for pull detail failed", "err", err)
		return detail
	}
	identity := providerplane.ItemIdentity{
		Repository: platform.RepositoryIdentity{
			Provider: detail.Repo.Provider, PlatformHost: detail.Repo.PlatformHost,
			Key: detail.Repo.Key,
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
			Repository: platform.RepositoryIdentity{
				Provider:     activity.Subject.Platform,
				PlatformHost: activity.Subject.PlatformHost,
				Key:          activity.Subject.RepoKey,
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
		Repository: platform.RepositoryIdentity{
			Provider: row.Repo.Provider, PlatformHost: row.Repo.PlatformHost,
			Key: row.Repo.Key,
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
		Native: output.Body.Native, MidStackBlocker: output.Body.MidStackBlocker,
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
		Size: stack.Size, Health: stack.Health, Native: stack.Native,
		MidStackBlocker: stack.MidStackBlocker,
		Members:         make([]stackMemberResponse, 0, len(stack.Members)),
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

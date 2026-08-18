package pullapi

import (
	"context"

	"go.kenn.io/forge/internal/server/httpapi"
)

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
	output, err := s.listPullsRouteCore(ctx, &listPullsInput{
		Repo: req.Repo, State: req.State, Kanban: req.Kanban,
		Starred: req.Starred, InvolvesMe: req.InvolvesMe,
		Q: req.Text, Limit: req.Limit, Offset: req.Offset,
	})
	if err != nil {
		return nil, err
	}
	return output.Body, nil
}

func (s *Handler) GetService(
	ctx context.Context, item ItemIdentity,
) (MergeRequestDetailResponse, error) {
	output, err := s.getPullRouteCore(ctx, item.routeInput())
	if err != nil {
		return MergeRequestDetailResponse{}, err
	}
	return output.Body, nil
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

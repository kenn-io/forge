package issueapi

import "context"

type ListQuery struct {
	Repo           string
	State          string
	Starred        bool
	InvolvesMe     bool
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
	output, err := s.listIssuesRouteCore(ctx, &listIssuesInput{
		Repo: req.Repo, State: req.State, Starred: req.Starred,
		InvolvesMe: req.InvolvesMe, ReferencedByPR: req.ReferencedByPR,
		Q: req.Text, Assignee: req.Assignee, Limit: req.Limit, Offset: req.Offset,
	})
	if err != nil {
		return nil, err
	}
	return output.Body, nil
}

func (s *Handler) GetService(
	ctx context.Context, item ItemIdentity,
) (IssueDetailResponse, error) {
	output, err := s.getIssueRouteCore(ctx, &issueRepoNumberInput{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		Owner: item.Owner, Name: item.Name, Number: item.Number,
	})
	if err != nil {
		return IssueDetailResponse{}, err
	}
	return output.Body, nil
}

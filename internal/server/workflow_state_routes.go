package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/middleman/internal/db"
)

type listWorkflowStateInput struct {
	Repo          string   `query:"repo" doc:"Repository filter. Accepts provider|platform_host/repo_path, with comma-separated values for multiple repositories."`
	ItemType      []string `query:"item_type" doc:"Optional item type filter: pr, issue."`
	State         []string `query:"state" doc:"Optional effective workflow states to include."`
	IncludeClosed bool     `query:"include_closed"`
	Limit         int      `query:"limit"`
	Cursor        string   `query:"cursor" doc:"Opaque keyset cursor from a previous response. Reuse only with the same repo, item_type, state, and include_closed filters; pages are best-effort under concurrent writes."`
}

type workflowStateItemResponse struct {
	Provider       string                    `json:"provider"`
	PlatformHost   string                    `json:"platform_host"`
	Owner          string                    `json:"owner"`
	Name           string                    `json:"name"`
	RepoPath       string                    `json:"repo_path"`
	ItemType       string                    `json:"item_type"`
	Number         int                       `json:"number"`
	Title          string                    `json:"title"`
	State          string                    `json:"state"`
	URL            string                    `json:"url"`
	Author         string                    `json:"author"`
	IsDraft        bool                      `json:"is_draft"`
	LastActivityAt string                    `json:"last_activity_at" format:"date-time"`
	Workflow       workflowStateMetaResponse `json:"workflow"`
}

type workflowStateListResponse struct {
	Items      []workflowStateItemResponse `json:"items"`
	NextCursor string                      `json:"next_cursor,omitempty"`
}

type listWorkflowStateOutput = bodyOutput[workflowStateListResponse]

type setWorkflowStateBody struct {
	Status         string `json:"status"`
	ExpectedStatus string `json:"expected_status,omitempty"`
	Source         string `json:"source,omitempty"`
	Actor          string `json:"actor,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type setWorkflowStateInput struct {
	ItemType     string `path:"item_type" enum:"pr,issue"`
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setWorkflowStateBody
}

type setWorkflowStateHostInput struct {
	ItemType     string `path:"item_type" enum:"pr,issue"`
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setWorkflowStateBody
}

type workflowStateChangeResponse struct {
	PreviousStatus db.KanbanStatus `json:"previous_status" enum:"new,reviewing,waiting,awaiting_merge"`
	Status         db.KanbanStatus `json:"status" enum:"new,reviewing,waiting,awaiting_merge"`
	UpdatedAt      string          `json:"updated_at" format:"date-time"`
	UpdatedSource  string          `json:"updated_source"`
	UpdatedActor   string          `json:"updated_actor,omitempty"`
	UpdatedReason  string          `json:"updated_reason,omitempty"`
}

type setWorkflowStateOutput = bodyOutput[workflowStateChangeResponse]

var workflowSourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

func (s *Server) registerWorkflowStateAPI(api huma.API) {
	huma.Get(api, "/workflow-state", s.listWorkflowState,
		documentOperation("list-workflow-state", "List item workflow state", "Workflow State"))
	huma.Register(api, huma.Operation{
		OperationID:   "set-workflow-state",
		Method:        http.MethodPut,
		Path:          "/workflow-state/{item_type}/{provider}/{owner}/{name}/{number}",
		DefaultStatus: http.StatusOK,
		Summary:       "Set item workflow state",
		Tags:          []string{"Workflow State"},
	}, s.setWorkflowState)
	huma.Register(api, huma.Operation{
		OperationID:   "set-workflow-state-on-host",
		Method:        http.MethodPut,
		Path:          "/host/{platform_host}/workflow-state/{item_type}/{provider}/{owner}/{name}/{number}",
		DefaultStatus: http.StatusOK,
		Summary:       "Set item workflow state",
		Tags:          []string{"Workflow State"},
	}, s.setWorkflowStateOnHost)
}

func (s *Server) listWorkflowState(
	ctx context.Context,
	input *listWorkflowStateInput,
) (*listWorkflowStateOutput, error) {
	if hasInvalidRepoFilter(input.Repo) {
		return nil, problemValidation("query.repo", "repo filter must be provider|platform_host/repo_path")
	}
	for _, itemType := range input.ItemType {
		if itemType != db.ItemTypePR && itemType != db.ItemTypeIssue {
			return nil, problemValidation("query.item_type", "item_type must be one of: pr, issue", "pr", "issue")
		}
	}
	for _, state := range input.State {
		if !validKanbanStates[state] {
			return nil, problemValidation(
				"query.state",
				"state must be one of: new, reviewing, waiting, awaiting_merge",
				"new", "reviewing", "waiting", "awaiting_merge",
			)
		}
	}

	rows, next, err := s.db.ListItemWorkflowStates(ctx, db.ListWorkflowStatesOpts{
		RepoFilters:   parseRepoFilters(input.Repo),
		ItemTypes:     input.ItemType,
		States:        input.State,
		IncludeClosed: input.IncludeClosed,
		Limit:         input.Limit,
		Cursor:        input.Cursor,
	})
	if err != nil {
		if input.Cursor != "" {
			return nil, problemValidation("query.cursor", "invalid cursor")
		}
		return nil, problemInternal("list workflow state failed")
	}

	out := workflowStateListResponse{
		Items:      make([]workflowStateItemResponse, 0, len(rows)),
		NextCursor: next,
	}
	for _, row := range rows {
		item := workflowStateItemResponse{
			Provider:       row.Platform,
			PlatformHost:   row.PlatformHost,
			Owner:          row.Owner,
			Name:           row.Name,
			RepoPath:       row.RepoPath,
			ItemType:       row.ItemType,
			Number:         row.Number,
			Title:          row.Title,
			State:          row.State,
			URL:            row.URL,
			Author:         row.Author,
			IsDraft:        row.IsDraft,
			LastActivityAt: formatUTCRFC3339(row.LastActivityAt),
			Workflow: workflowStateMetaResponse{
				Status: normalizeWorkflowStatus(
					row.Status, "provider", row.Platform, "platform_host", row.PlatformHost,
					"owner", row.Owner, "name", row.Name, "item_type", row.ItemType,
					"item_number", row.Number,
				),
			},
		}
		if row.HasRow && row.UpdatedAt != nil {
			item.Workflow.UpdatedAt = formatUTCRFC3339(*row.UpdatedAt)
			item.Workflow.UpdatedSource = row.UpdatedSource
			item.Workflow.UpdatedActor = row.UpdatedActor
			item.Workflow.UpdatedReason = row.UpdatedReason
		}
		out.Items = append(out.Items, item)
	}
	return &listWorkflowStateOutput{Body: out}, nil
}

func (s *Server) setWorkflowState(
	ctx context.Context,
	input *setWorkflowStateInput,
) (*setWorkflowStateOutput, error) {
	if err := validateSetWorkflowStateBody(input.Body); err != nil {
		return nil, err
	}
	source := input.Body.Source
	if source == "" {
		source = "api"
	}

	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	ref := repoNumberPathRef{
		repoID:       repo.ID,
		owner:        repo.Owner,
		name:         repo.Name,
		number:       input.Number,
		platformHost: repo.PlatformHost,
	}
	switch input.ItemType {
	case db.ItemTypePR:
		if _, err := s.lookupMRID(ctx, ref); err != nil {
			return nil, problemNotFound(CodePullNotFound, err.Error(), nil)
		}
	case db.ItemTypeIssue:
		if _, err := s.lookupIssueID(ctx, ref); err != nil {
			return nil, problemNotFound(CodeIssueNotFound, err.Error(), nil)
		}
	default:
		return nil, problemValidation("path.item_type", "item_type must be one of: pr, issue", "pr", "issue")
	}

	previous, err := s.db.SetItemWorkflowState(ctx, db.SetItemWorkflowStateParams{
		RepoID:         repo.ID,
		ItemType:       input.ItemType,
		ItemNumber:     input.Number,
		Status:         input.Body.Status,
		ExpectedStatus: input.Body.ExpectedStatus,
		Source:         source,
		Actor:          input.Body.Actor,
		Reason:         input.Body.Reason,
	})
	var conflict *db.WorkflowStateConflictError
	if errors.As(err, &conflict) {
		return nil, problemConflict(CodeConflict, "workflow state changed", map[string]any{
			"current_status":  conflict.Current,
			"expected_status": conflict.Expected,
		})
	}
	if err != nil {
		return nil, problemInternal("set workflow state failed")
	}

	row, err := s.db.GetItemWorkflowState(ctx, repo.ID, input.ItemType, input.Number)
	if err != nil || row == nil {
		return nil, problemInternal("read workflow state failed")
	}
	return &setWorkflowStateOutput{Body: workflowStateChangeResponse{
		PreviousStatus: normalizeWorkflowStatus(previous, "repo_id", repo.ID, "item_type", input.ItemType, "item_number", input.Number),
		Status:         normalizeWorkflowStatus(row.Status, "repo_id", repo.ID, "item_type", input.ItemType, "item_number", input.Number),
		UpdatedAt:      row.UpdatedAt.UTC().Format(time.RFC3339),
		UpdatedSource:  row.UpdatedSource,
		UpdatedActor:   row.UpdatedActor,
		UpdatedReason:  row.UpdatedReason,
	}}, nil
}

func validateSetWorkflowStateBody(body setWorkflowStateBody) error {
	if !validKanbanStates[body.Status] {
		return problemValidation(
			"body.status",
			"status must be one of: new, reviewing, waiting, awaiting_merge",
			"new", "reviewing", "waiting", "awaiting_merge",
		)
	}
	if body.ExpectedStatus != "" && !validKanbanStates[body.ExpectedStatus] {
		return problemValidation(
			"body.expected_status",
			"expected_status must be one of: new, reviewing, waiting, awaiting_merge",
			"new", "reviewing", "waiting", "awaiting_merge",
		)
	}
	if body.Source != "" && !workflowSourcePattern.MatchString(body.Source) {
		return problemValidation("body.source", "source must match ^[a-z][a-z0-9_-]{0,39}$")
	}
	if len(body.Actor) > 120 {
		return problemValidation("body.actor", "actor must be at most 120 bytes")
	}
	if len(body.Reason) > 500 {
		return problemValidation("body.reason", "reason must be at most 500 bytes")
	}
	return nil
}

func (s *Server) setWorkflowStateOnHost(
	ctx context.Context,
	input *setWorkflowStateHostInput,
) (*setWorkflowStateOutput, error) {
	next := setWorkflowStateInput{
		ItemType:     input.ItemType,
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.setWorkflowState(ctx, &next)
}

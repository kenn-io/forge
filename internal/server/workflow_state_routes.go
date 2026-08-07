package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
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
	Provider       string                        `json:"provider"`
	PlatformHost   string                        `json:"platform_host"`
	Owner          string                        `json:"owner"`
	Name           string                        `json:"name"`
	RepoPath       string                        `json:"repo_path"`
	ItemType       string                        `json:"item_type"`
	Number         int                           `json:"number"`
	Title          string                        `json:"title"`
	State          string                        `json:"state"`
	URL            string                        `json:"url"`
	Author         string                        `json:"author"`
	IsDraft        bool                          `json:"is_draft"`
	LastActivityAt string                        `json:"last_activity_at" format:"date-time"`
	Workflow       workflowStateListMetaResponse `json:"workflow"`
}

type workflowStateListMetaResponse struct {
	Status        db.KanbanStatus `json:"status" enum:"new,reviewing,waiting,awaiting_merge"`
	UpdatedAt     string          `json:"updated_at,omitempty" format:"date-time"`
	UpdatedSource string          `json:"updated_source,omitempty"`
	UpdatedActor  string          `json:"updated_actor,omitempty"`
	UpdatedReason string          `json:"updated_reason,omitempty"`
}

var validKanbanStates = map[string]bool{
	string(db.KanbanStatusNew):           true,
	string(db.KanbanStatusReviewing):     true,
	string(db.KanbanStatusWaiting):       true,
	string(db.KanbanStatusAwaitingMerge): true,
}

func normalizeWorkflowStatusForAPI(status string) db.KanbanStatus {
	if validKanbanStates[status] {
		return db.KanbanStatus(status)
	}
	return db.KanbanStatusNew
}

type workflowStateListResponse struct {
	Items      []workflowStateItemResponse `json:"items"`
	NextCursor string                      `json:"next_cursor,omitempty"`
}

type listWorkflowStateOutput = httpapi.BodyOutput[workflowStateListResponse]

type setWorkflowStateBody struct {
	Status         string `json:"status" enum:"new,reviewing,waiting,awaiting_merge"`
	ExpectedStatus string `json:"expected_status,omitempty" enum:"new,reviewing,waiting,awaiting_merge" doc:"Required unless force is true. Omit force when expected_status is provided. This compares against the effective current local workflow state before writing."`
	Force          *bool  `json:"force,omitempty" doc:"Set true only for a deliberate unconditional override when expected_status is omitted. Omit this field when expected_status is provided; false is invalid."`
	Source         string `json:"source,omitempty" pattern:"^[a-z][a-z0-9_-]{0,39}$"`
	Actor          string `json:"actor,omitempty" maxLength:"120"`
	Reason         string `json:"reason,omitempty" maxLength:"500"`
}

func (body *setWorkflowStateBody) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for field := range raw {
		switch field {
		case "status", "expected_status", "force", "source", "actor", "reason":
		default:
			return fmt.Errorf("unknown field %q", field)
		}
	}
	type workflowBody setWorkflowStateBody
	var decoded workflowBody
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*body = setWorkflowStateBody(decoded)
	return nil
}

func (setWorkflowStateBody) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	if schema.Extensions == nil {
		schema.Extensions = map[string]any{}
	}
	common := workflowStateCommonProperties(schema.Properties)
	guarded := copyWorkflowStateProperties(common)
	guarded["expected_status"] = schema.Properties["expected_status"]
	forced := copyWorkflowStateProperties(common)
	forced["force"] = &huma.Schema{
		Type: huma.TypeBoolean,
		Enum: []any{true},
	}
	schema.AdditionalProperties = nil
	schema.Extensions["properties"] = map[string]*huma.Schema{}
	schema.Extensions["required"] = []string{}
	schema.Extensions["oneOf"] = []*huma.Schema{
		{
			Type:                 huma.TypeObject,
			AdditionalProperties: false,
			Required:             []string{"status", "expected_status"},
			Properties:           guarded,
		},
		{
			Type:                 huma.TypeObject,
			AdditionalProperties: false,
			Required:             []string{"status", "force"},
			Properties:           forced,
		},
	}
	return schema
}

func workflowStateCommonProperties(properties map[string]*huma.Schema) map[string]*huma.Schema {
	common := make(map[string]*huma.Schema, len(properties))
	for name, property := range properties {
		if name == "expected_status" || name == "force" {
			continue
		}
		common[name] = property
	}
	return common
}

func copyWorkflowStateProperties(properties map[string]*huma.Schema) map[string]*huma.Schema {
	out := make(map[string]*huma.Schema, len(properties)+2)
	maps.Copy(out, properties)
	return out
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

type setWorkflowStateOutput = httpapi.BodyOutput[workflowStateChangeResponse]

var workflowSourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

func (s *Server) registerWorkflowStateAPI(api huma.API) {
	huma.Get(api, "/workflow-state", s.listWorkflowState,
		httpapi.DocumentOperation("list-workflow-state", "List item workflow state", "Activity"))
	huma.Register(api, huma.Operation{
		OperationID:   "set-workflow-state",
		Method:        http.MethodPut,
		Path:          "/workflow-state/{item_type}/{provider}/{owner}/{name}/{number}",
		DefaultStatus: http.StatusOK,
		Summary:       "Set item workflow state",
		Tags:          []string{"Activity"},
	}, s.setWorkflowState)
	huma.Register(api, huma.Operation{
		OperationID:   "set-workflow-state-on-host",
		Method:        http.MethodPut,
		Path:          "/host/{platform_host}/workflow-state/{item_type}/{provider}/{owner}/{name}/{number}",
		DefaultStatus: http.StatusOK,
		Summary:       "Set item workflow state",
		Tags:          []string{"Activity"},
	}, s.setWorkflowStateOnHost)
}

func (s *Server) listWorkflowState(
	ctx context.Context,
	input *listWorkflowStateInput,
) (*listWorkflowStateOutput, error) {
	if hasInvalidRepoFilter(input.Repo) {
		return nil, httpapi.Validation("query.repo", "repo filter must be provider|platform_host/repo_path")
	}
	for _, itemType := range input.ItemType {
		if itemType != db.ItemTypePR && itemType != db.ItemTypeIssue {
			return nil, httpapi.Validation("query.item_type", "item_type must be one of: pr, issue", "pr", "issue")
		}
	}
	for _, state := range input.State {
		if !validKanbanStates[state] {
			return nil, httpapi.Validation(
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
			return nil, httpapi.Validation("query.cursor", "invalid cursor")
		}
		return nil, httpapi.Internal("list workflow state failed")
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
			Workflow:       workflowStateListMetaResponse{Status: normalizeWorkflowStatusForAPI(row.Status)},
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

	owner, err := url.PathUnescape(input.Owner)
	if err != nil {
		return nil, httpapi.Validation("path.owner", "owner must be valid URL path text")
	}
	name, err := url.PathUnescape(input.Name)
	if err != nil {
		return nil, httpapi.Validation("path.name", "name must be valid URL path text")
	}
	repo, err := s.repoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, owner, name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	switch input.ItemType {
	case db.ItemTypePR:
		item, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
		if err != nil {
			return nil, httpapi.Internal("read pull request failed")
		}
		if item == nil {
			return nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found", nil)
		}
	case db.ItemTypeIssue:
		item, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
		if err != nil {
			return nil, httpapi.Internal("read issue failed")
		}
		if item == nil {
			return nil, httpapi.NotFound(httpapi.CodeIssueNotFound, "issue not found", nil)
		}
	default:
		return nil, httpapi.Validation("path.item_type", "item_type must be one of: pr, issue", "pr", "issue")
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
		return nil, httpapi.Conflict(httpapi.CodeConflict, "workflow state changed", map[string]any{
			"current_status":  conflict.Current,
			"expected_status": conflict.Expected,
		})
	}
	if err != nil {
		return nil, httpapi.Internal("set workflow state failed")
	}

	row, err := s.db.GetItemWorkflowState(ctx, repo.ID, input.ItemType, input.Number)
	if err != nil || row == nil {
		return nil, httpapi.Internal("read workflow state failed")
	}
	return &setWorkflowStateOutput{Body: workflowStateChangeResponse{
		PreviousStatus: normalizeWorkflowStatusForAPI(previous),
		Status:         normalizeWorkflowStatusForAPI(row.Status),
		UpdatedAt:      row.UpdatedAt.UTC().Format(time.RFC3339),
		UpdatedSource:  row.UpdatedSource,
		UpdatedActor:   row.UpdatedActor,
		UpdatedReason:  row.UpdatedReason,
	}}, nil
}

func validateSetWorkflowStateBody(body setWorkflowStateBody) error {
	if !validKanbanStates[body.Status] {
		return httpapi.Validation(
			"body.status",
			"status must be one of: new, reviewing, waiting, awaiting_merge",
			"new", "reviewing", "waiting", "awaiting_merge",
		)
	}
	if body.ExpectedStatus != "" && !validKanbanStates[body.ExpectedStatus] {
		return httpapi.Validation(
			"body.expected_status",
			"expected_status must be one of: new, reviewing, waiting, awaiting_merge",
			"new", "reviewing", "waiting", "awaiting_merge",
		)
	}
	forceProvided := body.Force != nil
	force := forceProvided && *body.Force
	if body.ExpectedStatus == "" && !force {
		if forceProvided {
			return httpapi.Validation("body.force", "force must be true when expected_status is omitted")
		}
		return httpapi.Validation("body.expected_status", "expected_status is required unless force is true")
	}
	if body.ExpectedStatus != "" && forceProvided {
		return httpapi.Validation("body.force", "force cannot be provided when expected_status is provided")
	}
	if body.Source != "" && !workflowSourcePattern.MatchString(body.Source) {
		return httpapi.Validation("body.source", "source must match ^[a-z][a-z0-9_-]{0,39}$")
	}
	if len(body.Actor) > 120 {
		return httpapi.Validation("body.actor", "actor must be at most 120 bytes")
	}
	if len(body.Reason) > 500 {
		return httpapi.Validation("body.reason", "reason must be at most 500 bytes")
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

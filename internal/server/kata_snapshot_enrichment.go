package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	katagenerated "go.kenn.io/kata/pkg/client/generated"

	"go.kenn.io/middleman/internal/db"
)

const (
	kataSnapshotEnrichmentStageDetail          = "detail"
	kataSnapshotEnrichmentStageGraph           = "graph"
	kataSnapshotEnrichmentStageHistory         = "history"
	kataSnapshotEnrichmentStageWorkspaceTarget = "workspace_target"
	kataSnapshotHistoryPageLimit               = int64(1000)
	kataSnapshotHistoryResultLimit             = 100
)

type kataSnapshotEnrichmentRequest struct {
	SelectedIssueUID string
	GraphSourceUID   string
}

type kataSnapshotEnrichmentError struct {
	Code    ProblemCode `json:"code"`
	Message string      `json:"message"`
}

type kataSnapshotEnrichment struct {
	SelectedIssueUID string                                    `json:"selected_issue_uid,omitempty"`
	SelectedDetail   *kataTaskDetailResponse                   `json:"selected_detail,omitempty"`
	SelectedHistory  []katagenerated.EventEnvelope             `json:"selected_history,omitempty"`
	Graph            *katagenerated.ReachableGraphResponseBody `json:"graph,omitempty"`
	GraphFetchedAt   *time.Time                                `json:"graph_fetched_at,omitempty"`
	Errors           map[string]kataSnapshotEnrichmentError    `json:"errors,omitempty"`
}

type kataSnapshotEnricherDeps struct {
	client                 kataAPIClient
	resolveWorkspaceTarget func(context.Context, db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error)
	now                    func() time.Time
}

type kataSnapshotEnricher struct {
	client                 kataAPIClient
	resolveWorkspaceTarget func(context.Context, db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error)
	now                    func() time.Time
}

type kataSnapshotEnrichmentIssue struct {
	ID          int64
	UID         string
	ProjectID   int64
	ProjectUID  string
	ProjectName string
	ShortID     string
	QualifiedID string
	Title       string
}

func newKataSnapshotEnricher(deps kataSnapshotEnricherDeps) *kataSnapshotEnricher {
	now := deps.now
	if now == nil {
		now = time.Now
	}
	resolveWorkspaceTarget := deps.resolveWorkspaceTarget
	if resolveWorkspaceTarget == nil {
		resolveWorkspaceTarget = func(context.Context, db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error) {
			return kataWorkspaceTargetResponse{Available: false}, nil
		}
	}
	return &kataSnapshotEnricher{
		client:                 deps.client,
		resolveWorkspaceTarget: resolveWorkspaceTarget,
		now:                    now,
	}
}

func (e *kataSnapshotEnricher) Enrich(
	ctx context.Context,
	authority kataCoordinatedAuthority,
	request kataSnapshotEnrichmentRequest,
) (kataSnapshotEnrichment, error) {
	result := kataSnapshotEnrichment{}
	projectsByID := make(map[int64]kataProjectSummary, len(authority.Snapshot.Projects))
	for _, project := range authority.Snapshot.Projects {
		projectsByID[project.ID] = project
	}
	memberUIDs := make(map[string]struct{}, len(authority.Snapshot.MemberIssueUIDs))
	for _, uid := range authority.Snapshot.MemberIssueUIDs {
		memberUIDs[uid] = struct{}{}
	}
	members := make(map[string]kataSnapshotEnrichmentIssue, len(memberUIDs))
	for _, issue := range authority.Snapshot.Issues {
		if _, isMember := memberUIDs[issue.UID]; !isMember {
			continue
		}
		members[issue.UID] = kataSnapshotEnrichmentIssue{
			ID:          issue.ID,
			UID:         issue.UID,
			ProjectID:   issue.ProjectID,
			ProjectUID:  issue.ProjectUID,
			ProjectName: issue.ProjectName,
			ShortID:     issue.ShortID,
			QualifiedID: issue.QualifiedID,
			Title:       issue.Title,
		}
	}

	selected, selectedIsMember := members[request.SelectedIssueUID]
	graphSource, graphSourceIsMember := members[request.GraphSourceUID]
	if request.GraphSourceUID != "" && graphSourceIsMember {
		graph, graphNodes, err := e.loadGraph(ctx, graphSource, projectsByID)
		if err != nil {
			if contextErr := kataEnrichmentContextError(ctx, err); contextErr != nil {
				return kataSnapshotEnrichment{}, contextErr
			}
			result.addError(kataSnapshotEnrichmentStageGraph, CodeUpstreamError, "Could not load reachable graph.")
		} else {
			result.Graph = graph
			fetchedAt := e.now().UTC()
			result.GraphFetchedAt = &fetchedAt
			if !selectedIsMember {
				selected, selectedIsMember = graphNodes[request.SelectedIssueUID]
			}
		}
	}

	if request.SelectedIssueUID == "" || !selectedIsMember {
		return result, nil
	}
	result.SelectedIssueUID = request.SelectedIssueUID
	detail, issue, err := e.loadDetail(ctx, selected)
	if err != nil {
		if contextErr := kataEnrichmentContextError(ctx, err); contextErr != nil {
			return kataSnapshotEnrichment{}, contextErr
		}
		result.addError(kataSnapshotEnrichmentStageDetail, CodeUpstreamError, "Could not load selected task detail.")
	} else {
		result.SelectedDetail = &kataTaskDetailResponse{Detail: detail, ETag: issue.etag}

		target, targetErr := e.resolveWorkspaceTarget(ctx, db.WorkspaceKataMetadata{
			DaemonID:    authority.DaemonID,
			ProjectUID:  selected.ProjectUID,
			ProjectName: selected.ProjectName,
			IssueUID:    issue.value.UID,
			ShortID:     issue.value.ShortID,
			QualifiedID: selected.QualifiedID,
			Title:       issue.value.Title,
		})
		if targetErr != nil {
			if contextErr := kataEnrichmentContextError(ctx, targetErr); contextErr != nil {
				return kataSnapshotEnrichment{}, contextErr
			}
			result.addError(kataSnapshotEnrichmentStageWorkspaceTarget, CodeInternalError, "Could not resolve workspace target.")
		} else {
			if err := ctx.Err(); err != nil {
				return kataSnapshotEnrichment{}, err
			}
			result.SelectedDetail.WorkspaceTarget = target
		}
	}

	history, err := e.loadHistory(ctx, selected.UID)
	if err != nil {
		if contextErr := kataEnrichmentContextError(ctx, err); contextErr != nil {
			return kataSnapshotEnrichment{}, contextErr
		}
		result.addError(kataSnapshotEnrichmentStageHistory, CodeUpstreamError, "Could not load selected task history.")
	} else {
		result.SelectedHistory = history
	}
	return result, nil
}

func (e *kataSnapshotEnricher) loadGraph(
	ctx context.Context,
	source kataSnapshotEnrichmentIssue,
	projectsByID map[int64]kataProjectSummary,
) (*katagenerated.ReachableGraphResponseBody, map[string]kataSnapshotEnrichmentIssue, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	depth := "full"
	hideDone := false
	response, err := e.client.ReachableIssueGraphWithResponse(ctx, &katagenerated.ReachableIssueGraphRequestOptions{
		PathParams: &katagenerated.ReachableIssueGraphPath{ProjectID: source.ProjectID, Ref: source.UID},
		Query:      &katagenerated.ReachableIssueGraphQuery{Depth: &depth, HideDone: &hideDone},
	})
	if err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if response == nil || response.StatusCode != http.StatusOK || response.JSON200 == nil {
		return nil, nil, fmt.Errorf("missing graph 200 response body")
	}
	graph := response.JSON200
	if err := graph.Validate(); err != nil {
		return nil, nil, fmt.Errorf("validate graph response: %w", err)
	}
	if graph.SourceUID != source.UID || graph.Depth != depth || graph.HideDone != hideDone {
		return nil, nil, fmt.Errorf("graph response does not match request")
	}
	nodes := make(map[string]kataSnapshotEnrichmentIssue, len(graph.Nodes))
	nodeIDs := make(map[int64]struct{}, len(graph.Nodes))
	for _, node := range graph.Nodes {
		project, projectExists := projectsByID[node.ProjectID]
		if node.ID <= 0 || node.ProjectID <= 0 || strings.TrimSpace(node.UID) == "" || node.ProjectUID == nil || !projectExists || *node.ProjectUID != project.UID {
			return nil, nil, fmt.Errorf("graph node identity does not match project catalog")
		}
		if _, duplicate := nodes[node.UID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate graph node UID")
		}
		if _, duplicate := nodeIDs[node.ID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate graph node ID")
		}
		nodeIDs[node.ID] = struct{}{}
		nodes[node.UID] = kataSnapshotEnrichmentIssue{
			ID:          node.ID,
			UID:         node.UID,
			ProjectID:   node.ProjectID,
			ProjectUID:  *node.ProjectUID,
			ProjectName: project.Name,
			ShortID:     node.ShortID,
			QualifiedID: node.QualifiedID,
			Title:       node.Title,
		}
	}
	sourceNode, ok := nodes[source.UID]
	if !ok {
		return nil, nil, fmt.Errorf("graph response omits source node")
	}
	if sourceNode.ID != source.ID || sourceNode.ProjectID != source.ProjectID || sourceNode.ProjectUID != source.ProjectUID {
		return nil, nil, fmt.Errorf("graph source identity does not match authority")
	}
	return graph, nodes, nil
}

type kataGeneratedIssueDetail struct {
	value katagenerated.Issue
	etag  string
}

func (e *kataSnapshotEnricher) loadDetail(
	ctx context.Context,
	selected kataSnapshotEnrichmentIssue,
) (*katagenerated.ShowIssueResponseBody, kataGeneratedIssueDetail, error) {
	if err := ctx.Err(); err != nil {
		return nil, kataGeneratedIssueDetail{}, err
	}
	includeDeleted := false
	response, err := e.client.ShowIssueByUIDWithResponse(ctx, &katagenerated.ShowIssueByUIDRequestOptions{
		PathParams: &katagenerated.ShowIssueByUIDPath{UID: selected.UID},
		Query:      &katagenerated.ShowIssueByUIDQuery{IncludeDeleted: &includeDeleted},
	})
	if err != nil {
		return nil, kataGeneratedIssueDetail{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, kataGeneratedIssueDetail{}, err
	}
	if response == nil || response.StatusCode != http.StatusOK || response.JSON200 == nil {
		return nil, kataGeneratedIssueDetail{}, fmt.Errorf("missing detail 200 response body")
	}
	if err := response.JSON200.Validate(); err != nil {
		return nil, kataGeneratedIssueDetail{}, fmt.Errorf("validate detail response: %w", err)
	}
	issue := response.JSON200.Issue
	if issue.ID <= 0 || issue.ProjectID <= 0 || issue.ID != selected.ID || issue.UID != selected.UID || issue.ProjectID != selected.ProjectID || issue.ProjectUID == nil || *issue.ProjectUID != selected.ProjectUID || issue.DeletedAt != nil {
		return nil, kataGeneratedIssueDetail{}, fmt.Errorf("detail response identity does not match selection")
	}
	etag := ""
	if response.HTTPResponse != nil {
		etag = response.HTTPResponse.Header.Get("ETag")
	}
	return response.JSON200, kataGeneratedIssueDetail{value: issue, etag: etag}, nil
}

func (e *kataSnapshotEnricher) loadHistory(ctx context.Context, selectedUID string) ([]katagenerated.EventEnvelope, error) {
	history := make([]katagenerated.EventEnvelope, 0, kataSnapshotHistoryResultLimit)
	afterID := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		limit := kataSnapshotHistoryPageLimit
		response, err := e.client.PollEventsWithResponse(ctx, &katagenerated.PollEventsRequestOptions{
			Query: &katagenerated.PollEventsQuery{AfterID: &afterID, Limit: &limit},
		})
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if response == nil || response.StatusCode != http.StatusOK || response.JSON200 == nil {
			return nil, fmt.Errorf("missing events 200 response body")
		}
		body := response.JSON200
		if err := body.Validate(); err != nil {
			return nil, fmt.Errorf("validate events response: %w", err)
		}
		if body.ResetRequired || body.ResetAfterID != nil {
			return nil, fmt.Errorf("events response requires cursor reset")
		}
		if len(body.Events) == 0 {
			return history, nil
		}

		lastEventID := afterID
		for _, event := range body.Events {
			if event.EventID <= 0 || event.EventID <= lastEventID {
				return nil, fmt.Errorf("events response contains non-monotonic event ids")
			}
			lastEventID = event.EventID
		}
		if body.NextAfterID <= afterID || body.NextAfterID < lastEventID {
			return nil, fmt.Errorf("events response cursor made no progress")
		}

		for _, event := range body.Events {
			if event.IssueUID == nil || *event.IssueUID != selectedUID {
				continue
			}
			event.CreatedAt = event.CreatedAt.UTC()
			history = append(history, event)
			if len(history) == kataSnapshotHistoryResultLimit {
				return history, nil
			}
		}
		afterID = body.NextAfterID
	}
}

func (r *kataSnapshotEnrichment) addError(stage string, code ProblemCode, message string) {
	if r.Errors == nil {
		r.Errors = make(map[string]kataSnapshotEnrichmentError)
	}
	r.Errors[stage] = kataSnapshotEnrichmentError{Code: code, Message: message}
}

func kataEnrichmentContextError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

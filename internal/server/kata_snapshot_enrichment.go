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
	SelectedDetail   *kataSnapshotSelectedDetail               `json:"selected_detail,omitempty"`
	SelectedHistory  []katagenerated.EventEnvelope             `json:"selected_history,omitempty"`
	Graph            *katagenerated.ReachableGraphResponseBody `json:"graph,omitempty"`
	GraphFetchedAt   *time.Time                                `json:"graph_fetched_at,omitempty"`
	Errors           map[string]kataSnapshotEnrichmentError    `json:"errors,omitempty"`
}

type kataSnapshotSelectedDetail struct {
	Detail          any                         `json:"detail" doc:"Verbatim Kata daemon issue detail payload"`
	ETag            string                      `json:"etag,omitempty" doc:"Daemon issue detail ETag, when the daemon provided one"`
	WorkspaceTarget kataWorkspaceTargetResponse `json:"workspace_target"`
}

type kataSnapshotEnricherDeps struct {
	client                 kataAPIClient
	cache                  *kataSnapshotEnrichmentCache
	resolveWorkspaceTarget func(context.Context, db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error)
	now                    func() time.Time
}

type kataSnapshotEnricher struct {
	client                 kataAPIClient
	cache                  *kataSnapshotEnrichmentCache
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
		cache:                  deps.cache,
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
	if request.SelectedIssueUID != "" && selectedIsMember {
		result.SelectedIssueUID = request.SelectedIssueUID
		if err := e.enrichSelected(ctx, authority, selected, &result); err != nil {
			return kataSnapshotEnrichment{}, err
		}
		if request.GraphSourceUID == "" || !graphSourceIsMember {
			return result, nil
		}

		graph, _, graphFetchedAt, err := e.loadGraph(ctx, authority, graphSource, projectsByID)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return kataSnapshotEnrichment{}, contextErr
			}
			result.addError(kataSnapshotEnrichmentStageGraph, CodeUpstreamError, "Could not load reachable graph.")
			return result, nil
		}
		result.Graph = graph
		result.GraphFetchedAt = &graphFetchedAt
		return result, nil
	}

	if request.GraphSourceUID != "" && graphSourceIsMember {
		graph, graphNodes, graphFetchedAt, err := e.loadGraph(ctx, authority, graphSource, projectsByID)
		if err != nil {
			if contextErr := kataEnrichmentContextError(ctx, err); contextErr != nil {
				return kataSnapshotEnrichment{}, contextErr
			}
			result.addError(kataSnapshotEnrichmentStageGraph, CodeUpstreamError, "Could not load reachable graph.")
		} else {
			result.Graph = graph
			result.GraphFetchedAt = &graphFetchedAt
			if !selectedIsMember {
				selected, selectedIsMember = graphNodes[request.SelectedIssueUID]
			}
		}
	}

	if request.SelectedIssueUID == "" || !selectedIsMember {
		return result, nil
	}
	result.SelectedIssueUID = request.SelectedIssueUID
	if err := e.enrichSelected(ctx, authority, selected, &result); err != nil {
		return kataSnapshotEnrichment{}, err
	}
	return result, nil
}

func (e *kataSnapshotEnricher) enrichSelected(
	ctx context.Context,
	authority kataCoordinatedAuthority,
	selected kataSnapshotEnrichmentIssue,
	result *kataSnapshotEnrichment,
) error {
	detail, issue, err := e.loadDetail(ctx, authority, selected)
	if err != nil {
		if contextErr := kataEnrichmentContextError(ctx, err); contextErr != nil {
			return contextErr
		}
		result.addError(kataSnapshotEnrichmentStageDetail, CodeUpstreamError, "Could not load selected task detail.")
	} else {
		result.SelectedDetail = &kataSnapshotSelectedDetail{Detail: detail, ETag: issue.ETag}

		target, targetErr := e.resolveWorkspaceTarget(ctx, db.WorkspaceKataMetadata{
			DaemonID:    authority.DaemonID,
			ProjectUID:  selected.ProjectUID,
			ProjectName: selected.ProjectName,
			IssueUID:    issue.Issue.UID,
			ShortID:     issue.Issue.ShortID,
			QualifiedID: selected.QualifiedID,
			Title:       issue.Issue.Title,
		})
		if targetErr != nil {
			if contextErr := kataEnrichmentContextError(ctx, targetErr); contextErr != nil {
				return contextErr
			}
			result.addError(kataSnapshotEnrichmentStageWorkspaceTarget, CodeInternalError, "Could not resolve workspace target.")
		} else {
			if err := ctx.Err(); err != nil {
				return err
			}
			result.SelectedDetail.WorkspaceTarget = target
		}
	}

	history, err := e.loadHistory(ctx, authority, selected.ProjectID, selected.UID)
	if err != nil {
		if contextErr := kataEnrichmentContextError(ctx, err); contextErr != nil {
			return contextErr
		}
		result.addError(kataSnapshotEnrichmentStageHistory, CodeUpstreamError, "Could not load selected task history.")
	} else {
		result.SelectedHistory = history
	}
	return nil
}

func (e *kataSnapshotEnricher) loadGraph(
	ctx context.Context,
	authority kataCoordinatedAuthority,
	source kataSnapshotEnrichmentIssue,
	projectsByID map[int64]kataProjectSummary,
) (*katagenerated.ReachableGraphResponseBody, map[string]kataSnapshotEnrichmentIssue, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, time.Time{}, err
	}
	depth := "full"
	hideDone := false
	load := func(loadCtx context.Context) (kataCachedGraph, error) {
		graph, err := e.loadGraphResponse(loadCtx, source, depth, hideDone)
		if err != nil {
			return kataCachedGraph{}, err
		}
		if _, err := validateKataGraph(graph, source, projectsByID); err != nil {
			return kataCachedGraph{}, err
		}
		return kataCachedGraph{Body: graph, FetchedAt: e.now().UTC()}, nil
	}
	var cachedGraph kataCachedGraph
	var err error
	if e.cache == nil {
		cachedGraph, err = load(ctx)
	} else {
		cachedGraph, err = e.cache.graph(ctx, kataGraphCacheKey{
			DaemonID: authority.DaemonID, DaemonEpoch: authority.InvalidationEpoch,
			SourceUID: source.UID, Depth: depth, HideDone: hideDone,
		}, load)
	}
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	nodes, err := validateKataGraph(cachedGraph.Body, source, projectsByID)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return cachedGraph.Body, nodes, cachedGraph.FetchedAt, nil
}

func (e *kataSnapshotEnricher) loadGraphResponse(
	ctx context.Context,
	source kataSnapshotEnrichmentIssue,
	depth string,
	hideDone bool,
) (*katagenerated.ReachableGraphResponseBody, error) {
	response, err := e.client.ReachableIssueGraphWithResponse(ctx, &katagenerated.ReachableIssueGraphRequestOptions{
		PathParams: &katagenerated.ReachableIssueGraphPath{ProjectID: source.ProjectID, Ref: source.UID},
		Query:      &katagenerated.ReachableIssueGraphQuery{Depth: &depth, HideDone: &hideDone},
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if response == nil || response.StatusCode != http.StatusOK || response.JSON200 == nil {
		return nil, fmt.Errorf("missing graph 200 response body")
	}
	graph := response.JSON200
	if err := graph.Validate(); err != nil {
		return nil, fmt.Errorf("validate graph response: %w", err)
	}
	if graph.SourceUID != source.UID || graph.Depth != depth || graph.HideDone != hideDone {
		return nil, fmt.Errorf("graph response does not match request")
	}
	return graph, nil
}

func validateKataGraph(
	graph *katagenerated.ReachableGraphResponseBody,
	source kataSnapshotEnrichmentIssue,
	projectsByID map[int64]kataProjectSummary,
) (map[string]kataSnapshotEnrichmentIssue, error) {
	nodes := make(map[string]kataSnapshotEnrichmentIssue, len(graph.Nodes))
	nodeIDs := make(map[int64]struct{}, len(graph.Nodes))
	for _, node := range graph.Nodes {
		project, projectExists := projectsByID[node.ProjectID]
		if node.ID <= 0 || node.ProjectID <= 0 || strings.TrimSpace(node.UID) == "" || node.ProjectUID == nil || !projectExists || *node.ProjectUID != project.UID || node.QualifiedID != project.Name+"#"+node.ShortID {
			return nil, fmt.Errorf("graph node identity does not match project catalog")
		}
		if _, duplicate := nodes[node.UID]; duplicate {
			return nil, fmt.Errorf("duplicate graph node UID")
		}
		if _, duplicate := nodeIDs[node.ID]; duplicate {
			return nil, fmt.Errorf("duplicate graph node ID")
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
		return nil, fmt.Errorf("graph response omits source node")
	}
	if sourceNode.ID != source.ID || sourceNode.ProjectID != source.ProjectID || sourceNode.ProjectUID != source.ProjectUID {
		return nil, fmt.Errorf("graph source identity does not match authority")
	}
	adjacent := make(map[string][]string, len(nodes))
	for _, edge := range graph.Edges {
		if _, ok := nodes[edge.FromUID]; !ok {
			return nil, fmt.Errorf("graph edge source is not in node set")
		}
		if _, ok := nodes[edge.ToUID]; !ok {
			return nil, fmt.Errorf("graph edge target is not in node set")
		}
		adjacent[edge.FromUID] = append(adjacent[edge.FromUID], edge.ToUID)
		adjacent[edge.ToUID] = append(adjacent[edge.ToUID], edge.FromUID)
	}
	reachable := make(map[string]kataSnapshotEnrichmentIssue, len(nodes))
	reachable[source.UID] = sourceNode
	queue := []string{source.UID}
	for len(queue) > 0 {
		uid := queue[0]
		queue = queue[1:]
		for _, adjacentUID := range adjacent[uid] {
			if _, seen := reachable[adjacentUID]; seen {
				continue
			}
			reachable[adjacentUID] = nodes[adjacentUID]
			queue = append(queue, adjacentUID)
		}
	}
	return reachable, nil
}

func (e *kataSnapshotEnricher) loadDetail(
	ctx context.Context,
	authority kataCoordinatedAuthority,
	selected kataSnapshotEnrichmentIssue,
) (*katagenerated.ShowIssueResponseBody, kataCachedIssueDetail, error) {
	load := func(loadCtx context.Context) (kataCachedIssueDetail, error) {
		return e.loadDetailResponse(loadCtx, selected)
	}
	var detail kataCachedIssueDetail
	var err error
	if e.cache == nil {
		detail, err = load(ctx)
	} else {
		detail, err = e.cache.issueDetail(ctx, kataIssueDetailCacheKey{
			DaemonID: authority.DaemonID, DaemonEpoch: authority.InvalidationEpoch, IssueUID: selected.UID,
		}, load)
	}
	if err != nil {
		return nil, kataCachedIssueDetail{}, err
	}
	return detail.Body, detail, nil
}

func (e *kataSnapshotEnricher) loadDetailResponse(
	ctx context.Context,
	selected kataSnapshotEnrichmentIssue,
) (kataCachedIssueDetail, error) {
	if err := ctx.Err(); err != nil {
		return kataCachedIssueDetail{}, err
	}
	includeDeleted := false
	response, err := e.client.ShowIssueByUIDWithResponse(ctx, &katagenerated.ShowIssueByUIDRequestOptions{
		PathParams: &katagenerated.ShowIssueByUIDPath{UID: selected.UID},
		Query:      &katagenerated.ShowIssueByUIDQuery{IncludeDeleted: &includeDeleted},
	})
	if err != nil {
		return kataCachedIssueDetail{}, err
	}
	if err := ctx.Err(); err != nil {
		return kataCachedIssueDetail{}, err
	}
	if response == nil || response.StatusCode != http.StatusOK || response.JSON200 == nil {
		return kataCachedIssueDetail{}, fmt.Errorf("missing detail 200 response body")
	}
	if err := response.JSON200.Validate(); err != nil {
		return kataCachedIssueDetail{}, fmt.Errorf("validate detail response: %w", err)
	}
	issue := response.JSON200.Issue
	if issue.ID <= 0 || issue.ProjectID <= 0 || issue.ID != selected.ID || issue.UID != selected.UID || issue.ProjectID != selected.ProjectID || issue.ProjectUID == nil || *issue.ProjectUID != selected.ProjectUID || issue.ShortID != selected.ShortID || issue.DeletedAt != nil {
		return kataCachedIssueDetail{}, fmt.Errorf("detail response identity does not match selection")
	}
	etag := ""
	if response.HTTPResponse != nil {
		etag = response.HTTPResponse.Header.Get("ETag")
	}
	return kataCachedIssueDetail{Body: response.JSON200, Issue: issue, ETag: etag}, nil
}

func (e *kataSnapshotEnricher) loadHistory(
	ctx context.Context,
	authority kataCoordinatedAuthority,
	projectID int64,
	selectedUID string,
) ([]katagenerated.EventEnvelope, error) {
	if e.cache == nil {
		loaded, err := e.loadProjectEvents(ctx, projectID, selectedUID, 0)
		if err != nil {
			return nil, err
		}
		return loaded.SelectedHistory, nil
	}
	events, err := e.cache.projectEvents(ctx, kataProjectEventsCacheKey{
		DaemonID: authority.DaemonID, DaemonEpoch: authority.InvalidationEpoch, ProjectID: projectID,
	}, selectedUID, func(loadCtx context.Context, maxBytes uint64) (kataProjectEventsLoadResult, error) {
		return e.loadProjectEvents(loadCtx, projectID, selectedUID, maxBytes)
	})
	if err != nil {
		return nil, err
	}
	if !events.CompleteProject {
		return events.Events, nil
	}
	return filterKataProjectEvents(events.Events, selectedUID), nil
}

func filterKataProjectEvents(
	events []katagenerated.EventEnvelope,
	selectedUID string,
) []katagenerated.EventEnvelope {
	history := make([]katagenerated.EventEnvelope, 0)
	for _, event := range events {
		if event.IssueUID != nil && *event.IssueUID == selectedUID {
			history = append(history, event)
		}
	}
	return history
}

func (e *kataSnapshotEnricher) loadProjectEvents(
	ctx context.Context,
	projectID int64,
	selectedUID string,
	maxBytes uint64,
) (kataProjectEventsLoadResult, error) {
	loaded := newKataProjectEventsLoadResult(maxBytes)
	afterID := int64(0)
	resetHandled := false
	for {
		if err := ctx.Err(); err != nil {
			return kataProjectEventsLoadResult{}, err
		}
		limit := kataSnapshotHistoryPageLimit
		response, err := e.client.PollProjectEventsWithResponse(ctx, &katagenerated.PollProjectEventsRequestOptions{
			PathParams: &katagenerated.PollProjectEventsPath{ProjectID: projectID},
			Query:      &katagenerated.PollProjectEventsQuery{AfterID: &afterID, Limit: &limit},
		})
		if err != nil {
			return kataProjectEventsLoadResult{}, err
		}
		if err := ctx.Err(); err != nil {
			return kataProjectEventsLoadResult{}, err
		}
		body, resetRequired, err := validateKataHistoryPage(response, afterID, resetHandled)
		if err != nil {
			return kataProjectEventsLoadResult{}, err
		}
		if resetRequired {
			loaded = newKataProjectEventsLoadResult(maxBytes)
			afterID = body.NextAfterID
			resetHandled = true
			continue
		}
		if len(body.Events) == 0 {
			return loaded, nil
		}
		for _, event := range body.Events {
			event.CreatedAt = event.CreatedAt.UTC()
			if event.IssueUID != nil && *event.IssueUID == selectedUID {
				loaded.SelectedHistory = append(loaded.SelectedHistory, event)
			}
			loaded.admitProjectEvent(event, maxBytes)
		}
		afterID = body.NextAfterID
	}
}

func newKataProjectEventsLoadResult(maxBytes uint64) kataProjectEventsLoadResult {
	return kataProjectEventsLoadResult{SerializedCost: 2, Cacheable: maxBytes >= 2}
}

func (r *kataProjectEventsLoadResult) admitProjectEvent(event katagenerated.EventEnvelope, maxBytes uint64) {
	if !r.Cacheable {
		return
	}
	separatorCost := uint64(0)
	if len(r.ProjectEvents) > 0 {
		separatorCost = 1
	}
	eventCost := kataSerializedCost(event)
	remaining := maxBytes - r.SerializedCost
	if separatorCost > remaining || eventCost > remaining-separatorCost {
		r.ProjectEvents = nil
		r.SerializedCost = 0
		r.Cacheable = false
		return
	}
	r.SerializedCost += separatorCost + eventCost
	r.ProjectEvents = append(r.ProjectEvents, event)
}

func validateKataHistoryPage(
	response *katagenerated.PollProjectEventsResp,
	afterID int64,
	resetHandled bool,
) (*katagenerated.PollEventsBody, bool, error) {
	if response == nil || response.StatusCode != http.StatusOK || response.JSON200 == nil {
		return nil, false, fmt.Errorf("missing events 200 response body")
	}
	body := response.JSON200
	if err := body.Validate(); err != nil {
		return nil, false, fmt.Errorf("validate events response: %w", err)
	}
	if body.ResetRequired {
		if resetHandled {
			return nil, false, fmt.Errorf("events response repeated cursor reset")
		}
		if body.ResetAfterID == nil {
			return nil, false, fmt.Errorf("events response cursor reset is missing reset cursor")
		}
		if len(body.Events) != 0 {
			return nil, false, fmt.Errorf("events response cursor reset contains events")
		}
		if *body.ResetAfterID <= afterID {
			return nil, false, fmt.Errorf("events response cursor reset does not advance")
		}
		if body.NextAfterID != *body.ResetAfterID {
			return nil, false, fmt.Errorf("events response reset cursor does not match next cursor")
		}
		return body, true, nil
	}
	if body.ResetAfterID != nil {
		return nil, false, fmt.Errorf("events response includes reset cursor without reset")
	}
	if len(body.Events) == 0 {
		if body.NextAfterID != afterID {
			return nil, false, fmt.Errorf("events response cursor does not match empty page")
		}
		return body, false, nil
	}
	lastEventID := afterID
	for _, event := range body.Events {
		if event.EventID <= 0 || event.EventID <= lastEventID {
			return nil, false, fmt.Errorf("events response contains non-monotonic event ids")
		}
		lastEventID = event.EventID
	}
	if body.NextAfterID != lastEventID {
		return nil, false, fmt.Errorf("events response cursor does not match last event")
	}
	return body, false, nil
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

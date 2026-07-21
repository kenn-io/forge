package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	katagenerated "go.kenn.io/kata/pkg/client/generated"
)

type kataAuthorityRequest struct {
	Scope      string `json:"scope"`
	ProjectUID string `json:"project_uid,omitempty"`
	Authority  string `json:"authority"`
}

type kataSnapshotLoader struct {
	client kataAPIClient
	now    func() time.Time
}

func (l *kataSnapshotLoader) Load(ctx context.Context, request kataAuthorityRequest) (kataAuthoritySnapshot, error) {
	if err := validateKataAuthorityRequest(request); err != nil {
		return kataAuthoritySnapshot{}, err
	}
	projects, projectsByID, projectID, err := l.loadProjects(ctx, request)
	if err != nil {
		return kataAuthoritySnapshot{}, err
	}
	var issues []kataTaskSummary
	switch request.Authority {
	case "ready":
		issues, err = l.loadReady(ctx, request, projectID, projectsByID)
	case "open", "closed", "all":
		issues, err = l.loadIssues(ctx, request.Authority, projectID, projectsByID)
	default:
		return kataAuthoritySnapshot{}, problemValidation("authority", "unsupported Kata authority", "open", "ready", "closed", "all")
	}
	if err != nil {
		return kataAuthoritySnapshot{}, err
	}
	confirmedProjects, _, err := l.loadProjectCatalog(ctx)
	if err != nil {
		return kataAuthoritySnapshot{}, err
	}
	if err := validateKataProjectCatalogStable(projects, confirmedProjects); err != nil {
		return kataAuthoritySnapshot{}, err
	}
	if err := validateKataAuthorityCounts(request, projects, issues); err != nil {
		return kataAuthoritySnapshot{}, err
	}

	memberIssueUIDs := make([]string, len(issues))
	for i := range issues {
		memberIssueUIDs[i] = issues[i].UID
	}
	now := time.Now
	if l.now != nil {
		now = l.now
	}
	return kataAuthoritySnapshot{
		FetchedAt:       now().UTC(),
		Projects:        projects,
		MemberIssueUIDs: memberIssueUIDs,
		Issues:          issues,
	}, nil
}

func (l *kataSnapshotLoader) loadProjects(
	ctx context.Context,
	request kataAuthorityRequest,
) ([]kataProjectSummary, map[int64]kataProjectSummary, *int64, error) {
	projects, projectsByID, err := l.loadProjectCatalog(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	var projectID *int64
	for _, project := range projects {
		if request.Scope == "project" && project.UID == request.ProjectUID {
			projectID = new(project.ID)
		}
	}
	if request.Scope == "global" {
		return projects, projectsByID, nil, nil
	}
	if request.Scope != "project" {
		return nil, nil, nil, problemValidation("scope", "unsupported Kata scope", "global", "project")
	}
	if projectID == nil {
		return nil, nil, nil, problemNotFound(CodeProjectNotFound, "Kata project not found", map[string]any{"projectUid": request.ProjectUID})
	}
	return projects, projectsByID, projectID, nil
}

func (l *kataSnapshotLoader) loadProjectCatalog(
	ctx context.Context,
) ([]kataProjectSummary, map[int64]kataProjectSummary, error) {
	include := "stats"
	response, err := l.client.ListProjectsWithResponse(ctx, &katagenerated.ListProjectsRequestOptions{
		Query: &katagenerated.ListProjectsQuery{Include: &include},
	})
	if err != nil {
		return nil, nil, kataSnapshotUpstreamError("list projects", err)
	}
	if response == nil || response.StatusCode != http.StatusOK || response.JSON200 == nil {
		return nil, nil, kataSnapshotUpstreamError("list projects", fmt.Errorf("missing 200 response body"))
	}
	if err := validateKataProjectsResponse(response.JSON200); err != nil {
		return nil, nil, kataSnapshotUpstreamError("validate projects", err)
	}

	projects := make([]kataProjectSummary, len(response.JSON200.Projects))
	projectsByID := make(map[int64]kataProjectSummary, len(projects))
	for i, project := range response.JSON200.Projects {
		projects[i] = kataProjectSummaryFromGenerated(project)
		projectsByID[project.ID] = projects[i]
	}
	return projects, projectsByID, nil
}

func (l *kataSnapshotLoader) loadReady(
	ctx context.Context,
	request kataAuthorityRequest,
	projectID *int64,
	projectsByID map[int64]kataProjectSummary,
) ([]kataTaskSummary, error) {
	if request.Scope == "global" {
		response, err := l.client.ReadyIssuesGlobalWithResponse(ctx, &katagenerated.ReadyIssuesGlobalRequestOptions{})
		if err != nil {
			return nil, kataSnapshotUpstreamError("list global ready issues", err)
		}
		if response == nil || response.StatusCode != http.StatusOK || response.JSON200 == nil {
			return nil, kataSnapshotUpstreamError("list global ready issues", fmt.Errorf("missing 200 response body"))
		}
		if err := validateKataGlobalReadyResponse(response.JSON200); err != nil {
			return nil, kataSnapshotUpstreamError("validate global ready issues", err)
		}
		for _, issue := range response.JSON200.Issues {
			if issue.Status != "open" {
				return nil, kataSnapshotUpstreamError("validate global ready issues", fmt.Errorf("issue %q is not open", issue.UID))
			}
		}
		issues := make([]kataTaskSummary, len(response.JSON200.Issues))
		for i, issue := range response.JSON200.Issues {
			project, ok := projectsByID[issue.ProjectID]
			if !ok || issue.ProjectName != project.Name {
				return nil, kataAuthorityInconsistency(
					"issue %q has inconsistent project name %q", issue.UID, issue.ProjectName,
				)
			}
			issues[i], err = kataTaskSummaryFromGenerated(kataIssueOutFromGlobalReady(issue), projectsByID, nil)
			if err != nil {
				return nil, err
			}
		}
		return issues, nil
	}
	if projectID == nil {
		return nil, kataAuthorityInconsistency("project ready authority has no project ID")
	}

	response, err := l.client.ReadyIssuesWithResponse(ctx, &katagenerated.ReadyIssuesRequestOptions{
		PathParams: &katagenerated.ReadyIssuesPath{ProjectID: *projectID},
	})
	if err != nil {
		return nil, kataSnapshotUpstreamError("list project ready issues", err)
	}
	if response != nil && response.StatusCode == http.StatusNotFound {
		return nil, kataAuthorityInconsistency("project disappeared before ready issues were read")
	}
	if response == nil || response.StatusCode != http.StatusOK || response.JSON200 == nil {
		return nil, kataSnapshotUpstreamError("list project ready issues", fmt.Errorf("missing 200 response body"))
	}
	if err := validateKataIssues(response.JSON200.Issues); err != nil {
		return nil, kataSnapshotUpstreamError("validate project ready issues", err)
	}
	if err := validateKataAuthorityStatus(response.JSON200.Issues, "open"); err != nil {
		return nil, kataSnapshotUpstreamError("validate project ready issues", err)
	}
	return kataTaskSummariesFromGenerated(response.JSON200.Issues, projectsByID, projectID)
}

func (l *kataSnapshotLoader) loadIssues(
	ctx context.Context,
	authority string,
	projectID *int64,
	projectsByID map[int64]kataProjectSummary,
) ([]kataTaskSummary, error) {
	query := &katagenerated.ListAllIssuesQuery{ProjectID: projectID}
	switch authority {
	case "open":
		query.Status = new(katagenerated.ListAllIssuesQueryStatusOpen)
	case "closed":
		query.Status = new(katagenerated.ListAllIssuesQueryStatusClosed)
	}
	response, err := l.client.ListAllIssuesWithResponse(ctx, &katagenerated.ListAllIssuesRequestOptions{Query: query})
	if err != nil {
		return nil, kataSnapshotUpstreamError("list issues", err)
	}
	if projectID != nil && response != nil && response.StatusCode == http.StatusNotFound {
		return nil, kataAuthorityInconsistency("project disappeared before issues were read")
	}
	if response == nil || response.StatusCode != http.StatusOK || response.JSON200 == nil {
		return nil, kataSnapshotUpstreamError("list issues", fmt.Errorf("missing 200 response body"))
	}
	if err := validateKataIssues(response.JSON200.Issues); err != nil {
		return nil, kataSnapshotUpstreamError("validate issues", err)
	}
	if authority != "all" {
		if err := validateKataAuthorityStatus(response.JSON200.Issues, authority); err != nil {
			return nil, kataSnapshotUpstreamError("validate issues", err)
		}
	}
	return kataTaskSummariesFromGenerated(response.JSON200.Issues, projectsByID, projectID)
}

func kataSnapshotUpstreamError(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return problemUpstream(fmt.Sprintf("Kata %s failed: %v", operation, err), "", "")
}

func kataProjectSummaryFromGenerated(project katagenerated.ProjectOut) kataProjectSummary {
	return kataProjectSummary{
		ID:          project.ID,
		UID:         project.UID,
		Name:        project.Name,
		Metadata:    cloneKataMetadata(project.Metadata),
		Revision:    project.Revision,
		CreatedAt:   project.CreatedAt.UTC(),
		DeletedAt:   kataTimePointerUTC(project.DeletedAt),
		OpenCount:   project.Stats.Open,
		ClosedCount: project.Stats.Closed,
		LastEventAt: kataTimePointerUTC(project.Stats.LastEventAt),
	}
}

func validateKataProjectCatalogStable(before, after []kataProjectSummary) error {
	if len(before) != len(after) {
		return kataAuthorityInconsistency("project catalog size changed from %d to %d between reads", len(before), len(after))
	}
	afterByID := make(map[int64]kataProjectSummary, len(after))
	for _, project := range after {
		afterByID[project.ID] = project
	}
	for _, first := range before {
		second, ok := afterByID[first.ID]
		if !ok {
			return kataAuthorityInconsistency("project %q changed numeric identity between reads", first.UID)
		}
		if first.UID != second.UID {
			return kataAuthorityInconsistency("project ID %d changed UID from %q to %q between reads", first.ID, first.UID, second.UID)
		}
		if first.Name != second.Name {
			return kataAuthorityInconsistency("project %q changed name between reads", first.UID)
		}
		if !reflect.DeepEqual(first.Metadata, second.Metadata) {
			return kataAuthorityInconsistency("project %q changed metadata between reads", first.UID)
		}
		if first.Revision != second.Revision {
			return kataAuthorityInconsistency("project %q changed revision from %d to %d between reads", first.UID, first.Revision, second.Revision)
		}
		if first.OpenCount != second.OpenCount || first.ClosedCount != second.ClosedCount {
			return kataAuthorityInconsistency("project %q changed issue counts between reads", first.UID)
		}
		if !kataTimePointersEqual(first.LastEventAt, second.LastEventAt) {
			return kataAuthorityInconsistency("project %q changed last event time between reads", first.UID)
		}
	}
	return nil
}

func kataTimePointersEqual(first, second *time.Time) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.Equal(*second)
}

func validateKataAuthorityCounts(
	request kataAuthorityRequest,
	projects []kataProjectSummary,
	issues []kataTaskSummary,
) error {
	if request.Authority == "ready" {
		return nil
	}
	type issueCounts struct {
		open   int64
		closed int64
	}
	countsByProject := make(map[int64]issueCounts, len(projects))
	for _, issue := range issues {
		counts := countsByProject[issue.ProjectID]
		if issue.Status == "open" {
			counts.open++
		} else {
			counts.closed++
		}
		countsByProject[issue.ProjectID] = counts
	}
	for _, project := range projects {
		if request.Scope == "project" && project.UID != request.ProjectUID {
			continue
		}
		counts := countsByProject[project.ID]
		if (request.Authority == "open" || request.Authority == "all") && counts.open != project.OpenCount {
			return kataAuthorityInconsistency(
				"project %q open count changed from %d to %d between reads", project.UID, project.OpenCount, counts.open,
			)
		}
		if (request.Authority == "closed" || request.Authority == "all") && counts.closed != project.ClosedCount {
			return kataAuthorityInconsistency(
				"project %q closed count changed from %d to %d between reads", project.UID, project.ClosedCount, counts.closed,
			)
		}
	}
	return nil
}

func kataTaskSummariesFromGenerated(
	issues []katagenerated.IssueOut,
	projectsByID map[int64]kataProjectSummary,
	expectedProjectID *int64,
) ([]kataTaskSummary, error) {
	summaries := make([]kataTaskSummary, len(issues))
	for i, issue := range issues {
		var err error
		summaries[i], err = kataTaskSummaryFromGenerated(issue, projectsByID, expectedProjectID)
		if err != nil {
			return nil, err
		}
	}
	return summaries, nil
}

func kataTaskSummaryFromGenerated(
	issue katagenerated.IssueOut,
	projectsByID map[int64]kataProjectSummary,
	expectedProjectID *int64,
) (kataTaskSummary, error) {
	if expectedProjectID != nil && issue.ProjectID != *expectedProjectID {
		return kataTaskSummary{}, kataAuthorityInconsistency(
			"issue %q belongs to project ID %d, expected %d", issue.UID, issue.ProjectID, *expectedProjectID,
		)
	}
	project, ok := projectsByID[issue.ProjectID]
	if !ok {
		return kataTaskSummary{}, kataAuthorityInconsistency(
			"issue %q references unknown project ID %d", issue.UID, issue.ProjectID,
		)
	}
	if issue.ProjectUID != nil && *issue.ProjectUID != project.UID {
		return kataTaskSummary{}, kataAuthorityInconsistency(
			"issue %q has project UID %q, expected %q", issue.UID, *issue.ProjectUID, project.UID,
		)
	}
	projectUID := project.UID
	if projectUID == "" {
		return kataTaskSummary{}, kataSnapshotUpstreamError(
			"normalize issues",
			fmt.Errorf("issue %q has no project UID", issue.UID),
		)
	}
	return kataTaskSummary{
		ID:            issue.ID,
		UID:           issue.UID,
		ProjectID:     issue.ProjectID,
		ShortID:       issue.ShortID,
		QualifiedID:   issue.QualifiedID,
		Title:         issue.Title,
		Body:          issue.Body,
		Status:        issue.Status,
		ProjectUID:    projectUID,
		ProjectName:   project.Name,
		Metadata:      cloneKataMetadata(issue.Metadata),
		Revision:      issue.Revision,
		Owner:         cloneKataPointer(issue.Owner),
		Author:        issue.Author,
		Priority:      cloneKataPointer(issue.Priority),
		Labels:        append([]string(nil), issue.Labels...),
		Parent:        kataLinkPeerFromGenerated(issue.Parent),
		Blocks:        kataLinkPeersFromGenerated(issue.Blocks),
		BlockedBy:     kataLinkPeersFromGenerated(issue.BlockedBy),
		Related:       kataLinkPeersFromGenerated(issue.Related),
		ChildCounts:   kataChildCountsFromGenerated(issue.ChildCounts),
		RecurrenceID:  cloneKataPointer(issue.RecurrenceID),
		OccurrenceKey: cloneKataPointer(issue.OccurrenceKey),
		CreatedAt:     issue.CreatedAt.UTC(),
		UpdatedAt:     issue.UpdatedAt.UTC(),
		ClosedReason:  cloneKataPointer(issue.ClosedReason),
		ClosedAt:      kataTimePointerUTC(issue.ClosedAt),
		DeletedAt:     kataTimePointerUTC(issue.DeletedAt),
	}, nil
}

func kataTimePointerUTC(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func kataAuthorityInconsistency(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errKataAuthorityInconsistent, fmt.Sprintf(format, args...))
}

func validateKataProjectsResponse(response *katagenerated.ListProjectsResponse) error {
	seenIDs := make(map[int64]struct{}, len(response.Projects))
	seenUIDs := make(map[string]struct{}, len(response.Projects))
	for i, project := range response.Projects {
		if project.ID <= 0 || project.UID == "" || project.Name == "" || project.CreatedAt.IsZero() {
			return fmt.Errorf("project at index %d is missing required identity fields", i)
		}
		if project.Stats == nil {
			return fmt.Errorf("project %q is missing requested stats", project.UID)
		}
		if project.DeletedAt != nil {
			return fmt.Errorf("active project %q is deleted", project.UID)
		}
		if project.Stats.Open < 0 || project.Stats.Closed < 0 {
			return fmt.Errorf("project %q has negative issue counts", project.UID)
		}
		if _, exists := seenIDs[project.ID]; exists {
			return fmt.Errorf("duplicate project ID %d", project.ID)
		}
		if _, exists := seenUIDs[project.UID]; exists {
			return fmt.Errorf("duplicate project UID %q", project.UID)
		}
		seenIDs[project.ID] = struct{}{}
		seenUIDs[project.UID] = struct{}{}
	}
	return nil
}

func validateKataGlobalReadyResponse(response *katagenerated.ReadyIssuesGlobalResponse) error {
	issues := make([]katagenerated.IssueOut, len(response.Issues))
	for i, issue := range response.Issues {
		if issue.ProjectName == "" {
			return fmt.Errorf("issue at index %d is missing project name", i)
		}
		issues[i] = kataIssueOutFromGlobalReady(issue)
	}
	return validateKataIssues(issues)
}

func validateKataIssues(issues []katagenerated.IssueOut) error {
	seenIDs := make(map[int64]struct{}, len(issues))
	seenUIDs := make(map[string]struct{}, len(issues))
	for i, issue := range issues {
		if issue.ID <= 0 || issue.UID == "" || issue.ProjectID <= 0 || issue.ShortID == "" ||
			issue.QualifiedID == "" || issue.Title == "" || issue.Author == "" ||
			issue.CreatedAt.IsZero() || issue.UpdatedAt.IsZero() {
			return fmt.Errorf("issue at index %d is missing required identity fields", i)
		}
		if issue.Status != "open" && issue.Status != "closed" {
			return fmt.Errorf("issue %q has invalid status %q", issue.UID, issue.Status)
		}
		if issue.Metadata == nil {
			return fmt.Errorf("issue %q is missing metadata", issue.UID)
		}
		if issue.ProjectUID != nil && *issue.ProjectUID == "" {
			return fmt.Errorf("issue %q has an empty project UID", issue.UID)
		}
		if issue.DeletedAt != nil {
			return fmt.Errorf("authority issue %q is deleted", issue.UID)
		}
		if _, exists := seenUIDs[issue.UID]; exists {
			return fmt.Errorf("duplicate issue UID %q", issue.UID)
		}
		if _, exists := seenIDs[issue.ID]; exists {
			return fmt.Errorf("duplicate issue ID %d", issue.ID)
		}
		seenIDs[issue.ID] = struct{}{}
		seenUIDs[issue.UID] = struct{}{}
		if err := validateKataLinkPeer(issue.Parent); err != nil {
			return fmt.Errorf("issue %q parent: %w", issue.UID, err)
		}
		for relation, peers := range map[string][]katagenerated.LinkPeer{
			"blocks": issue.Blocks, "blocked_by": issue.BlockedBy, "related": issue.Related,
		} {
			for peerIndex := range peers {
				if err := validateKataLinkPeer(&peers[peerIndex]); err != nil {
					return fmt.Errorf("issue %q %s[%d]: %w", issue.UID, relation, peerIndex, err)
				}
			}
		}
		if issue.ChildCounts != nil && (issue.ChildCounts.Open < 0 || issue.ChildCounts.Total < issue.ChildCounts.Open) {
			return fmt.Errorf("issue %q has invalid child counts", issue.UID)
		}
	}
	return nil
}

func validateKataAuthorityStatus(issues []katagenerated.IssueOut, expected string) error {
	for _, issue := range issues {
		if issue.Status != expected {
			return fmt.Errorf("issue %q has status %q, expected %q", issue.UID, issue.Status, expected)
		}
	}
	return nil
}

func validateKataLinkPeer(peer *katagenerated.LinkPeer) error {
	if peer == nil {
		return nil
	}
	if peer.UID == "" || peer.ShortID == "" || peer.Project == "" || peer.QualifiedID == "" || peer.Status == "" {
		return fmt.Errorf("missing required identity fields")
	}
	return nil
}

func kataIssueOutFromGlobalReady(issue katagenerated.ReadyGlobalIssueOut) katagenerated.IssueOut {
	return katagenerated.IssueOut{
		ID: issue.ID, UID: issue.UID, ProjectID: issue.ProjectID, ProjectUID: issue.ProjectUID,
		ShortID: issue.ShortID, QualifiedID: issue.QualifiedID, Title: issue.Title, Body: issue.Body,
		Status: issue.Status, Metadata: issue.Metadata, Revision: issue.Revision, Owner: issue.Owner,
		Author: issue.Author, Priority: issue.Priority, Labels: issue.Labels, Parent: issue.Parent,
		Blocks: issue.Blocks, BlockedBy: issue.BlockedBy, Related: issue.Related, ChildCounts: issue.ChildCounts,
		RecurrenceID: issue.RecurrenceID, OccurrenceKey: issue.OccurrenceKey, CreatedAt: issue.CreatedAt,
		UpdatedAt: issue.UpdatedAt, ClosedReason: issue.ClosedReason, ClosedAt: issue.ClosedAt, DeletedAt: issue.DeletedAt,
	}
}

func kataLinkPeerFromGenerated(peer *katagenerated.LinkPeer) *kataLinkPeer {
	if peer == nil {
		return nil
	}
	return &kataLinkPeer{UID: peer.UID, ShortID: peer.ShortID}
}

func kataLinkPeersFromGenerated(peers []katagenerated.LinkPeer) []kataLinkPeer {
	if peers == nil {
		return nil
	}
	result := make([]kataLinkPeer, len(peers))
	for i, peer := range peers {
		result[i] = kataLinkPeer{UID: peer.UID, ShortID: peer.ShortID}
	}
	return result
}

func kataChildCountsFromGenerated(counts *katagenerated.ChildCounts) *kataChildCounts {
	if counts == nil {
		return nil
	}
	return &kataChildCounts{Open: counts.Open, Total: counts.Total}
}

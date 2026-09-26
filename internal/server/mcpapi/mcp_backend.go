package mcpapi

import (
	"encoding/json/v2"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

// resolvedMCPRepository binds a stable-identity-validated repository to either
// its spoke-local route generation or the hub authority that resolved
// it.
type ResolvedMCPRepository struct {
	Repo  *db.Repo
	Fence db.RepositoryRouteFence
	Hub   bool
}

func McpRepositoryIdentityChangedError() error {
	return &mcpserver.Error{
		Kind: "not_found", Code: string(httpapi.CodeRepoNotFound),
		Message: "repository identity no longer matches this route",
	}
}

func ValidateMCPRepositoryIdentity(identity mcpserver.RepositoryIdentity) error {
	if strings.TrimSpace(identity.Provider) == "" {
		return &mcpserver.Error{
			Kind: "invalid_request", Code: string(httpapi.CodeValidationError),
			Message: "provider is required",
		}
	}
	if strings.TrimSpace(identity.PlatformRepoID) == "" {
		return &mcpserver.Error{
			Kind: "invalid_request", Code: string(httpapi.CodeValidationError),
			Message: "platform_repo_id is required",
		}
	}
	return nil
}

func McpRepositoryStableIdentityMatches(
	repo db.Repo, identity mcpserver.RepositoryIdentity,
) bool {
	actual := providerplane.RepositoryIdentity{
		Provider: repo.Platform, PlatformHost: repo.PlatformHost,
		PlatformRepoID: repo.PlatformRepoID,
	}.Canonical()
	expected := providerplane.RepositoryIdentity{
		Provider: identity.Provider, PlatformHost: identity.PlatformHost,
		PlatformRepoID: identity.PlatformRepoID,
	}.Canonical()
	return actual.Valid() && actual == expected
}

func ItemRepositoryIdentity(item mcpserver.ItemIdentity) mcpserver.RepositoryIdentity {
	return mcpserver.RepositoryIdentity{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		PlatformRepoID: item.PlatformRepoID,
		Owner:          item.Owner, Name: item.Name,
	}
}

func McpBackendError(err error) error {
	if err == nil {
		return nil
	}
	if existing, ok := errors.AsType[*mcpserver.Error](err); ok {
		return existing
	}
	problem, ok := errors.AsType[*httpapi.ProblemError](err)
	if !ok {
		return &mcpserver.Error{Kind: "internal_error", Message: err.Error()}
	}
	kind := "internal_error"
	retryable := false
	switch problem.Status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge:
		kind = "invalid_request"
	case http.StatusUnauthorized:
		kind = "unauthorized"
	case http.StatusForbidden:
		kind = "forbidden"
	case http.StatusNotFound:
		kind = "not_found"
	case http.StatusConflict:
		kind = "conflict"
	case http.StatusTooManyRequests:
		kind = "rate_limited"
		retryable = true
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		kind = "unavailable"
		retryable = true
	}
	ambiguous := problem.Code == httpapi.CodeMutationOutcomeUnknown
	if ambiguous {
		retryable = false
	}
	return &mcpserver.Error{
		Kind: kind, Code: string(problem.Code), Message: problem.Error(),
		Retryable: retryable, Ambiguous: ambiguous, Details: problem.Details,
	}
}

func McpBackendMutationError(err error) error {
	converted := McpBackendError(err)
	backendErr, ok := errors.AsType[*mcpserver.Error](converted)
	if !ok || backendErr.Kind != "internal_error" {
		return converted
	}
	annotated := *backendErr
	annotated.Ambiguous = true
	annotated.Retryable = false
	annotated.Details = CloneMCPErrorDetails(backendErr.Details)
	return &annotated
}

func NormalizeMCPWorkflowStatus(status string) db.KanbanStatus {
	switch db.KanbanStatus(status) {
	case db.KanbanStatusNew, db.KanbanStatusReviewing,
		db.KanbanStatusWaiting, db.KanbanStatusAwaitingMerge:
		return db.KanbanStatus(status)
	default:
		return db.KanbanStatusNew
	}
}

func RepositoryIdentityFromResponse(repo httpapi.RepoRefResponse) mcpserver.RepositoryIdentity {
	return mcpserver.RepositoryIdentity{
		Provider: repo.Provider, PlatformHost: repo.PlatformHost,
		PlatformRepoID: repo.PlatformRepoID,
		RepoPath:       repo.RepoPath, Owner: repo.Owner, Name: repo.Name,
	}
}

func McpRepositoryFilter(repo mcpserver.RepositoryIdentity) string {
	if repo.Provider == "" {
		return ""
	}
	path := strings.Trim(repo.RepoPath, "/")
	if path == "" {
		path = strings.Trim(repo.Owner, "/") + "/" + strings.Trim(repo.Name, "/")
	}
	return repo.Provider + "|" + repo.PlatformHost + "/" + path
}

func McpRepoFilters(repo mcpserver.RepositoryIdentity) []db.RepoFilter {
	if repo.Provider == "" {
		return nil
	}
	return []db.RepoFilter{{
		Platform: repo.Provider, PlatformHost: repo.PlatformHost,
		PlatformRepoID: repo.PlatformRepoID,
		RepoPath:       repo.RepoPath, RepoOwner: repo.Owner, RepoName: repo.Name,
	}}
}

func McpWorkspaceRef(ref *workspaceapi.WorkspaceRef) *mcpserver.WorkspaceRef {
	if ref == nil {
		return nil
	}
	return &mcpserver.WorkspaceRef{ID: ref.ID, Status: ref.Status}
}

func McpPull(row pullapi.MergeRequestResponse) mcpserver.Pull {
	var checks []db.CICheck
	if strings.TrimSpace(row.CIChecksJSON) != "" {
		if err := json.Unmarshal([]byte(row.CIChecksJSON), &checks); err != nil {
			slog.Warn("decode cached pull checks failed", "pull_number", row.Number, "err", err)
			checks = nil
		}
	}
	pull := mcpserver.Pull{
		Labels:         McpLabelNames(row.Labels),
		MergeableState: row.MergeableState, ReviewDecision: row.ReviewDecision,
		CIStatus: row.CIStatus, HeadSHA: row.PlatformHeadSHA, Checks: McpChecks(checks),
		Number: row.Number, Title: row.Title, State: string(row.State),
		Author: row.Author, URL: row.URL, IsDraft: row.IsDraft, Body: row.Body,
		WorkflowStatus: string(row.KanbanStatus), LastActivityAt: row.LastActivityAt,
		Repository:   RepositoryIdentityFromResponse(row.Repo),
		Workspace:    McpWorkspaceRef(row.Workspace),
		DetailLoaded: row.DetailLoaded, DetailFetchedAt: row.DetailFetchedAt,
	}
	if row.Stack != nil {
		pull.Stack = &mcpserver.Stack{Position: row.Stack.Position, Size: row.Stack.Size}
	}
	return pull
}

func McpLabelNames(labels []db.Label) []string {
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		names = append(names, label.Name)
	}
	return names
}

func McpChecks(checks []db.CICheck) []mcpserver.Check {
	out := make([]mcpserver.Check, 0, len(checks))
	for _, check := range checks {
		out = append(out, mcpserver.Check{
			Name: check.Name, Status: check.Status, Conclusion: check.Conclusion,
			URL: check.URL, App: check.App, DurationSeconds: check.DurationSeconds,
		})
	}
	return out
}

func McpIssue(row issueapi.IssueResponse) mcpserver.Issue {
	return mcpserver.Issue{
		Number: row.Number, Title: row.Title, State: row.State,
		Author: row.Author, URL: row.URL, Body: row.Body,
		WorkflowStatus: string(row.WorkflowStatus), LastActivityAt: row.LastActivityAt,
		Repository:   RepositoryIdentityFromResponse(row.Repo),
		Workspace:    McpWorkspaceRef(row.Workspace),
		DetailLoaded: row.DetailLoaded, DetailFetchedAt: row.DetailFetchedAt,
	}
}

func PullServiceIdentity(item mcpserver.ItemIdentity) pullapi.ItemIdentity {
	return pullapi.ItemIdentity{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		Owner: item.Owner, Name: item.Name, Number: item.Number,
	}
}

func IssueServiceIdentity(item mcpserver.ItemIdentity) issueapi.ItemIdentity {
	return issueapi.ItemIdentity{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		Owner: item.Owner, Name: item.Name, Number: item.Number,
	}
}

func McpDiffFiles(files []gitclone.DiffFile) []mcpserver.DiffFile {
	out := make([]mcpserver.DiffFile, 0, len(files))
	for _, file := range files {
		out = append(out, mcpserver.DiffFile{
			Path: file.Path, OldPath: file.OldPath, Status: file.Status,
			IsBinary: file.IsBinary, IsGenerated: file.IsGenerated,
			Additions: file.Additions, Deletions: file.Deletions, Patch: file.Patch,
		})
	}
	return out
}

func McpStack(stack pullapi.StackContext) mcpserver.Stack {
	out := mcpserver.Stack{
		Position: stack.Position, Size: stack.Size, Health: stack.Health,
		Members: make([]mcpserver.StackMember, 0, len(stack.Members)),
	}
	for _, member := range stack.Members {
		out.Members = append(out.Members, mcpserver.StackMember{
			Number: member.Number, Title: member.Title, State: member.State,
			Position: member.Position, IsDraft: member.IsDraft,
		})
	}
	return out
}

func McpWorkspace(result workspaceapi.WorkspaceResult) mcpserver.Workspace {
	workspace := result.Workspace
	return mcpserver.Workspace{
		ID: workspace.ID, Status: workspace.Status, Created: workspace.Created,
		GitHeadRef: workspace.GitHeadRef, ErrorMessage: workspace.ErrorMessage,
	}
}

func CloneMCPErrorDetails(details map[string]any) map[string]any {
	cloned := maps.Clone(details)
	if cloned == nil {
		cloned = make(map[string]any)
	}
	return cloned
}

func McpInitialMessage(result workspaceapi.InitialMessageResult) *mcpserver.InitialMessageStatus {
	return &mcpserver.InitialMessageStatus{
		State: result.State, MessageBytes: result.MessageBytes,
		DeliveredAt: result.DeliveredAt,
	}
}

# Huma OpenAPI Operation Metadata Coverage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every non-Hidden operation in middleman's Huma-served OpenAPI document carry an explicit Summary, exactly one known Tag, and a stable, unique OperationID, and guard the invariant with live-OpenAPI and source-level tests.

**Architecture:** Add a contract test in `internal/server/route_metadata_test.go` that walks the live `*huma.OpenAPI` document produced by `server.NewOpenAPI()`. Introduce a small `documentOperation` registration helper and use it (or inline `huma.Operation` fields) to attach Summary, Tags, and OperationID to every existing non-Hidden route. After the source backfill, regenerate the four checked-in API artifacts via `make api-generate` so the metadata flows into the Go client and TypeScript schema.

**Tech Stack:** Go (huma/v2, testify), oapi-codegen, openapi-typescript, Bun.

**Pre-commit hook caveat:** The repository's `prek.toml` configures a `make test-short` pre-commit hook that runs every Go test on commit. Across Tasks 2-3 the contract test file from Task 1 lives uncommitted on disk; `go test ./...` and any `make test-short` invocation will pick it up and fail until Task 4 lands. The pre-commit hook is not installed in this worktree, so commits in Tasks 2-3 succeed in practice. If the hook is later installed via `make install-hooks`, run the registerAPI/provider-repo backfills with the contract-test file temporarily stashed out of the worktree (move to `/tmp/route_metadata_test.go.draft`, then restore for Task 4). Do not pass `--no-verify`.

---

### Task 1: Sketch the Contract Test as a Working File

This task drafts the contract test so subsequent backfill tasks have a concrete acceptance criterion to drive off. The test is not committed yet: a single commit at this point would either (a) break `go test ./...` and refuse to land in any environment with a `make test-short` pre-commit hook, or (b) require an awkward `t.Skip` that adds churn to a later removal commit. Instead, the working file lives uncommitted alongside the backfill tasks and lands in Task 4 once the live OpenAPI document already satisfies it.

**Files:**
- Create (uncommitted for now): `internal/server/route_metadata_test.go`

- [ ] **Step 1: Write the contract test and negative-case smoke test**

Create `internal/server/route_metadata_test.go`:

```go
package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectMetadataFailures walks an OpenAPI document and returns one entry per
// missing-or-auto-generated metadata field on every non-nil operation. The
// returned slice is sorted so failure output is stable across test runs.
func collectMetadataFailures(openAPI *huma.OpenAPI) []string {
	var failures []string
	seen := map[string]string{}

	paths := make([]string, 0, len(openAPI.Paths))
	for p := range openAPI.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		item := openAPI.Paths[path]
		if item == nil {
			continue
		}
		for _, opRef := range []struct {
			method string
			op     *huma.Operation
		}{
			{http.MethodGet, item.Get},
			{http.MethodPut, item.Put},
			{http.MethodPost, item.Post},
			{http.MethodDelete, item.Delete},
			{http.MethodOptions, item.Options},
			{http.MethodHead, item.Head},
			{http.MethodPatch, item.Patch},
			{http.MethodTrace, item.Trace},
		} {
			op := opRef.op
			if op == nil {
				continue
			}
			label := fmt.Sprintf("%s %s", opRef.method, path)

			if strings.TrimSpace(op.Summary) == "" {
				failures = append(failures, label+": missing Summary")
			}
			if strings.TrimSpace(op.OperationID) == "" {
				failures = append(failures, label+": missing OperationID")
			}
			if len(op.Tags) < 1 {
				failures = append(failures, label+": missing Tags")
			} else {
				for _, tag := range op.Tags {
					if strings.TrimSpace(tag) == "" {
						failures = append(failures, label+": empty Tag")
					}
				}
			}
			if !usesKnownSingleTag(op.Tags) {
				failures = append(failures,
					label+": expected exactly one tag from the API tag taxonomy")
			}
			if op.OperationID != "" {
				if prior, ok := seen[op.OperationID]; ok {
					failures = append(failures,
						label+": duplicate OperationID with "+prior)
				} else {
					seen[op.OperationID] = label
				}
			}
		}
	}
	return failures
}

// TestHumaContractMetadata asserts that every non-Hidden operation in the
// live OpenAPI document carries an explicit Summary, exactly one known Tag,
// and a unique OperationID.
func TestHumaContractMetadata(t *testing.T) {
	require := require.New(t)
	openAPI := NewOpenAPI()
	require.NotNil(openAPI)
	require.NotEmpty(openAPI.Paths, "OpenAPI document should expose paths")

	failures := collectMetadataFailures(openAPI)
	assert.Empty(t, failures, strings.Join(failures, "\n"))
}

// TestRouteMetadataWalkerCatchesUnannotatedRoute is a teeth-test: it builds
// a tiny in-process huma.API with one convenience-helper route that has no
// metadata, runs collectMetadataFailures, and asserts the walker reports at
// least one failure. Guards against the contract test becoming a no-op if
// Huma changes how it marks auto-generated values.
func TestRouteMetadataWalkerCatchesUnannotatedRoute(t *testing.T) {
	require := require.New(t)

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))

	type emptyInput struct{}
	type emptyOutput struct{}
	huma.Get(api, "/unannotated", func(
		_ context.Context, _ *emptyInput,
	) (*emptyOutput, error) {
		return &emptyOutput{}, nil
	})

	failures := collectMetadataFailures(api.OpenAPI())
	require.NotEmpty(failures,
		"walker must flag unannotated routes; got no failures")
}
```

- [ ] **Step 2: Run the contract test to confirm it currently fails (sanity check)**

Run:

```bash
nix run 'nixpkgs#go' -- test ./internal/server -run TestHumaContractMetadata -shuffle=on
```

Expected: FAIL. Output contains entries like `GET /pulls: missing Summary`, `POST /pulls/{provider}/{owner}/{name}/{number}/approve: missing Tags`, `GET /version: missing Tags`.

- [ ] **Step 3: Run the teeth-test in isolation and verify it passes**

```bash
nix run 'nixpkgs#go' -- test ./internal/server -run TestRouteMetadataWalkerCatchesUnannotatedRoute -shuffle=on
```

Expected: PASS. The teeth test does not depend on the production registrations.

- [ ] **Step 4: Do not commit yet**

Leave the test file uncommitted. It will be committed in Task 6 once the backfill makes it pass. This step has no shell command; it's a reminder to not stage the file.

---

### Task 2: Backfill Top-Level Routes In registerAPI

This task adds the `documentOperation` helper and uses it on every route in `registerAPI` (excluding `registerProviderRepoAPI` and `registerSettingsAPI`, which are separate tasks). The helper and its first use go in the same commit so `golangci-lint`'s `unused` check does not reject a stand-alone helper definition.

**Files:**
- Modify: `internal/server/huma_routes.go` (lines 413-575 approximately)

- [ ] **Step 1: Add the documentOperation helper next to apiConfig**

In `internal/server/huma_routes.go`, immediately after the `apiConfig` function (around line 422), add:

```go
// documentOperation returns an operationHandler that sets Summary, Tags, and
// OperationID on the resulting *huma.Operation. Use it with the huma.Get/
// Post/Put/Patch/Delete convenience helpers so routes registered through
// shorthand still carry the metadata that the OpenAPI contract test enforces.
func documentOperation(
	operationID, summary string, tags ...string,
) func(*huma.Operation) {
	return func(o *huma.Operation) {
		o.OperationID = operationID
		o.Summary = summary
		o.Tags = tags
	}
}
```

- [ ] **Step 2: Backfill the version, activity, pulls, issues, and starred routes**

Replace the existing registrations in `registerAPI` with their metadata-bearing equivalents. For the `get-version` block, change:

```go
	huma.Register(api, huma.Operation{
		OperationID: "get-version",
		Method:      http.MethodGet,
		Path:        "/version",
	}, s.getVersion)

	huma.Get(api, "/activity", s.listActivity)
	huma.Get(api, "/pulls", s.listPulls)
	huma.Get(api, "/issues", s.listIssues)
```

to:

```go
	huma.Register(api, huma.Operation{
		OperationID: "get-version",
		Method:      http.MethodGet,
		Path:        "/version",
		Summary:     "Get server version",
		Tags:        []string{"System"},
	}, s.getVersion)

	huma.Get(api, "/activity", s.listActivity,
		documentOperation("list-activity", "List activity", "Activity"))
	huma.Get(api, "/pulls", s.listPulls,
		documentOperation("list-pulls", "List pull requests", "Pull Requests"))
	huma.Get(api, "/issues", s.listIssues,
		documentOperation("list-issues", "List issues", "Issues"))
```

- [ ] **Step 3: Backfill repo summaries and starred routes**

Replace:

```go
	huma.Register(api, huma.Operation{
		OperationID:   "list-repo-summaries",
		Method:        http.MethodGet,
		Path:          "/repos/summary",
		DefaultStatus: http.StatusOK,
	}, s.listRepoSummaries)
	huma.Register(api, huma.Operation{
		OperationID:   "set-starred",
		Method:        http.MethodPut,
		Path:          "/starred",
		DefaultStatus: http.StatusOK,
	}, s.setStarred)
	huma.Register(api, huma.Operation{
		OperationID:   "unset-starred",
		Method:        http.MethodDelete,
		Path:          "/starred",
		DefaultStatus: http.StatusOK,
	}, s.unsetStarred)
```

with:

```go
	huma.Register(api, huma.Operation{
		OperationID:   "list-repo-summaries",
		Method:        http.MethodGet,
		Path:          "/repos/summary",
		DefaultStatus: http.StatusOK,
		Summary:       "List repository summaries",
		Tags:          []string{"Repositories"},
	}, s.listRepoSummaries)
	huma.Register(api, huma.Operation{
		OperationID:   "set-starred",
		Method:        http.MethodPut,
		Path:          "/starred",
		DefaultStatus: http.StatusOK,
		Summary:       "Star repository",
		Tags:          []string{"Settings"},
	}, s.setStarred)
	huma.Register(api, huma.Operation{
		OperationID:   "unset-starred",
		Method:        http.MethodDelete,
		Path:          "/starred",
		DefaultStatus: http.StatusOK,
		Summary:       "Unstar repository",
		Tags:          []string{"Settings"},
	}, s.unsetStarred)
```

- [ ] **Step 4: Backfill repos, preview-repos, bulk-add-repos**

Replace:

```go
	huma.Get(api, "/repos", s.listRepos)
	huma.Register(api, huma.Operation{
		OperationID:   "preview-repos",
		Method:        http.MethodPost,
		Path:          "/repos/preview",
		DefaultStatus: http.StatusOK,
	}, s.previewRepos)
	huma.Register(api, huma.Operation{
		OperationID:   "bulk-add-repos",
		Method:        http.MethodPost,
		Path:          "/repos/bulk",
		DefaultStatus: http.StatusCreated,
	}, s.bulkAddRepos)
```

with:

```go
	huma.Get(api, "/repos", s.listRepos,
		documentOperation("list-repos", "List repositories", "Repositories"))
	huma.Register(api, huma.Operation{
		OperationID:   "preview-repos",
		Method:        http.MethodPost,
		Path:          "/repos/preview",
		DefaultStatus: http.StatusOK,
		Summary:       "Preview repositories",
		Tags:          []string{"Repositories"},
	}, s.previewRepos)
	huma.Register(api, huma.Operation{
		OperationID:   "bulk-add-repos",
		Method:        http.MethodPost,
		Path:          "/repos/bulk",
		DefaultStatus: http.StatusCreated,
		Summary:       "Bulk add repositories",
		Tags:          []string{"Repositories"},
	}, s.bulkAddRepos)
```

- [ ] **Step 5: Backfill sync/event/status routes**

Replace:

```go
	huma.Register(api, huma.Operation{
		OperationID:   "trigger-sync",
		Method:        http.MethodPost,
		Path:          "/sync",
		DefaultStatus: http.StatusAccepted,
	}, s.triggerSync)
	huma.Register(api, huma.Operation{
		OperationID: "stream-events",
		Method:      http.MethodGet,
		Path:        "/events",
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Server-sent event stream",
				Content: map[string]*huma.MediaType{
					"text/event-stream": {},
				},
			},
		},
	}, s.streamEvents)
	huma.Get(api, "/sync/status", s.syncStatus)
	huma.Get(api, "/rate-limits", s.getRateLimits)
	huma.Register(api, huma.Operation{
		OperationID: "get-roborev-status",
		Method:      http.MethodGet,
		Path:        "/roborev/status",
	}, s.getRoborevStatus)

	huma.Get(api, "/stacks", s.listStacks)
```

with:

```go
	huma.Register(api, huma.Operation{
		OperationID:   "trigger-sync",
		Method:        http.MethodPost,
		Path:          "/sync",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Trigger sync",
		Tags:          []string{"Sync"},
	}, s.triggerSync)
	huma.Register(api, huma.Operation{
		OperationID: "stream-events",
		Method:      http.MethodGet,
		Path:        "/events",
		Summary:     "Stream server events",
		Tags:        []string{"System"},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Server-sent event stream",
				Content: map[string]*huma.MediaType{
					"text/event-stream": {},
				},
			},
		},
	}, s.streamEvents)
	huma.Get(api, "/sync/status", s.syncStatus,
		documentOperation("get-sync-status", "Get sync status", "Sync"))
	huma.Get(api, "/rate-limits", s.getRateLimits,
		documentOperation("get-rate-limits", "Get rate limits", "Sync"))
	huma.Register(api, huma.Operation{
		OperationID: "get-roborev-status",
		Method:      http.MethodGet,
		Path:        "/roborev/status",
		Summary:     "Get roborev status",
		Tags:        []string{"Roborev"},
	}, s.getRoborevStatus)

	huma.Get(api, "/stacks", s.listStacks,
		documentOperation("list-stacks", "List stacks", "Stacks"))
```

- [ ] **Step 6: Backfill workspace routes**

Replace the entire workspace block (from `create-workspace` through `delete-workspace`):

```go
	huma.Register(api, huma.Operation{
		OperationID:   "create-workspace",
		Method:        http.MethodPost,
		Path:          "/workspaces",
		DefaultStatus: http.StatusAccepted,
	}, s.createWorkspace)
	huma.Get(api, "/workspaces", s.listWorkspaces)
	huma.Get(api, "/workspaces/{id}", s.getWorkspace)
	huma.Get(api, "/workspaces/{id}/commits", s.getWorkspaceCommits)
	huma.Get(api, "/workspaces/{id}/diff", s.getWorkspaceDiff)
	huma.Get(api, "/workspaces/{id}/files", s.getWorkspaceFiles)
	huma.Register(api, huma.Operation{
		OperationID:   "retry-workspace",
		Method:        http.MethodPost,
		Path:          "/workspaces/{id}/retry",
		DefaultStatus: http.StatusAccepted,
	}, s.retryWorkspace)
	huma.Register(api, huma.Operation{
		OperationID: "get-workspace-runtime",
		Method:      http.MethodGet,
		Path:        "/workspaces/{id}/runtime",
	}, s.getWorkspaceRuntime)
	huma.Register(api, huma.Operation{
		OperationID: "launch-workspace-runtime-session",
		Method:      http.MethodPost,
		Path:        "/workspaces/{id}/runtime/sessions",
	}, s.launchWorkspaceRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID:   "stop-workspace-runtime-session",
		Method:        http.MethodDelete,
		Path:          "/workspaces/{id}/runtime/sessions/{session_key}",
		DefaultStatus: http.StatusNoContent,
	}, s.stopWorkspaceRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID: "ensure-workspace-runtime-shell",
		Method:      http.MethodPost,
		Path:        "/workspaces/{id}/runtime/shell",
	}, s.ensureWorkspaceRuntimeShell)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-workspace",
		Method:        http.MethodDelete,
		Path:          "/workspaces/{id}",
		DefaultStatus: http.StatusNoContent,
	}, s.deleteWorkspace)
```

with:

```go
	huma.Register(api, huma.Operation{
		OperationID:   "create-workspace",
		Method:        http.MethodPost,
		Path:          "/workspaces",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Create workspace",
		Tags:          []string{"Workspaces"},
	}, s.createWorkspace)
	huma.Get(api, "/workspaces", s.listWorkspaces,
		documentOperation("list-workspaces", "List workspaces", "Workspaces"))
	huma.Get(api, "/workspaces/{id}", s.getWorkspace,
		documentOperation("get-workspace", "Get workspace", "Workspaces"))
	huma.Get(api, "/workspaces/{id}/commits", s.getWorkspaceCommits,
		documentOperation("get-workspace-commits", "Get workspace commits", "Workspaces"))
	huma.Get(api, "/workspaces/{id}/diff", s.getWorkspaceDiff,
		documentOperation("get-workspace-diff", "Get workspace diff", "Workspaces"))
	huma.Get(api, "/workspaces/{id}/files", s.getWorkspaceFiles,
		documentOperation("get-workspace-files", "Get workspace files", "Workspaces"))
	huma.Register(api, huma.Operation{
		OperationID:   "retry-workspace",
		Method:        http.MethodPost,
		Path:          "/workspaces/{id}/retry",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Retry workspace",
		Tags:          []string{"Workspaces"},
	}, s.retryWorkspace)
	huma.Register(api, huma.Operation{
		OperationID: "get-workspace-runtime",
		Method:      http.MethodGet,
		Path:        "/workspaces/{id}/runtime",
		Summary:     "Get workspace runtime",
		Tags:        []string{"Workspaces"},
	}, s.getWorkspaceRuntime)
	huma.Register(api, huma.Operation{
		OperationID: "launch-workspace-runtime-session",
		Method:      http.MethodPost,
		Path:        "/workspaces/{id}/runtime/sessions",
		Summary:     "Launch workspace runtime session",
		Tags:        []string{"Workspaces"},
	}, s.launchWorkspaceRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID:   "stop-workspace-runtime-session",
		Method:        http.MethodDelete,
		Path:          "/workspaces/{id}/runtime/sessions/{session_key}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Stop workspace runtime session",
		Tags:          []string{"Workspaces"},
	}, s.stopWorkspaceRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID: "ensure-workspace-runtime-shell",
		Method:      http.MethodPost,
		Path:        "/workspaces/{id}/runtime/shell",
		Summary:     "Ensure workspace runtime shell",
		Tags:        []string{"Workspaces"},
	}, s.ensureWorkspaceRuntimeShell)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-workspace",
		Method:        http.MethodDelete,
		Path:          "/workspaces/{id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete workspace",
		Tags:          []string{"Workspaces"},
	}, s.deleteWorkspace)
```

- [ ] **Step 7: Backfill projects, worktrees, and launch targets**

Replace:

```go
	huma.Register(api, huma.Operation{
		OperationID:   "register-project",
		Method:        http.MethodPost,
		Path:          "/projects",
		DefaultStatus: http.StatusCreated,
	}, s.registerProject)
	huma.Register(api, huma.Operation{
		OperationID: "list-projects",
		Method:      http.MethodGet,
		Path:        "/projects",
	}, s.listProjects)
	huma.Register(api, huma.Operation{
		OperationID: "get-project",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}",
	}, s.getProject)
	huma.Register(api, huma.Operation{
		OperationID:   "register-worktree",
		Method:        http.MethodPost,
		Path:          "/projects/{project_id}/worktrees",
		DefaultStatus: http.StatusCreated,
	}, s.registerWorktree)
	huma.Register(api, huma.Operation{
		OperationID: "list-worktrees",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/worktrees",
	}, s.listWorktrees)
	huma.Register(api, huma.Operation{
		OperationID: "list-launch-targets",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/launch-targets",
	}, s.listLaunchTargets)
```

with:

```go
	huma.Register(api, huma.Operation{
		OperationID:   "register-project",
		Method:        http.MethodPost,
		Path:          "/projects",
		DefaultStatus: http.StatusCreated,
		Summary:       "Register project",
		Tags:          []string{"Projects"},
	}, s.registerProject)
	huma.Register(api, huma.Operation{
		OperationID: "list-projects",
		Method:      http.MethodGet,
		Path:        "/projects",
		Summary:     "List projects",
		Tags:        []string{"Projects"},
	}, s.listProjects)
	huma.Register(api, huma.Operation{
		OperationID: "get-project",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}",
		Summary:     "Get project",
		Tags:        []string{"Projects"},
	}, s.getProject)
	huma.Register(api, huma.Operation{
		OperationID:   "register-worktree",
		Method:        http.MethodPost,
		Path:          "/projects/{project_id}/worktrees",
		DefaultStatus: http.StatusCreated,
		Summary:       "Register worktree",
		Tags:          []string{"Projects"},
	}, s.registerWorktree)
	huma.Register(api, huma.Operation{
		OperationID: "list-worktrees",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/worktrees",
		Summary:     "List worktrees",
		Tags:        []string{"Projects"},
	}, s.listWorktrees)
	huma.Register(api, huma.Operation{
		OperationID: "list-launch-targets",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/launch-targets",
		Summary:     "List launch targets",
		Tags:        []string{"Projects"},
	}, s.listLaunchTargets)
```

- [ ] **Step 8: Run contract test and verify remaining failures are confined to provider-repo routes and settings routes**

Run:

```bash
nix run 'nixpkgs#go' -- test ./internal/server -run TestHumaContractMetadata -shuffle=on
```

Expected: FAIL, but every remaining failure line should reference a path matching `^/(host/|repo/|repos$|pulls/|issues/|settings)`. No failures should reference `/pulls`, `/issues`, `/activity`, `/version`, `/repos`, `/workspaces`, `/projects`, `/sync`, `/stacks`, `/events`, `/rate-limits`, `/roborev/status`, or `/starred`.

- [ ] **Step 9: Commit the registerAPI backfill (helper + every change in Task 2)**

```bash
git add internal/server/huma_routes.go
git commit -m "$(cat <<'EOF'
feat(server): document top-level Huma routes in registerAPI

Backfills Summary, Tags, and (where missing) OperationID on every
non-provider-repo route registered through registerAPI: list/get
operations for version, activity, pulls, issues, repos, workspaces,
projects, stacks, plus trigger-sync, sync status, rate limits, the
SSE /events stream, and starred/unstarred mutations.

Picks one tag per route from the taxonomy in the spec so the docs UI
groups related operations together: Pull Requests, Issues,
Repositories, Settings, Sync, Activity, Stacks, Workspaces, Projects,
Roborev, System.

Adds a documentOperation(operationID, summary string, tags ...string)
helper next to apiConfig so callers can fill the three required pieces
of OpenAPI metadata in one expression alongside huma.Get/Post/Put/
Patch/Delete shorthand calls.
EOF
)"
```

---

### Task 3: Backfill Provider-Repo Routes In registerProviderRepoAPI

This task covers everything registered inside `registerProviderRepoAPI` (roughly lines 577-661 in `huma_routes.go`). The inline `huma.Register(api, huma.Operation{...})` calls keep their structure; they just gain `Summary` and `Tags` fields. The convenience-helper calls (`huma.Get`/`huma.Post`) gain a `documentOperation(...)` argument. Several existing OperationIDs already conform to the kebab-case `verb-resource[-qualifier]` convention and stay as-is; the rest follow the conventions in the spec.

**Files:**
- Modify: `internal/server/huma_routes.go` (lines 577-661 approximately)

- [ ] **Step 1: Backfill PR detail, kanban-state, content, comments, and labels**

Replace:

```go
	huma.Get(api, pullPath, s.getPull)
	huma.Get(api, hostPullPath, s.getPullOnHost)
	huma.Get(api, pullPath+"/import-metadata", s.getMRImportMetadata)
	huma.Get(api, hostPullPath+"/import-metadata", s.getMRImportMetadataOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-kanban-state", Method: http.MethodPut, Path: pullPath + "/state", DefaultStatus: http.StatusOK}, s.setKanbanState)
	huma.Register(api, huma.Operation{OperationID: "set-kanban-state-on-host", Method: http.MethodPut, Path: hostPullPath + "/state", DefaultStatus: http.StatusOK}, s.setKanbanStateOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-content", Method: http.MethodPatch, Path: pullPath, DefaultStatus: http.StatusOK}, s.editPRContent)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-content-on-host", Method: http.MethodPatch, Path: hostPullPath, DefaultStatus: http.StatusOK}, s.editPRContentOnHost)
	huma.Register(api, huma.Operation{OperationID: "post-pr-comment", Method: http.MethodPost, Path: pullPath + "/comments", DefaultStatus: http.StatusCreated}, s.postComment)
	huma.Register(api, huma.Operation{OperationID: "post-pr-comment-on-host", Method: http.MethodPost, Path: hostPullPath + "/comments", DefaultStatus: http.StatusCreated}, s.postCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-comment", Method: http.MethodPatch, Path: pullPath + "/comments/{comment_id}", DefaultStatus: http.StatusOK}, s.editComment)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-comment-on-host", Method: http.MethodPatch, Path: hostPullPath + "/comments/{comment_id}", DefaultStatus: http.StatusOK}, s.editCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-pr-labels", Method: http.MethodPut, Path: pullPath + "/labels", DefaultStatus: http.StatusOK}, s.setPullLabels)
	huma.Register(api, huma.Operation{OperationID: "set-pr-labels-on-host", Method: http.MethodPut, Path: hostPullPath + "/labels", DefaultStatus: http.StatusOK}, s.setPullLabelsOnHost)
```

with:

```go
	huma.Get(api, pullPath, s.getPull,
		documentOperation("get-pull", "Get pull request", "Pull Requests"))
	huma.Get(api, hostPullPath, s.getPullOnHost,
		documentOperation("get-pull-on-host", "Get pull request", "Pull Requests"))
	huma.Get(api, pullPath+"/import-metadata", s.getMRImportMetadata,
		documentOperation("get-pull-import-metadata", "Get pull request import metadata", "Pull Requests"))
	huma.Get(api, hostPullPath+"/import-metadata", s.getMRImportMetadataOnHost,
		documentOperation("get-pull-import-metadata-on-host", "Get pull request import metadata", "Pull Requests"))
	huma.Register(api, huma.Operation{OperationID: "set-kanban-state", Method: http.MethodPut, Path: pullPath + "/state", DefaultStatus: http.StatusOK, Summary: "Set pull request kanban state", Tags: []string{"Pull Requests"}}, s.setKanbanState)
	huma.Register(api, huma.Operation{OperationID: "set-kanban-state-on-host", Method: http.MethodPut, Path: hostPullPath + "/state", DefaultStatus: http.StatusOK, Summary: "Set pull request kanban state", Tags: []string{"Pull Requests"}}, s.setKanbanStateOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-content", Method: http.MethodPatch, Path: pullPath, DefaultStatus: http.StatusOK, Summary: "Edit pull request content", Tags: []string{"Pull Requests"}}, s.editPRContent)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-content-on-host", Method: http.MethodPatch, Path: hostPullPath, DefaultStatus: http.StatusOK, Summary: "Edit pull request content", Tags: []string{"Pull Requests"}}, s.editPRContentOnHost)
	huma.Register(api, huma.Operation{OperationID: "post-pr-comment", Method: http.MethodPost, Path: pullPath + "/comments", DefaultStatus: http.StatusCreated, Summary: "Post pull request comment", Tags: []string{"Pull Requests"}}, s.postComment)
	huma.Register(api, huma.Operation{OperationID: "post-pr-comment-on-host", Method: http.MethodPost, Path: hostPullPath + "/comments", DefaultStatus: http.StatusCreated, Summary: "Post pull request comment", Tags: []string{"Pull Requests"}}, s.postCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-comment", Method: http.MethodPatch, Path: pullPath + "/comments/{comment_id}", DefaultStatus: http.StatusOK, Summary: "Edit pull request comment", Tags: []string{"Pull Requests"}}, s.editComment)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-comment-on-host", Method: http.MethodPatch, Path: hostPullPath + "/comments/{comment_id}", DefaultStatus: http.StatusOK, Summary: "Edit pull request comment", Tags: []string{"Pull Requests"}}, s.editCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-pr-labels", Method: http.MethodPut, Path: pullPath + "/labels", DefaultStatus: http.StatusOK, Summary: "Set pull request labels", Tags: []string{"Pull Requests"}}, s.setPullLabels)
	huma.Register(api, huma.Operation{OperationID: "set-pr-labels-on-host", Method: http.MethodPut, Path: hostPullPath + "/labels", DefaultStatus: http.StatusOK, Summary: "Set pull request labels", Tags: []string{"Pull Requests"}}, s.setPullLabelsOnHost)
```

- [ ] **Step 2: Backfill issue creation, detail, content, comments, and labels**

Replace:

```go
	huma.Register(api, huma.Operation{OperationID: "create-issue", Method: http.MethodPost, Path: issueRepoPath, DefaultStatus: http.StatusCreated}, s.createIssue)
	huma.Register(api, huma.Operation{OperationID: "create-issue-on-host", Method: http.MethodPost, Path: hostIssueRepoPath, DefaultStatus: http.StatusCreated}, s.createIssueOnHost)
	huma.Get(api, issuePath, s.getIssue)
	huma.Get(api, hostIssuePath, s.getIssueOnHost)
	huma.Register(api, huma.Operation{OperationID: "post-issue-comment", Method: http.MethodPost, Path: issuePath + "/comments", DefaultStatus: http.StatusCreated}, s.postIssueComment)
	huma.Register(api, huma.Operation{OperationID: "post-issue-comment-on-host", Method: http.MethodPost, Path: hostIssuePath + "/comments", DefaultStatus: http.StatusCreated}, s.postIssueCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-content", Method: http.MethodPatch, Path: issuePath, DefaultStatus: http.StatusOK}, s.editIssueContent)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-content-on-host", Method: http.MethodPatch, Path: hostIssuePath, DefaultStatus: http.StatusOK}, s.editIssueContentOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-comment", Method: http.MethodPatch, Path: issuePath + "/comments/{comment_id}", DefaultStatus: http.StatusOK}, s.editIssueComment)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-comment-on-host", Method: http.MethodPatch, Path: hostIssuePath + "/comments/{comment_id}", DefaultStatus: http.StatusOK}, s.editIssueCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-issue-labels", Method: http.MethodPut, Path: issuePath + "/labels", DefaultStatus: http.StatusOK}, s.setIssueLabels)
	huma.Register(api, huma.Operation{OperationID: "set-issue-labels-on-host", Method: http.MethodPut, Path: hostIssuePath + "/labels", DefaultStatus: http.StatusOK}, s.setIssueLabelsOnHost)
```

with:

```go
	huma.Register(api, huma.Operation{OperationID: "create-issue", Method: http.MethodPost, Path: issueRepoPath, DefaultStatus: http.StatusCreated, Summary: "Create issue", Tags: []string{"Issues"}}, s.createIssue)
	huma.Register(api, huma.Operation{OperationID: "create-issue-on-host", Method: http.MethodPost, Path: hostIssueRepoPath, DefaultStatus: http.StatusCreated, Summary: "Create issue", Tags: []string{"Issues"}}, s.createIssueOnHost)
	huma.Get(api, issuePath, s.getIssue,
		documentOperation("get-issue", "Get issue", "Issues"))
	huma.Get(api, hostIssuePath, s.getIssueOnHost,
		documentOperation("get-issue-on-host", "Get issue", "Issues"))
	huma.Register(api, huma.Operation{OperationID: "post-issue-comment", Method: http.MethodPost, Path: issuePath + "/comments", DefaultStatus: http.StatusCreated, Summary: "Post issue comment", Tags: []string{"Issues"}}, s.postIssueComment)
	huma.Register(api, huma.Operation{OperationID: "post-issue-comment-on-host", Method: http.MethodPost, Path: hostIssuePath + "/comments", DefaultStatus: http.StatusCreated, Summary: "Post issue comment", Tags: []string{"Issues"}}, s.postIssueCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-content", Method: http.MethodPatch, Path: issuePath, DefaultStatus: http.StatusOK, Summary: "Edit issue content", Tags: []string{"Issues"}}, s.editIssueContent)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-content-on-host", Method: http.MethodPatch, Path: hostIssuePath, DefaultStatus: http.StatusOK, Summary: "Edit issue content", Tags: []string{"Issues"}}, s.editIssueContentOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-comment", Method: http.MethodPatch, Path: issuePath + "/comments/{comment_id}", DefaultStatus: http.StatusOK, Summary: "Edit issue comment", Tags: []string{"Issues"}}, s.editIssueComment)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-comment-on-host", Method: http.MethodPatch, Path: hostIssuePath + "/comments/{comment_id}", DefaultStatus: http.StatusOK, Summary: "Edit issue comment", Tags: []string{"Issues"}}, s.editIssueCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-issue-labels", Method: http.MethodPut, Path: issuePath + "/labels", DefaultStatus: http.StatusOK, Summary: "Set issue labels", Tags: []string{"Issues"}}, s.setIssueLabels)
	huma.Register(api, huma.Operation{OperationID: "set-issue-labels-on-host", Method: http.MethodPut, Path: hostIssuePath + "/labels", DefaultStatus: http.StatusOK, Summary: "Set issue labels", Tags: []string{"Issues"}}, s.setIssueLabelsOnHost)
```

- [ ] **Step 3: Backfill repo resolve, repo detail, repo labels, comment autocomplete**

Replace:

```go
	huma.Post(api, repoPath+"/resolve/{number}", s.resolveItem)
	huma.Post(api, hostRepoPath+"/resolve/{number}", s.resolveItemOnHost)
	huma.Get(api, repoPath, s.getRepo)
	huma.Get(api, hostRepoPath, s.getRepoOnHost)
	huma.Register(api, huma.Operation{OperationID: "list-repo-labels", Method: http.MethodGet, Path: repoPath + "/labels", DefaultStatus: http.StatusOK}, s.listRepoLabels)
	huma.Register(api, huma.Operation{OperationID: "list-repo-labels-on-host", Method: http.MethodGet, Path: hostRepoPath + "/labels", DefaultStatus: http.StatusOK}, s.listRepoLabelsOnHost)
	huma.Get(api, repoPath+"/comment-autocomplete", s.getCommentAutocomplete)
	huma.Get(api, hostRepoPath+"/comment-autocomplete", s.getCommentAutocompleteOnHost)
```

with:

```go
	huma.Post(api, repoPath+"/resolve/{number}", s.resolveItem,
		documentOperation("resolve-item", "Resolve repository item", "Repositories"))
	huma.Post(api, hostRepoPath+"/resolve/{number}", s.resolveItemOnHost,
		documentOperation("resolve-item-on-host", "Resolve repository item", "Repositories"))
	huma.Get(api, repoPath, s.getRepo,
		documentOperation("get-repo", "Get repository", "Repositories"))
	huma.Get(api, hostRepoPath, s.getRepoOnHost,
		documentOperation("get-repo-on-host", "Get repository", "Repositories"))
	huma.Register(api, huma.Operation{OperationID: "list-repo-labels", Method: http.MethodGet, Path: repoPath + "/labels", DefaultStatus: http.StatusOK, Summary: "List repository labels", Tags: []string{"Repositories"}}, s.listRepoLabels)
	huma.Register(api, huma.Operation{OperationID: "list-repo-labels-on-host", Method: http.MethodGet, Path: hostRepoPath + "/labels", DefaultStatus: http.StatusOK, Summary: "List repository labels", Tags: []string{"Repositories"}}, s.listRepoLabelsOnHost)
	huma.Get(api, repoPath+"/comment-autocomplete", s.getCommentAutocomplete,
		documentOperation("get-comment-autocomplete", "Get comment autocomplete", "Repositories"))
	huma.Get(api, hostRepoPath+"/comment-autocomplete", s.getCommentAutocompleteOnHost,
		documentOperation("get-comment-autocomplete-on-host", "Get comment autocomplete", "Repositories"))
```

- [ ] **Step 4: Backfill PR mutations (approve, ready-for-review, merge, sync, ci-refresh, github-state)**

Replace:

```go
	huma.Post(api, pullPath+"/approve", s.approvePR)
	huma.Post(api, hostPullPath+"/approve", s.approvePROnHost)
	huma.Post(api, pullPath+"/approve-workflows", s.approveWorkflows)
	huma.Post(api, hostPullPath+"/approve-workflows", s.approveWorkflowsOnHost)
	huma.Post(api, pullPath+"/ready-for-review", s.readyForReview)
	huma.Post(api, hostPullPath+"/ready-for-review", s.readyForReviewOnHost)
	huma.Post(api, pullPath+"/merge", s.mergePR)
	huma.Post(api, hostPullPath+"/merge", s.mergePROnHost)
	huma.Post(api, pullPath+"/sync", s.syncPR)
	huma.Post(api, hostPullPath+"/sync", s.syncPROnHost)
	huma.Post(api, pullPath+"/ci-refresh", s.syncPRCI)
	huma.Post(api, hostPullPath+"/ci-refresh", s.syncPRCIOnHost)
	huma.Register(api, huma.Operation{OperationID: "enqueue-pr-sync", Method: http.MethodPost, Path: pullPath + "/sync/async", DefaultStatus: http.StatusAccepted}, s.enqueuePRSync)
	huma.Register(api, huma.Operation{OperationID: "enqueue-pr-sync-on-host", Method: http.MethodPost, Path: hostPullPath + "/sync/async", DefaultStatus: http.StatusAccepted}, s.enqueuePRSyncOnHost)
	huma.Post(api, issuePath+"/sync", s.syncIssue)
	huma.Post(api, hostIssuePath+"/sync", s.syncIssueOnHost)
	huma.Register(api, huma.Operation{OperationID: "enqueue-issue-sync", Method: http.MethodPost, Path: issuePath + "/sync/async", DefaultStatus: http.StatusAccepted}, s.enqueueIssueSync)
	huma.Register(api, huma.Operation{OperationID: "enqueue-issue-sync-on-host", Method: http.MethodPost, Path: hostIssuePath + "/sync/async", DefaultStatus: http.StatusAccepted}, s.enqueueIssueSyncOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-pr-github-state", Method: http.MethodPost, Path: pullPath + "/github-state", DefaultStatus: http.StatusOK}, s.setPRGitHubState)
	huma.Register(api, huma.Operation{OperationID: "set-pr-github-state-on-host", Method: http.MethodPost, Path: hostPullPath + "/github-state", DefaultStatus: http.StatusOK}, s.setPRGitHubStateOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-issue-github-state", Method: http.MethodPost, Path: issuePath + "/github-state", DefaultStatus: http.StatusOK}, s.setIssueGitHubState)
	huma.Register(api, huma.Operation{OperationID: "set-issue-github-state-on-host", Method: http.MethodPost, Path: hostIssuePath + "/github-state", DefaultStatus: http.StatusOK}, s.setIssueGitHubStateOnHost)
```

with:

```go
	huma.Post(api, pullPath+"/approve", s.approvePR,
		documentOperation("approve-pull", "Approve pull request", "Pull Requests"))
	huma.Post(api, hostPullPath+"/approve", s.approvePROnHost,
		documentOperation("approve-pull-on-host", "Approve pull request", "Pull Requests"))
	huma.Post(api, pullPath+"/approve-workflows", s.approveWorkflows,
		documentOperation("approve-pull-workflows", "Approve pull request workflows", "Pull Requests"))
	huma.Post(api, hostPullPath+"/approve-workflows", s.approveWorkflowsOnHost,
		documentOperation("approve-pull-workflows-on-host", "Approve pull request workflows", "Pull Requests"))
	huma.Post(api, pullPath+"/ready-for-review", s.readyForReview,
		documentOperation("mark-pull-ready-for-review", "Mark pull request ready for review", "Pull Requests"))
	huma.Post(api, hostPullPath+"/ready-for-review", s.readyForReviewOnHost,
		documentOperation("mark-pull-ready-for-review-on-host", "Mark pull request ready for review", "Pull Requests"))
	huma.Post(api, pullPath+"/merge", s.mergePR,
		documentOperation("merge-pull", "Merge pull request", "Pull Requests"))
	huma.Post(api, hostPullPath+"/merge", s.mergePROnHost,
		documentOperation("merge-pull-on-host", "Merge pull request", "Pull Requests"))
	huma.Post(api, pullPath+"/sync", s.syncPR,
		documentOperation("sync-pull", "Sync pull request", "Pull Requests"))
	huma.Post(api, hostPullPath+"/sync", s.syncPROnHost,
		documentOperation("sync-pull-on-host", "Sync pull request", "Pull Requests"))
	huma.Post(api, pullPath+"/ci-refresh", s.syncPRCI,
		documentOperation("refresh-pull-ci", "Refresh pull request CI", "Pull Requests"))
	huma.Post(api, hostPullPath+"/ci-refresh", s.syncPRCIOnHost,
		documentOperation("refresh-pull-ci-on-host", "Refresh pull request CI", "Pull Requests"))
	huma.Register(api, huma.Operation{OperationID: "enqueue-pr-sync", Method: http.MethodPost, Path: pullPath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue pull request sync", Tags: []string{"Pull Requests"}}, s.enqueuePRSync)
	huma.Register(api, huma.Operation{OperationID: "enqueue-pr-sync-on-host", Method: http.MethodPost, Path: hostPullPath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue pull request sync", Tags: []string{"Pull Requests"}}, s.enqueuePRSyncOnHost)
	huma.Post(api, issuePath+"/sync", s.syncIssue,
		documentOperation("sync-issue", "Sync issue", "Issues"))
	huma.Post(api, hostIssuePath+"/sync", s.syncIssueOnHost,
		documentOperation("sync-issue-on-host", "Sync issue", "Issues"))
	huma.Register(api, huma.Operation{OperationID: "enqueue-issue-sync", Method: http.MethodPost, Path: issuePath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue issue sync", Tags: []string{"Issues"}}, s.enqueueIssueSync)
	huma.Register(api, huma.Operation{OperationID: "enqueue-issue-sync-on-host", Method: http.MethodPost, Path: hostIssuePath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue issue sync", Tags: []string{"Issues"}}, s.enqueueIssueSyncOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-pr-github-state", Method: http.MethodPost, Path: pullPath + "/github-state", DefaultStatus: http.StatusOK, Summary: "Set pull request GitHub state", Tags: []string{"Pull Requests"}}, s.setPRGitHubState)
	huma.Register(api, huma.Operation{OperationID: "set-pr-github-state-on-host", Method: http.MethodPost, Path: hostPullPath + "/github-state", DefaultStatus: http.StatusOK, Summary: "Set pull request GitHub state", Tags: []string{"Pull Requests"}}, s.setPRGitHubStateOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-issue-github-state", Method: http.MethodPost, Path: issuePath + "/github-state", DefaultStatus: http.StatusOK, Summary: "Set issue GitHub state", Tags: []string{"Issues"}}, s.setIssueGitHubState)
	huma.Register(api, huma.Operation{OperationID: "set-issue-github-state-on-host", Method: http.MethodPost, Path: hostIssuePath + "/github-state", DefaultStatus: http.StatusOK, Summary: "Set issue GitHub state", Tags: []string{"Issues"}}, s.setIssueGitHubStateOnHost)
```

- [ ] **Step 5: Backfill PR commits/diff/files/file-preview/stack and create-issue-workspace**

Replace:

```go
	huma.Get(api, pullPath+"/commits", s.getCommits)
	huma.Get(api, hostPullPath+"/commits", s.getCommitsOnHost)
	huma.Get(api, pullPath+"/diff", s.getDiff)
	huma.Get(api, hostPullPath+"/diff", s.getDiffOnHost)
	huma.Get(api, pullPath+"/files", s.getFiles)
	huma.Get(api, hostPullPath+"/files", s.getFilesOnHost)
	huma.Get(api, pullPath+"/file-preview", s.getFilePreview)
	huma.Get(api, hostPullPath+"/file-preview", s.getFilePreviewOnHost)
	huma.Get(api, pullPath+"/stack", s.getStackForPR)
	huma.Get(api, hostPullPath+"/stack", s.getStackForPROnHost)
	huma.Register(api, huma.Operation{OperationID: "create-issue-workspace", Method: http.MethodPost, Path: issuePath + "/workspace", DefaultStatus: http.StatusAccepted}, s.createIssueWorkspace)
	huma.Register(api, huma.Operation{OperationID: "create-issue-workspace-on-host", Method: http.MethodPost, Path: hostIssuePath + "/workspace", DefaultStatus: http.StatusAccepted}, s.createIssueWorkspaceOnHost)
```

with:

```go
	huma.Get(api, pullPath+"/commits", s.getCommits,
		documentOperation("get-pull-commits", "Get pull request commits", "Pull Requests"))
	huma.Get(api, hostPullPath+"/commits", s.getCommitsOnHost,
		documentOperation("get-pull-commits-on-host", "Get pull request commits", "Pull Requests"))
	huma.Get(api, pullPath+"/diff", s.getDiff,
		documentOperation("get-pull-diff", "Get pull request diff", "Pull Requests"))
	huma.Get(api, hostPullPath+"/diff", s.getDiffOnHost,
		documentOperation("get-pull-diff-on-host", "Get pull request diff", "Pull Requests"))
	huma.Get(api, pullPath+"/files", s.getFiles,
		documentOperation("get-pull-files", "Get pull request files", "Pull Requests"))
	huma.Get(api, hostPullPath+"/files", s.getFilesOnHost,
		documentOperation("get-pull-files-on-host", "Get pull request files", "Pull Requests"))
	huma.Get(api, pullPath+"/file-preview", s.getFilePreview,
		documentOperation("get-pull-file-preview", "Get pull request file preview", "Pull Requests"))
	huma.Get(api, hostPullPath+"/file-preview", s.getFilePreviewOnHost,
		documentOperation("get-pull-file-preview-on-host", "Get pull request file preview", "Pull Requests"))
	huma.Get(api, pullPath+"/stack", s.getStackForPR,
		documentOperation("get-pull-stack", "Get pull request stack", "Pull Requests"))
	huma.Get(api, hostPullPath+"/stack", s.getStackForPROnHost,
		documentOperation("get-pull-stack-on-host", "Get pull request stack", "Pull Requests"))
	huma.Register(api, huma.Operation{OperationID: "create-issue-workspace", Method: http.MethodPost, Path: issuePath + "/workspace", DefaultStatus: http.StatusAccepted, Summary: "Create issue workspace", Tags: []string{"Issues"}}, s.createIssueWorkspace)
	huma.Register(api, huma.Operation{OperationID: "create-issue-workspace-on-host", Method: http.MethodPost, Path: hostIssuePath + "/workspace", DefaultStatus: http.StatusAccepted, Summary: "Create issue workspace", Tags: []string{"Issues"}}, s.createIssueWorkspaceOnHost)
```

- [ ] **Step 6: Run the contract test and verify only settings routes are left**

Run:

```bash
nix run 'nixpkgs#go' -- test ./internal/server -run TestHumaContractMetadata -shuffle=on
```

Expected: FAIL, with the remaining failures referring only to settings paths: `/settings`, `/repos`, `/repo/{provider}/{owner}/{name}`, `/repo/{provider}/{owner}/{name}/refresh`, `/host/{platform_host}/repo/{provider}/{owner}/{name}`, `/host/{platform_host}/repo/{provider}/{owner}/{name}/refresh`.

- [ ] **Step 7: Commit the provider-repo backfill**

```bash
git add internal/server/huma_routes.go
git commit -m "$(cat <<'EOF'
feat(server): document provider-repo Huma routes

Backfills Summary, Tags, and OperationID on every route inside
registerProviderRepoAPI. Tags follow the spec taxonomy: PR detail,
content, comments, labels, kanban state, mutations (approve / merge /
ready-for-review / sync / ci-refresh), commits/diff/files/file-preview/
stack, and create-issue-workspace all carry the Pull Requests or
Issues tag. Repo-level reads (resolve, get, list labels, comment
autocomplete) carry the Repositories tag.

Convenience-helper calls gain a documentOperation() argument; existing
huma.Register blocks gain Summary and Tags fields without moving the
OperationID. Host-prefixed variants share the same Summary and Tag and
get a unique -on-host OperationID suffix so the contract test's
uniqueness check passes.

Per-PR sync and per-issue sync remain under their primary tag
(Pull Requests / Issues) rather than the global Sync tag, matching the
taxonomy in the spec.
EOF
)"
```

---

### Task 4: Backfill Settings Routes And Commit The Contract Test

After this task the contract test passes; the final settings backfill plus the previously-uncommitted `route_metadata_test.go` from Task 1 land in one atomic commit that flips the OpenAPI document from un-tagged to fully tagged and locks the new invariant in.

**Files:**
- Modify: `internal/server/settings_routes.go`
- Add (from Task 1): `internal/server/route_metadata_test.go`

- [ ] **Step 1: Backfill every block in registerSettingsAPI**

Replace the entire body of `registerSettingsAPI` (lines 41-80) with:

```go
func (s *Server) registerSettingsAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-settings",
		Method:      http.MethodGet,
		Path:        "/settings",
		Summary:     "Get settings",
		Tags:        []string{"Settings"},
	}, s.getSettings)
	huma.Register(api, huma.Operation{
		OperationID: "update-settings",
		Method:      http.MethodPut,
		Path:        "/settings",
		Summary:     "Update settings",
		Tags:        []string{"Settings"},
	}, s.updateSettings)
	huma.Register(api, huma.Operation{
		OperationID:   "add-repo",
		Method:        http.MethodPost,
		Path:          "/repos",
		DefaultStatus: http.StatusCreated,
		Summary:       "Add repository",
		Tags:          []string{"Settings"},
	}, s.addConfiguredRepo)
	huma.Register(api, huma.Operation{
		OperationID: "refresh-repo",
		Method:      http.MethodPost,
		Path:        "/repo/{provider}/{owner}/{name}/refresh",
		Summary:     "Refresh repository",
		Tags:        []string{"Settings"},
	}, s.refreshConfiguredRepo)
	huma.Register(api, huma.Operation{
		OperationID: "refresh-repo-on-host",
		Method:      http.MethodPost,
		Path:        "/host/{platform_host}/repo/{provider}/{owner}/{name}/refresh",
		Summary:     "Refresh repository",
		Tags:        []string{"Settings"},
	}, s.refreshConfiguredRepoOnHost)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-repo",
		Method:        http.MethodDelete,
		Path:          "/repo/{provider}/{owner}/{name}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete repository",
		Tags:          []string{"Settings"},
	}, s.deleteConfiguredRepo)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-repo-on-host",
		Method:        http.MethodDelete,
		Path:          "/host/{platform_host}/repo/{provider}/{owner}/{name}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete repository",
		Tags:          []string{"Settings"},
	}, s.deleteConfiguredRepoOnHost)
}
```

- [ ] **Step 2: Run the contract test and verify it passes**

Run:

```bash
nix run 'nixpkgs#go' -- test ./internal/server -run TestHumaContractMetadata -shuffle=on
```

Expected: PASS. The walker should find no missing-or-auto-generated metadata anywhere in the OpenAPI document.

- [ ] **Step 3: Run the full server package tests to confirm no regressions**

Run:

```bash
nix run 'nixpkgs#go' -- test ./internal/server -shuffle=on
```

Expected: PASS. No test in `internal/server` should depend on the auto-generated OperationID or Summary text; if any do, the failure surface is small and obvious.

- [ ] **Step 4: Commit the settings backfill together with the contract test**

```bash
git add internal/server/settings_routes.go internal/server/route_metadata_test.go
git commit -m "$(cat <<'EOF'
feat(server): document settings routes and land OpenAPI metadata contract test

Backfills Summary and Tags on every block in registerSettingsAPI:
get-settings, update-settings, add-repo, refresh-repo (and -on-host),
delete-repo (and -on-host). All carry the Settings tag.

With this commit every non-Hidden operation in /api/v1/openapi.json
carries an explicit Summary, exactly one known Tag, and a unique
OperationID. The new TestHumaContractMetadata walks the live OpenAPI
document and fails the build if a future change drops any of those
three. Teeth-tests guard against the assertion becoming a no-op, and
a source-level check keeps Huma convenience routes on documentOperation.
EOF
)"
```

---

### Task 5: Regenerate Checked-In API Artifacts

**Files:**
- Modify: `frontend/openapi/openapi.yaml`
- Modify: `internal/apiclient/generated/client.gen.go`
- Modify: `packages/ui/src/api/generated/schema.ts`
- May modify: `packages/ui/src/api/generated/client.ts`
- May modify: `internal/server/api_test.go`, `internal/server/tmux_wrapper_test.go`, `internal/server/apitest/api_test.go`, `internal/server/workspacetest/*.go` (rename references to auto-generated client methods)
- May modify: `packages/ui/src/api/types.ts` (rename `operations["get-activity"]` to `operations["list-activity"]`)

- [ ] **Step 1: Regenerate via make api-generate**

Run:

```bash
nix shell 'nixpkgs#go' 'nixpkgs#bun' --command make api-generate
```

Expected: writes `frontend/openapi/openapi.yaml`, `internal/apiclient/spec/openapi.json` (gitignored), `packages/ui/src/api/generated/schema.ts`, `packages/ui/src/api/generated/client.ts`, and `internal/apiclient/generated/client.gen.go`. The Go client diff will be large but mechanical — every operation's method/type names change to match the new OperationIDs.

- [ ] **Step 2: Verify the regenerated OpenAPI document carries the new metadata**

Spot-check one operation:

```bash
nix run 'nixpkgs#jq' -- '.paths["/pulls/{provider}/{owner}/{name}/{number}/approve"].post | {operationId, summary, tags}' internal/apiclient/spec/openapi.json
```

Expected: `{ "operationId": "approve-pull", "summary": "Approve pull request", "tags": ["Pull Requests"] }`.

If `internal/apiclient/spec/openapi.json` is gitignored and not present after generation, use the YAML form instead:

```bash
nix run 'nixpkgs#yq-go' -- '.paths["/pulls/{provider}/{owner}/{name}/{number}/approve"].post | pick(["operationId","summary","tags"])' frontend/openapi/openapi.yaml
```

Expected: same content rendered as YAML.

- [ ] **Step 3: Try to build the server tests and surface any consumer of the old method names**

Run:

```bash
nix run 'nixpkgs#go' -- vet ./internal/server/... ./internal/apiclient/...
```

Expected: many `undefined: <OldMethodName>` errors in `internal/server/api_test.go`, `internal/server/tmux_wrapper_test.go`, `internal/server/apitest/api_test.go`, and `internal/server/workspacetest/*_test.go`. The Go test files reference the previously auto-generated client method names (for example `client.HTTP.PostPullsByProviderByOwnerByNameByNumberApproveWithResponse`), which the regeneration renamed (to `client.HTTP.ApprovePullWithResponse`, etc.).

Locate the old method-name references:

```bash
nix run 'nixpkgs#ripgrep' -- "client\.HTTP\.(Get|Post|Put|Patch|Delete)(Pulls|Issues|Repo|HostByPlatformHost)ByProviderByOwnerByName|client\.HTTP\.(Get|Post)HostByPlatformHost(Pulls|Issues|Repo|RepoByProvider)" --type go --glob '!internal/apiclient/generated/**'
```

For each match, rewrite the call site to the new OperationID-derived method name. The mapping is mechanical: a route with OperationID `approve-pull` produces `ApprovePullWithResponse`. Lookups in the regenerated `internal/apiclient/generated/client.gen.go` resolve any uncertainty. Concrete renames most likely to appear:

| Old method (post-regen: gone) | New method |
|---|---|
| `PostPullsByProviderByOwnerByNameByNumberApproveWithResponse` | `ApprovePullWithResponse` |
| `PostHostByPlatformHostPullsByProviderByOwnerByNameByNumberApproveWithResponse` | `ApprovePullOnHostWithResponse` |
| `PostPullsByProviderByOwnerByNameByNumberApproveWorkflowsWithResponse` | `ApprovePullWorkflowsWithResponse` |
| `PostPullsByProviderByOwnerByNameByNumberMergeWithResponse` | `MergePullWithResponse` |
| `PostHostByPlatformHostPullsByProviderByOwnerByNameByNumberMergeWithResponse` | `MergePullOnHostWithResponse` |
| `PostPullsByProviderByOwnerByNameByNumberReadyForReviewWithResponse` | `MarkPullReadyForReviewWithResponse` |
| `PostPullsByProviderByOwnerByNameByNumberSyncWithResponse` | `SyncPullWithResponse` |
| `PostHostByPlatformHostPullsByProviderByOwnerByNameByNumberSyncWithResponse` | `SyncPullOnHostWithResponse` |
| `PostPullsByProviderByOwnerByNameByNumberCiRefreshWithResponse` | `RefreshPullCiWithResponse` |
| `PostHostByPlatformHostPullsByProviderByOwnerByNameByNumberCiRefreshWithResponse` | `RefreshPullCiOnHostWithResponse` |
| `GetPullsByProviderByOwnerByNameByNumberWithResponse` | `GetPullWithResponse` |
| `GetHostByPlatformHostPullsByProviderByOwnerByNameByNumberWithResponse` | `GetPullOnHostWithResponse` |
| `GetPullsByProviderByOwnerByNameByNumberCommitsWithResponse` | `GetPullCommitsWithResponse` |
| `GetPullsByProviderByOwnerByNameByNumberDiffWithResponse` | `GetPullDiffWithResponse` |
| `GetPullsByProviderByOwnerByNameByNumberFilesWithResponse` | `GetPullFilesWithResponse` |
| `GetPullsByProviderByOwnerByNameByNumberFilePreviewWithResponse` | `GetPullFilePreviewWithResponse` |
| `GetPullsByProviderByOwnerByNameByNumberStackWithResponse` | `GetPullStackWithResponse` |
| `GetIssuesByProviderByOwnerByNameByNumberWithResponse` | `GetIssueWithResponse` |
| `GetHostByPlatformHostIssuesByProviderByOwnerByNameByNumberWithResponse` | `GetIssueOnHostWithResponse` |
| `PostIssuesByProviderByOwnerByNameByNumberSyncWithResponse` | `SyncIssueWithResponse` |
| `PostHostByPlatformHostIssuesByProviderByOwnerByNameByNumberSyncWithResponse` | `SyncIssueOnHostWithResponse` |
| `PostRepoByProviderByOwnerByNameResolveByNumberWithResponse` | `ResolveRepoItemWithResponse` |
| `GetActivityWithResponse` | `ListActivityWithResponse` |

If the same line carries a `JSONRequestBody` type that was also renamed (for example `PostPullsByProviderByOwnerByNameByNumberMergeJSONRequestBody` → `MergePullJSONRequestBody`), rename that too.

Some `client.HTTP.*` method names in the test files (`CreateIssueWithResponse`, `CreateWorkspaceWithResponse`, `ListPullsWithResponse`, `SetKanbanStateWithResponse`, `TriggerSyncWithResponse`, etc.) already derive from existing explicit OperationIDs that the spec keeps as-is. Those don't change. Confirm by checking the new `internal/apiclient/generated/client.gen.go` after regeneration.

- [ ] **Step 4: Rename the TS consumer**

The pre-existing OperationID `get-activity` (Huma's auto-generated kebab-case from `GET /activity`) becomes `list-activity` after the backfill. Update the TypeScript consumer:

In `packages/ui/src/api/types.ts`, change:

```ts
export type ActivityParams = NonNullable<operations["get-activity"]["parameters"]["query"]>;
```

to:

```ts
export type ActivityParams = NonNullable<operations["list-activity"]["parameters"]["query"]>;
```

Verify no other TS callsite references `operations["get-activity"]`:

```bash
nix run 'nixpkgs#ripgrep' -- 'operations\["get-activity"\]' packages/ frontend/
```

Expected: only `packages/ui/src/api/types.ts` (which was just updated) and `packages/ui/src/api/generated/schema.ts` (which is the generated definition site and now reads `"list-activity"`). If anything else surfaces, rename it too.

- [ ] **Step 5: Run the full Go test suite to confirm renames compile and pass**

Run:

```bash
nix run 'nixpkgs#go' -- test ./... -shuffle=on
```

Expected: PASS.

- [ ] **Step 6: Run frontend type checks to confirm TS consumers compile**

Run:

```bash
nix shell 'nixpkgs#bun' --command bash -c 'cd packages/ui && bun run typecheck'
```

Expected: PASS. The `ActivityParams` rename resolves the `get-activity` → `list-activity` shift; no other TS site refers to a renamed operation by ID.

- [ ] **Step 7: Commit the regenerated artifacts together with the consumer renames**

The regeneration and the consumer renames must commit together because individually they leave the tree in a non-compiling state: the regenerated client lacks the old method names, the unrenamed tests reference the old method names, and the unrenamed `ActivityParams` references a no-longer-existing operation key. Stage everything together:

```bash
git add frontend/openapi/openapi.yaml internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts packages/ui/src/api/generated/client.ts packages/ui/src/api/types.ts
git add internal/server/api_test.go internal/server/tmux_wrapper_test.go internal/server/apitest/ internal/server/workspacetest/
git commit -m "$(cat <<'EOF'
chore(api): regenerate clients with explicit OperationID, Summary, Tags

Regenerates frontend/openapi/openapi.yaml, internal/apiclient/
generated/client.gen.go, and packages/ui/src/api/generated/schema.ts
from the now-fully-documented Huma routes. Method and type names in
the Go client and TS schema track the new OperationIDs, so paths like
PostPullsByProviderByOwnerByNameByNumberApprove become ApprovePull.

Also renames every consumer of the auto-generated method names in
internal/server/api_test.go and the apitest / workspacetest /
tmux_wrapper_test files, plus the one TS consumer that referenced
the auto-generated operations["get-activity"] type (now
operations["list-activity"]).

Diff in the generated files is large but mechanical: every operation's
name and the surrounding response/request types shift in lockstep
with the upstream OperationID.
EOF
)"
```

If `git add` of an `internal/server/apitest/` or `workspacetest/` path fails because nothing in those directories changed, drop the missing path and re-run the add+commit pair.

---

### Task 6: Final Lint And Vet Pass

**Files:**
- None (verification only).

- [ ] **Step 1: Run go vet across the workspace**

```bash
nix run 'nixpkgs#go' -- vet ./...
```

Expected: PASS. No vet diagnostics.

- [ ] **Step 2: Run golangci-lint via nix**

The Makefile lint target requires the globally-banned `mise`. Use golangci-lint directly:

```bash
nix shell 'nixpkgs#golangci-lint' 'nixpkgs#go' --command golangci-lint run ./...
```

Expected: PASS. No new warnings.

- [ ] **Step 3: Run testify helper check (replicates Makefile lint's second half)**

```bash
nix run 'nixpkgs#go' -- run ./cmd/testify-helper-check ./...
```

Expected: PASS. The contract test uses a local `assert := require.New(t)` for the document-build precondition and falls back to package-level `assert.Empty(t, ...)` for the failures slice; both are explicit `t`-receiver calls within testify's idioms and the assertion-counter helper should not flag them.

- [ ] **Step 4: Run the full Go test suite one last time**

```bash
nix run 'nixpkgs#go' -- test ./... -shuffle=on
```

Expected: PASS. No regressions.

- [ ] **Step 5: Verify the working tree is clean**

```bash
git status
```

Expected: `nothing to commit, working tree clean`.

No commit in this task — verification only.

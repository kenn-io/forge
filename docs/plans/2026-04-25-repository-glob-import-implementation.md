# Repository Glob Import Implementation Plan

> **For agentic workers:** REQUIRED: Use `/skill:orchestrator-implements` (in-session, orchestrator implements), `/skill:subagent-driven-development` (in-session, subagents implement), or `/skill:executing-plans` (parallel session) to implement this plan. Steps use checkbox syntax for tracking.

**Goal:** Add a staged repository import workflow that previews repositories matching `owner/pattern`, lets users filter/sort/select a subset, and persists selected repositories as exact config entries.

**Architecture:** Add two manual `net/http` settings routes for preview and bulk add, keeping Huma/OpenAPI out of scope. Add a Svelte modal with a pure TypeScript selection/filter/sort helper so UI behavior is deterministic and easy to test. Preserve existing direct exact/glob add path inside an advanced section.

**Tech Stack:** Go `net/http`, SQLite-backed integration tests, `go-github/v84`, Svelte 5 runes, Vitest, Testing Library, Playwright full-stack e2e, Bun.

---

## Files and responsibilities

- Create `internal/server/repo_import_handlers.go`
  - Request/response types for preview and bulk add.
  - Pattern and exact repo validation helpers.
  - Preview row assembly from GitHub repository list.
  - Bulk exact repo prevalidation and config application.
  - HTTP handlers `handlePreviewRepos` and `handleBulkAddRepos`.
- Modify `internal/server/server.go`
  - Register `POST /api/v1/repos/preview` and `POST /api/v1/repos/bulk` beside existing settings repo routes.
- Modify `internal/server/settings_test.go`
  - Add full-stack HTTP tests for preview and bulk add using real config, real SQLite, and mock GitHub client.
- Modify `cmd/e2e-server/main.go`
  - Seed repository metadata for preview rows and add a small request-order hook for latest-preview-wins e2e.
- Modify `frontend/src/lib/api/settings.ts`
  - Add `RepoPreviewRow`, `RepoPreviewResponse`, `previewRepos`, `bulkAddRepos`, and shared error parsing.
- Modify `frontend/src/lib/api/settings.test.ts`
  - Test error envelope parsing and bulk/preview request bodies.
- Create `frontend/src/lib/components/settings/repoImportSelection.ts`
  - Pure input parsing, row keys, filtering, sorting, all/none selection, shift-click range behavior, and submit ordering.
- Create `frontend/src/lib/components/settings/repoImportSelection.test.ts`
  - Unit tests for helper semantics.
- Create `frontend/src/lib/components/settings/RepoPreviewTable.svelte`
  - Presentational table with filters, status selector, sortable headers, checkboxes, all/none buttons, and disabled already-added rows.
- Create `frontend/src/lib/components/settings/RepoImportModal.svelte`
  - Modal state, preview/bulk requests, request invalidation, focus handling, and success propagation.
- Modify `frontend/src/lib/components/settings/RepoSettings.svelte`
  - Add primary import action, advanced direct add details, modal integration, and embedded-mode gating.
- Modify `frontend/src/lib/components/settings/RepoSettings.test.ts`
  - Cover opening modal, advanced direct add, update callback, and embedded controls.
- Create `frontend/src/lib/components/settings/RepoImportModal.test.ts`
  - Cover preview success, filtering/sorting/selection, stale request invalidation, failed preview clearing rows, and successful bulk add.
- Modify `frontend/tests/e2e-full/settings-globs.spec.ts`
  - Add full-stack staged import flow and request-race/failure coverage.

---

### Task 1: Backend preview and bulk-add tests

**TDD scenario:** New feature — full TDD cycle.

**Files:**
- Modify: `internal/server/settings_test.go`
- Implemented in later task files: `internal/server/repo_import_handlers.go`, `internal/server/server.go`

- [ ] **Step 1: Write failing backend tests**

Append these tests near existing settings repo tests in `internal/server/settings_test.go`. Use existing `mockGH`, `setupTestServerWithConfigContent`, `doJSON`, `Assert`, and `require` helpers.

```go
func TestHandlePreviewReposFiltersAndMarksAlreadyConfigured(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	pushedNewer := gh.Timestamp{Time: time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)}
	pushedOlder := gh.Timestamp{Time: time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)}
	privateRepo := true
	publicRepo := false
	mock := &mockGH{
		listReposByOwnerFn: func(_ context.Context, owner string) ([]*gh.Repository, error) {
			return []*gh.Repository{
				{
					Name:        new("widget"),
					Owner:       &gh.User{Login: new(owner)},
					Description: new("already configured widget"),
					Private:     &privateRepo,
					Archived:    new(false),
					PushedAt:    &pushedOlder,
				},
				{
					Name:        new("widget-api"),
					Owner:       &gh.User{Login: new(owner)},
					Description: new("api service"),
					Private:     &publicRepo,
					Archived:    new(false),
					PushedAt:    &pushedNewer,
				},
				{
					Name:     new("widget-archive"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(true),
					PushedAt: &pushedNewer,
				},
				{
					Name:     new("other"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
			}
		},
	}
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "widget-*"
`, mock)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"owner": " ACME ",
		"pattern": " Widget* ",
	})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp repoPreviewResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 2)
	assert.Equal("ACME", resp.Owner)
	assert.Equal("Widget*", resp.Pattern)
	assert.Equal("acme", resp.Repos[0].Owner)
	assert.Equal("widget", resp.Repos[0].Name)
	assert.Equal("already configured widget", *resp.Repos[0].Description)
	assert.True(resp.Repos[0].Private)
	assert.True(resp.Repos[0].AlreadyConfigured)
	require.NotNil(resp.Repos[0].PushedAt)
	assert.Equal(pushedOlder.Time.UTC().Format(time.RFC3339), *resp.Repos[0].PushedAt)
	assert.Equal("widget-api", resp.Repos[1].Name)
	assert.False(resp.Repos[1].Private)
	assert.False(resp.Repos[1].AlreadyConfigured)
	assert.NotContains(rr.Body.String(), "widget-archive")
	assert.NotContains(rr.Body.String(), "other")
}

func TestHandlePreviewReposRejectsInvalidPattern(t *testing.T) {
	srv, _, _ := setupTestServerWithConfig(t)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"owner": "acme*",
		"pattern": "widget",
	})
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	Assert.Contains(t, rr.Body.String(), "glob syntax in owner is not supported")

	rr = doJSON(t, srv, http.MethodPost, "/api/v1/repos/preview", map[string]string{
		"owner": "acme",
		"pattern": "widget[",
	})
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	Assert.Contains(t, rr.Body.String(), "invalid glob pattern")
}

func TestHandleBulkAddReposPersistsExactRepos(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	var getCalls atomic.Int32
	mock := &mockGH{
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			getCalls.Add(1)
			return &gh.Repository{
				Name:     new(strings.ToUpper(repo)),
				Owner:    &gh.User{Login: new(strings.ToUpper(owner))},
				Archived: new(false),
			}, nil
		},
	}
	srv, _, cfgPath := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, mock)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{"owner": " acme ", "name": " api "},
			{"owner": "acme", "name": "worker"},
			{"owner": "acme", "name": "api"},
		},
	})
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())
	assert.Equal(int32(2), getCalls.Load())

	var resp settingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(resp.Repos, 3)
	assert.Equal("acme", resp.Repos[1].Owner)
	assert.Equal("api", resp.Repos[1].Name)
	assert.Equal("worker", resp.Repos[2].Name)
	assert.True(srv.syncer.IsTrackedRepo("acme", "api"))
	assert.True(srv.syncer.IsTrackedRepo("acme", "worker"))

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 3)
	assert.Equal("api", cfg2.Repos[1].Name)
	assert.Equal("worker", cfg2.Repos[2].Name)
}

func TestHandleBulkAddReposValidationFailureChangesNothing(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	mock := &mockGH{
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			if repo == "missing" {
				return nil, errors.New("not found")
			}
			return &gh.Repository{
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}, nil
		},
	}
	srv, _, cfgPath := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, mock)

	rr := doJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
		"repos": []map[string]string{
			{"owner": "acme", "name": "api"},
			{"owner": "acme", "name": "missing"},
		},
	})
	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	assert.False(srv.syncer.IsTrackedRepo("acme", "api"))

	cfg2, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg2.Repos, 1)
	assert.Equal("widget", cfg2.Repos[0].Name)
}

func TestHandleBulkAddReposSkipsAlreadyConfiguredAtApplyTime(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	var unblockGet = make(chan struct{})
	var getStarted = make(chan struct{}, 1)
	mock := &mockGH{
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			if repo == "api" {
				getStarted <- struct{}{}
				<-unblockGet
			}
			return &gh.Repository{
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}, nil
		},
	}
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, mock)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- doJSON(t, srv, http.MethodPost, "/api/v1/repos/bulk", map[string]any{
			"repos": []map[string]string{
				{"owner": "acme", "name": "api"},
				{"owner": "acme", "name": "worker"},
			},
		})
	}()

	select {
	case <-getStarted:
	case <-time.After(5 * time.Second):
		require.FailNow("bulk validation did not start")
	}
	addRR := doJSON(t, srv, http.MethodPost, "/api/v1/repos", map[string]string{
		"owner": "acme", "name": "api",
	})
	require.Equal(http.StatusCreated, addRR.Code, addRR.Body.String())
	close(unblockGet)

	var rr *httptest.ResponseRecorder
	select {
	case rr = <-done:
	case <-time.After(5 * time.Second):
		require.FailNow("bulk add did not finish")
	}
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())
	var resp settingsResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal([]string{"widget", "api", "worker"}, []string{resp.Repos[0].Name, resp.Repos[1].Name, resp.Repos[2].Name})
}
```

If `strings` is not already imported, add it to the import list. Keep `sync/atomic`, `httptest`, and `time` because existing tests already use them.

- [ ] **Step 2: Run backend tests and verify failure**

Run:

```bash
go test ./internal/server -run 'TestHandlePreviewRepos|TestHandleBulkAddRepos' -shuffle=on
```

Expected: compile failure because `repoPreviewResponse`, `handlePreviewRepos`, or `/api/v1/repos/preview` does not exist.

- [ ] **Step 3: Commit failing tests**

Do not commit failing tests alone unless working in a TDD checkpoint branch with explicit approval. For normal execution, continue to Task 2 before first commit.

---

### Task 2: Backend repository import handlers

**TDD scenario:** Implement minimal code to pass Task 1 tests.

**Files:**
- Create: `internal/server/repo_import_handlers.go`
- Modify: `internal/server/server.go`
- Test: `internal/server/settings_test.go`

- [ ] **Step 1: Register endpoints**

In `internal/server/server.go`, add these routes beside the existing settings repo routes:

```go
mux.HandleFunc("POST /api/v1/repos/preview", s.handlePreviewRepos)
mux.HandleFunc("POST /api/v1/repos/bulk", s.handleBulkAddRepos)
```

Place them near:

```go
mux.HandleFunc("POST /api/v1/repos", s.handleAddRepo)
mux.HandleFunc("POST /api/v1/repos/{owner}/{name}/refresh", s.handleRefreshRepo)
mux.HandleFunc("DELETE /api/v1/repos/{owner}/{name}", s.handleDeleteRepo)
```

- [ ] **Step 2: Add request/response types**

Create `internal/server/repo_import_handlers.go` with package `server` and these types:

```go
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	gh "github.com/google/go-github/v84/github"
	"github.com/wesm/middleman/internal/config"
	ghclient "github.com/wesm/middleman/internal/github"
)

type repoPreviewRequest struct {
	Owner   string `json:"owner"`
	Pattern string `json:"pattern"`
}

type repoPreviewResponse struct {
	Owner   string           `json:"owner"`
	Pattern string           `json:"pattern"`
	Repos   []repoPreviewRow `json:"repos"`
}

type repoPreviewRow struct {
	Owner             string  `json:"owner"`
	Name              string  `json:"name"`
	Description       *string `json:"description"`
	Private           bool    `json:"private"`
	PushedAt          *string `json:"pushed_at"`
	AlreadyConfigured bool    `json:"already_configured"`
}

type bulkAddReposRequest struct {
	Repos []bulkAddRepoRequest `json:"repos"`
}

type bulkAddRepoRequest struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

type resolvedBulkRepo struct {
	Config config.Repo
	Ref    ghclient.RepoRef
}
```

- [ ] **Step 3: Add validation helpers**

In `repo_import_handlers.go`, add:

```go
func normalizeImportOwnerPattern(owner, pattern string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	pattern = strings.TrimSpace(pattern)
	if owner == "" || pattern == "" {
		return "", "", fmt.Errorf("owner and pattern are required")
	}
	if strings.Contains(owner, "/") || strings.ContainsAny(owner, "*?[]") {
		return "", "", fmt.Errorf("glob syntax in owner is not supported")
	}
	if strings.Contains(pattern, "/") {
		return "", "", fmt.Errorf("pattern must not contain /")
	}
	if _, err := path.Match(strings.ToLower(pattern), ""); err != nil {
		return "", "", fmt.Errorf("invalid glob pattern: %w", err)
	}
	return owner, pattern, nil
}

func normalizeExactRepoInput(owner, name string) (config.Repo, error) {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" || name == "" {
		return config.Repo{}, fmt.Errorf("owner and name are required")
	}
	if strings.Contains(owner, "/") || strings.Contains(name, "/") ||
		strings.ContainsAny(owner, "*?[]") || strings.ContainsAny(name, "*?[]") {
		return config.Repo{}, fmt.Errorf("bulk add only accepts exact owner/name repositories")
	}
	return config.Repo{Owner: owner, Name: name}, nil
}
```

- [ ] **Step 4: Add exact configured set and preview row builder**

In `repo_import_handlers.go`, add:

```go
func exactConfiguredRepoSet(repos []config.Repo) map[string]struct{} {
	set := make(map[string]struct{}, len(repos))
	for _, repo := range repos {
		if repo.HasNameGlob() {
			continue
		}
		owner := strings.ToLower(strings.TrimSpace(repo.Owner))
		name := strings.ToLower(strings.TrimSpace(repo.Name))
		if owner == "" || name == "" {
			continue
		}
		set[owner+"/"+name] = struct{}{}
	}
	return set
}

func buildRepoPreviewRows(
	ctx context.Context,
	client ghclient.Client,
	exactConfigured map[string]struct{},
	owner, pattern string,
) ([]repoPreviewRow, error) {
	repos, err := client.ListRepositoriesByOwner(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("list repositories for preview %s/%s: %w", owner, pattern, err)
	}
	rows := make([]repoPreviewRow, 0, len(repos))
	for _, repo := range repos {
		if repo.GetArchived() {
			continue
		}
		name := repo.GetName()
		matched, err := path.Match(strings.ToLower(pattern), strings.ToLower(name))
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern: %w", err)
		}
		if !matched {
			continue
		}
		canonicalOwner := repo.GetOwner().GetLogin()
		if canonicalOwner == "" {
			canonicalOwner = owner
		}
		canonicalOwner = strings.ToLower(canonicalOwner)
		canonicalName := strings.ToLower(name)
		var pushedAt *string
		if repo.PushedAt != nil {
			formatted := repo.PushedAt.Time.UTC().Format(time.RFC3339)
			pushedAt = &formatted
		}
		key := canonicalOwner + "/" + canonicalName
		_, already := exactConfigured[key]
		rows = append(rows, repoPreviewRow{
			Owner:             canonicalOwner,
			Name:              canonicalName,
			Description:       repo.Description,
			Private:           repo.GetPrivate(),
			PushedAt:          pushedAt,
			AlreadyConfigured: already,
		})
	}
	return rows, nil
}
```

Add `time` to imports. The helper intentionally does not sort rows; frontend owns sorting.

- [ ] **Step 5: Add preview handler**

In `repo_import_handlers.go`, add:

```go
func (s *Server) handlePreviewRepos(w http.ResponseWriter, r *http.Request) {
	if s.cfgPath == "" {
		writeError(w, http.StatusNotFound, "settings not available")
		return
	}

	var body repoPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	owner, pattern, err := normalizeImportOwnerPattern(body.Owner, body.Pattern)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	client, err := s.syncer.ClientForHost("github.com")
	if err != nil {
		writeError(w, http.StatusBadGateway, "GitHub API error: "+err.Error())
		return
	}

	s.cfgMu.Lock()
	repos := append([]config.Repo(nil), s.cfg.Repos...)
	s.cfgMu.Unlock()

	rows, err := buildRepoPreviewRows(r.Context(), client, exactConfiguredRepoSet(repos), owner, pattern)
	if err != nil {
		writeError(w, http.StatusBadGateway, "GitHub API error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, repoPreviewResponse{
		Owner:   owner,
		Pattern: pattern,
		Repos:   rows,
	})
}
```

- [ ] **Step 6: Add bulk validation and apply helpers**

In `repo_import_handlers.go`, add:

```go
func validateBulkExactRepos(
	ctx context.Context,
	clients map[string]ghclient.Client,
	candidates []config.Repo,
) ([]resolvedBulkRepo, error) {
	seenInput := make(map[string]struct{}, len(candidates))
	seenResolved := make(map[string]struct{}, len(candidates))
	resolved := make([]resolvedBulkRepo, 0, len(candidates))
	for _, candidate := range candidates {
		key := strings.ToLower(candidate.Owner) + "/" + strings.ToLower(candidate.Name)
		if _, ok := seenInput[key]; ok {
			continue
		}
		seenInput[key] = struct{}{}

		_, refs, err := ghclient.ResolveConfiguredRepo(ctx, clients, candidate)
		if err != nil {
			return nil, err
		}
		if len(refs) != 1 {
			return nil, fmt.Errorf("resolve exact repo %s/%s returned %d matches", candidate.Owner, candidate.Name, len(refs))
		}
		ref := refs[0]
		resolvedKey := strings.ToLower(ref.Owner) + "/" + strings.ToLower(ref.Name)
		if _, ok := seenResolved[resolvedKey]; ok {
			continue
		}
		seenResolved[resolvedKey] = struct{}{}
		resolved = append(resolved, resolvedBulkRepo{
			Config: config.Repo{Owner: ref.Owner, Name: ref.Name},
			Ref:    ref,
		})
	}
	return resolved, nil
}

func (s *Server) applyBulkExactRepos(resolved []resolvedBulkRepo) (settingsResponse, int, error) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	existing := exactConfiguredRepoSet(s.cfg.Repos)
	addConfigs := make([]config.Repo, 0, len(resolved))
	addRefs := make([]ghclient.RepoRef, 0, len(resolved))
	for _, repo := range resolved {
		key := strings.ToLower(repo.Config.Owner) + "/" + strings.ToLower(repo.Config.Name)
		if _, ok := existing[key]; ok {
			continue
		}
		existing[key] = struct{}{}
		addConfigs = append(addConfigs, repo.Config)
		addRefs = append(addRefs, repo.Ref)
	}
	if len(addConfigs) == 0 {
		return settingsResponse{}, http.StatusBadRequest, fmt.Errorf("all selected repositories are already configured")
	}

	prev := append([]config.Repo(nil), s.cfg.Repos...)
	s.cfg.Repos = append(s.cfg.Repos, addConfigs...)
	if err := s.cfg.Validate(); err != nil {
		s.cfg.Repos = prev
		return settingsResponse{}, http.StatusBadRequest, err
	}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.Repos = prev
		return settingsResponse{}, http.StatusInternalServerError, fmt.Errorf("save config: %w", err)
	}
	s.mergeTrackedRepos(addRefs)
	return s.buildLocalSettingsResponse(), http.StatusCreated, nil
}
```

- [ ] **Step 7: Add bulk handler**

In `repo_import_handlers.go`, add:

```go
func (s *Server) handleBulkAddRepos(w http.ResponseWriter, r *http.Request) {
	if s.cfgPath == "" {
		writeError(w, http.StatusNotFound, "settings not available")
		return
	}

	var body bulkAddReposRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(body.Repos) == 0 {
		writeError(w, http.StatusBadRequest, "repos are required")
		return
	}

	candidates := make([]config.Repo, 0, len(body.Repos))
	for _, raw := range body.Repos {
		repo, err := normalizeExactRepoInput(raw.Owner, raw.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		candidates = append(candidates, repo)
	}

	clients := s.configuredClients(append([]config.Repo(nil), candidates...))
	if _, ok := clients["github.com"]; !ok {
		client, err := s.syncer.ClientForHost("github.com")
		if err != nil {
			writeError(w, http.StatusBadGateway, "GitHub API error: "+err.Error())
			return
		}
		clients["github.com"] = client
	}
	resolved, err := validateBulkExactRepos(r.Context(), clients, candidates)
	if err != nil {
		status, msg := classifyResolveError(err)
		writeError(w, status, msg)
		return
	}
	resp, status, err := s.applyBulkExactRepos(resolved)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}

	s.syncer.TriggerRun(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusCreated, resp)
}
```

- [ ] **Step 8: Run backend tests**

Run:

```bash
go test ./internal/server -run 'TestHandlePreviewRepos|TestHandleBulkAddRepos' -shuffle=on
```

Expected: PASS.

- [ ] **Step 9: Run broader settings tests**

Run:

```bash
go test ./internal/server -run 'TestHandle.*Settings|TestHandle.*Repo|TestGlob|TestAddRepo|TestConcurrentRefreshAndDelete' -shuffle=on
```

Expected: PASS.

- [ ] **Step 10: Commit backend work**

Run:

```bash
git add internal/server/repo_import_handlers.go internal/server/server.go internal/server/settings_test.go
git commit -m "feat: add repository import API"
```

---

### Task 3: Frontend API helpers and pure selection helper

**TDD scenario:** New feature — full TDD cycle for TypeScript helpers.

**Files:**
- Modify: `frontend/src/lib/api/settings.ts`
- Modify: `frontend/src/lib/api/settings.test.ts`
- Create: `frontend/src/lib/components/settings/repoImportSelection.ts`
- Create: `frontend/src/lib/components/settings/repoImportSelection.test.ts`

- [ ] **Step 1: Write API helper tests**

Add these cases to `frontend/src/lib/api/settings.test.ts`:

```ts
import { beforeEach, describe, expect, it, vi } from "vitest";
import { bulkAddRepos, previewRepos, removeRepo } from "./settings.js";

describe("settings api", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue({ repos: [] }),
      text: vi.fn().mockResolvedValue(""),
    }));
  });

  it("encodes repo names for delete requests", async () => {
    await removeRepo("acme", "widgets-?");

    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/repos/acme/widgets-%3F",
      { method: "DELETE", headers: { "Content-Type": "application/json" } },
    );
  });

  it("posts preview requests", async () => {
    await previewRepos("acme", "widget-*");

    expect(fetch).toHaveBeenCalledWith("/api/v1/repos/preview", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ owner: "acme", pattern: "widget-*" }),
    });
  });

  it("posts bulk add requests", async () => {
    await bulkAddRepos([{ owner: "acme", name: "api" }]);

    expect(fetch).toHaveBeenCalledWith("/api/v1/repos/bulk", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ repos: [{ owner: "acme", name: "api" }] }),
    });
  });

  it("uses json error envelope when present", async () => {
    vi.mocked(fetch).mockResolvedValueOnce({
      ok: false,
      status: 400,
      json: vi.fn().mockResolvedValue({ error: "invalid glob pattern" }),
      text: vi.fn(),
    } as unknown as Response);

    await expect(previewRepos("acme", "[")).rejects.toThrow("invalid glob pattern");
  });
});
```

If duplicate imports result from existing file contents, merge imports into one line.

- [ ] **Step 2: Write selection helper tests**

Create `frontend/src/lib/components/settings/repoImportSelection.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import {
  applyRangeSelection,
  filterRows,
  parseImportPattern,
  rowKey,
  selectedRowsForSubmit,
  setAllVisible,
  sortRows,
  type RepoImportRow,
} from "./repoImportSelection.js";

const rows: RepoImportRow[] = [
  { owner: "acme", name: "worker", description: "Background jobs", private: false, pushed_at: "2026-04-20T00:00:00Z", already_configured: false },
  { owner: "acme", name: "api", description: "HTTP API", private: true, pushed_at: "2026-04-22T00:00:00Z", already_configured: false },
  { owner: "acme", name: "empty", description: null, private: false, pushed_at: null, already_configured: false },
  { owner: "acme", name: "widget", description: "Configured", private: false, pushed_at: "2026-04-21T00:00:00Z", already_configured: true },
];

describe("repo import selection helpers", () => {
  it("parses owner/pattern and trims whitespace", () => {
    expect(parseImportPattern(" acme / widget-* ")).toEqual({ owner: "acme", pattern: "widget-*" });
  });

  it("rejects malformed patterns before the API call", () => {
    expect(() => parseImportPattern("acme/widgets/extra")).toThrow("Format: owner/pattern");
    expect(() => parseImportPattern("acme*/widgets")).toThrow("glob syntax in owner is not supported");
    expect(() => parseImportPattern("acme/")).toThrow("pattern is required");
  });

  it("filters by owner/name, name, description, and status", () => {
    expect(filterRows(rows, "HTTP", "all").map((row) => row.name)).toEqual(["api"]);
    expect(filterRows(rows, "acme/worker", "all").map((row) => row.name)).toEqual(["worker"]);
    expect(filterRows(rows, "", "already-added").map((row) => row.name)).toEqual(["widget"]);
    const selected = new Set([rowKey(rows[0])]);
    expect(filterRows(rows, "", "selected", selected).map((row) => row.name)).toEqual(["worker"]);
    expect(filterRows(rows, "", "unselected", selected).map((row) => row.name)).toEqual(["api", "empty"]);
  });

  it("sorts deterministically with null pushed_at last", () => {
    expect(sortRows(rows, { field: "pushed_at", direction: "desc" }).map((row) => row.name)).toEqual(["api", "widget", "worker", "empty"]);
    expect(sortRows(rows, { field: "pushed_at", direction: "asc" }).map((row) => row.name)).toEqual(["worker", "widget", "api", "empty"]);
    expect(sortRows(rows, { field: "name", direction: "asc" }).map((row) => row.name)).toEqual(["api", "empty", "widget", "worker"]);
  });

  it("selects and deselects all visible selectable rows", () => {
    const selected = setAllVisible(new Set<string>(), rows, true);
    expect([...selected].sort()).toEqual(["acme/api", "acme/empty", "acme/worker"]);
    expect([...setAllVisible(selected, [rows[0], rows[3]], false)].sort()).toEqual(["acme/api", "acme/empty"]);
  });

  it("applies shift-click ranges with visible anchors", () => {
    const visible = sortRows(rows, { field: "name", direction: "asc" });
    const result = applyRangeSelection({
      selected: new Set<string>(),
      visibleRows: visible,
      anchorKey: "acme/api",
      clickedKey: "acme/worker",
      checked: true,
    });
    expect([...result.selected].sort()).toEqual(["acme/api", "acme/empty", "acme/worker"]);
    expect(result.anchorKey).toBe("acme/worker");
  });

  it("treats hidden anchors as normal clicks", () => {
    const result = applyRangeSelection({
      selected: new Set<string>(),
      visibleRows: [rows[1]],
      anchorKey: "acme/worker",
      clickedKey: "acme/api",
      checked: true,
    });
    expect([...result.selected]).toEqual(["acme/api"]);
    expect(result.anchorKey).toBe("acme/api");
  });

  it("returns selected rows for submit in full sorted order", () => {
    const sorted = sortRows(rows, { field: "name", direction: "asc" });
    const selected = new Set(["acme/worker", "acme/api"]);
    expect(selectedRowsForSubmit(sorted, selected).map((row) => row.name)).toEqual(["api", "worker"]);
  });
});
```

- [ ] **Step 3: Run frontend helper tests and verify failure**

Run:

```bash
cd frontend && bun run test src/lib/api/settings.test.ts src/lib/components/settings/repoImportSelection.test.ts
```

Expected: FAIL because new exports do not exist.

- [ ] **Step 4: Implement API helpers**

Modify `frontend/src/lib/api/settings.ts`:

```ts
export interface RepoPreviewRow {
  owner: string;
  name: string;
  description: string | null;
  private: boolean;
  pushed_at: string | null;
  already_configured: boolean;
}

export interface RepoPreviewResponse {
  owner: string;
  pattern: string;
  repos: RepoPreviewRow[];
}

interface RepoInput {
  owner: string;
  name: string;
}

async function errorFromResponse(res: Response, fallback: string): Promise<Error> {
  try {
    const data = await res.json() as { error?: string };
    if (data.error) return new Error(data.error);
  } catch {
    // Fall through to text fallback.
  }
  const text = await res.text().catch(() => res.statusText);
  return new Error(text || fallback);
}
```

Update existing `addRepo`, `removeRepo`, and `refreshRepo` to use `errorFromResponse` for consistency. Then add:

```ts
export async function previewRepos(
  owner: string,
  pattern: string,
): Promise<RepoPreviewResponse> {
  const res = await fetch(`${BASE}/repos/preview`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ owner, pattern }),
  });
  if (!res.ok) throw await errorFromResponse(res, `POST /repos/preview → ${res.status}`);
  return res.json() as Promise<RepoPreviewResponse>;
}

export async function bulkAddRepos(repos: RepoInput[]): Promise<Settings> {
  const res = await fetch(`${BASE}/repos/bulk`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ repos }),
  });
  if (!res.ok) throw await errorFromResponse(res, `POST /repos/bulk → ${res.status}`);
  return res.json() as Promise<Settings>;
}
```

- [ ] **Step 5: Implement selection helper**

Create `frontend/src/lib/components/settings/repoImportSelection.ts`:

```ts
export type SortField = "name" | "pushed_at";
export type SortDirection = "asc" | "desc";
export type StatusFilter = "all" | "selected" | "unselected" | "already-added";

export interface RepoImportRow {
  owner: string;
  name: string;
  description: string | null;
  private: boolean;
  pushed_at: string | null;
  already_configured: boolean;
}

export interface SortState {
  field: SortField;
  direction: SortDirection;
}

export function rowKey(row: Pick<RepoImportRow, "owner" | "name">): string {
  return `${row.owner.toLowerCase()}/${row.name.toLowerCase()}`;
}

export function parseImportPattern(input: string): { owner: string; pattern: string } {
  const trimmed = input.trim();
  const slashCount = [...trimmed].filter((char) => char === "/").length;
  if (slashCount !== 1) throw new Error("Format: owner/pattern");
  const [rawOwner, rawPattern] = trimmed.split("/");
  const owner = rawOwner.trim();
  const pattern = rawPattern.trim();
  if (!owner) throw new Error("owner is required");
  if (!pattern) throw new Error("pattern is required");
  if (/[*?[\]]/.test(owner)) throw new Error("glob syntax in owner is not supported");
  if (pattern.includes("/")) throw new Error("pattern must not contain /");
  return { owner, pattern };
}

export function filterRows(
  rows: RepoImportRow[],
  query: string,
  status: StatusFilter,
  selected = new Set<string>(),
): RepoImportRow[] {
  const needle = query.trim().toLowerCase();
  return rows.filter((row) => {
    const key = rowKey(row);
    const matchesText = needle === "" ||
      key.includes(needle) ||
      row.name.toLowerCase().includes(needle) ||
      (row.description ?? "").toLowerCase().includes(needle);
    if (!matchesText) return false;
    if (status === "selected") return selected.has(key);
    if (status === "unselected") return !row.already_configured && !selected.has(key);
    if (status === "already-added") return row.already_configured;
    return true;
  });
}

export function sortRows(rows: RepoImportRow[], sort: SortState): RepoImportRow[] {
  return rows
    .map((row, index) => ({ row, index }))
    .sort((left, right) => {
      let cmp = 0;
      if (sort.field === "name") {
        cmp = rowKey(left.row).localeCompare(rowKey(right.row));
      } else {
        const leftTime = left.row.pushed_at ? Date.parse(left.row.pushed_at) : null;
        const rightTime = right.row.pushed_at ? Date.parse(right.row.pushed_at) : null;
        if (leftTime === null && rightTime === null) cmp = 0;
        else if (leftTime === null) cmp = 1;
        else if (rightTime === null) cmp = -1;
        else cmp = leftTime - rightTime;
      }
      if (sort.direction === "desc" && sort.field !== "pushed_at") cmp = -cmp;
      if (sort.field === "pushed_at" && sort.direction === "desc" && cmp !== 0) cmp = -cmp;
      if (cmp !== 0) return cmp;
      const keyCmp = rowKey(left.row).localeCompare(rowKey(right.row));
      if (keyCmp !== 0) return keyCmp;
      return left.index - right.index;
    })
    .map(({ row }) => row);
}

export function setAllVisible(
  selected: Set<string>,
  visibleRows: RepoImportRow[],
  checked: boolean,
): Set<string> {
  const next = new Set(selected);
  for (const row of visibleRows) {
    if (row.already_configured) continue;
    const key = rowKey(row);
    if (checked) next.add(key);
    else next.delete(key);
  }
  return next;
}

export function applyRangeSelection(input: {
  selected: Set<string>;
  visibleRows: RepoImportRow[];
  anchorKey: string | null;
  clickedKey: string;
  checked: boolean;
}): { selected: Set<string>; anchorKey: string } {
  const next = new Set(input.selected);
  const clickedIndex = input.visibleRows.findIndex((row) => rowKey(row) === input.clickedKey);
  const anchorIndex = input.anchorKey
    ? input.visibleRows.findIndex((row) => rowKey(row) === input.anchorKey)
    : -1;
  if (clickedIndex === -1) return { selected: next, anchorKey: input.clickedKey };
  const start = anchorIndex === -1 ? clickedIndex : Math.min(anchorIndex, clickedIndex);
  const end = anchorIndex === -1 ? clickedIndex : Math.max(anchorIndex, clickedIndex);
  for (const row of input.visibleRows.slice(start, end + 1)) {
    if (row.already_configured) continue;
    const key = rowKey(row);
    if (input.checked) next.add(key);
    else next.delete(key);
  }
  return { selected: next, anchorKey: input.clickedKey };
}

export function selectedRowsForSubmit(
  sortedRows: RepoImportRow[],
  selected: Set<string>,
): RepoImportRow[] {
  return sortedRows.filter((row) => selected.has(rowKey(row)) && !row.already_configured);
}
```

- [ ] **Step 6: Run helper tests**

Run:

```bash
cd frontend && bun run test src/lib/api/settings.test.ts src/lib/components/settings/repoImportSelection.test.ts
```

Expected: PASS.

- [ ] **Step 7: Commit helper work**

Run:

```bash
git add frontend/src/lib/api/settings.ts frontend/src/lib/api/settings.test.ts frontend/src/lib/components/settings/repoImportSelection.ts frontend/src/lib/components/settings/repoImportSelection.test.ts
git commit -m "feat: add repository import selection helpers"
```

---

### Task 4: Repository import modal and settings integration

**TDD scenario:** New UI feature — component tests first, then Svelte implementation. Use Svelte 5 runes guidance from `svelte-core-bestpractices` and run `svelte-autofixer` before committing.

**Files:**
- Create: `frontend/src/lib/components/settings/RepoPreviewTable.svelte`
- Create: `frontend/src/lib/components/settings/RepoImportModal.svelte`
- Create: `frontend/src/lib/components/settings/RepoImportModal.test.ts`
- Modify: `frontend/src/lib/components/settings/RepoSettings.svelte`
- Modify: `frontend/src/lib/components/settings/RepoSettings.test.ts`

- [ ] **Step 1: Extend API mocks in tests**

Update `frontend/src/lib/components/settings/RepoSettings.test.ts` mock block:

```ts
vi.mock("../../api/settings.js", () => ({
  addRepo: vi.fn(),
  removeRepo: vi.fn(),
  getSettings: vi.fn(),
  refreshRepo: vi.fn(),
  previewRepos: vi.fn(),
  bulkAddRepos: vi.fn(),
}));
```

- [ ] **Step 2: Add modal component tests**

Create `frontend/src/lib/components/settings/RepoImportModal.test.ts`:

```ts
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import RepoImportModal from "./RepoImportModal.svelte";
import { bulkAddRepos, previewRepos } from "../../api/settings.js";

vi.mock("../../api/settings.js", () => ({
  previewRepos: vi.fn(),
  bulkAddRepos: vi.fn(),
}));

const preview = vi.mocked(previewRepos);
const bulk = vi.mocked(bulkAddRepos);

const rows = [
  { owner: "acme", name: "worker", description: "Background jobs", private: false, pushed_at: "2026-04-20T00:00:00Z", already_configured: false },
  { owner: "acme", name: "api", description: "HTTP API", private: true, pushed_at: "2026-04-22T00:00:00Z", already_configured: false },
  { owner: "acme", name: "widget", description: "Configured", private: false, pushed_at: "2026-04-21T00:00:00Z", already_configured: true },
];

describe("RepoImportModal", () => {
  afterEach(() => {
    cleanup();
    preview.mockReset();
    bulk.mockReset();
  });

  it("previews rows and defaults selectable rows to selected", async () => {
    preview.mockResolvedValue({ owner: "acme", pattern: "*", repos: rows });
    render(RepoImportModal, { props: { open: true, onClose: vi.fn(), onImported: vi.fn() } });

    await fireEvent.input(screen.getByLabelText("Repository pattern"), { target: { value: "acme/*" } });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));

    await screen.findByText("acme/api");
    expect(screen.getByText("Selected 2 of 3")).toBeTruthy();
    expect(screen.getByRole("checkbox", { name: "Select acme/widget" })).toBeDisabled();
    expect(screen.getByText("Already added")).toBeTruthy();
  });

  it("filters, deselects visible rows, and submits remaining selected rows", async () => {
    const onImported = vi.fn();
    preview.mockResolvedValue({ owner: "acme", pattern: "*", repos: rows });
    bulk.mockResolvedValue({ repos: [], activity: { view_mode: "threaded", time_range: "7d", hide_closed: false, hide_bots: false }, terminal: { font_family: "" } });
    render(RepoImportModal, { props: { open: true, onClose: vi.fn(), onImported } });

    await fireEvent.input(screen.getByLabelText("Repository pattern"), { target: { value: "acme/*" } });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByText("acme/api");

    await fireEvent.input(screen.getByLabelText("Filter repositories"), { target: { value: "worker" } });
    await fireEvent.click(screen.getByRole("button", { name: "None" }));
    await fireEvent.click(screen.getByRole("button", { name: "Add selected repositories" }));

    await waitFor(() => expect(bulk).toHaveBeenCalledWith([{ owner: "acme", name: "api" }]));
    expect(onImported).toHaveBeenCalled();
  });

  it("ignores stale preview responses after input changes", async () => {
    let resolveFirst: (value: Awaited<ReturnType<typeof previewRepos>>) => void = () => {};
    preview.mockReturnValueOnce(new Promise((resolve) => { resolveFirst = resolve; }));
    render(RepoImportModal, { props: { open: true, onClose: vi.fn(), onImported: vi.fn() } });

    await fireEvent.input(screen.getByLabelText("Repository pattern"), { target: { value: "acme/*" } });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await fireEvent.input(screen.getByLabelText("Repository pattern"), { target: { value: "acme/api-*" } });
    resolveFirst({ owner: "acme", pattern: "*", repos: rows });

    await waitFor(() => expect(screen.queryByText("acme/api")).toBeNull());
    expect(screen.getByText("Preview repositories before adding them."));
  });

  it("clears stale rows on failed preview", async () => {
    preview.mockResolvedValueOnce({ owner: "acme", pattern: "*", repos: rows });
    preview.mockRejectedValueOnce(new Error("GitHub API error: boom"));
    render(RepoImportModal, { props: { open: true, onClose: vi.fn(), onImported: vi.fn() } });

    await fireEvent.input(screen.getByLabelText("Repository pattern"), { target: { value: "acme/*" } });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByText("acme/api");
    await fireEvent.input(screen.getByLabelText("Repository pattern"), { target: { value: "acme/worker*" } });
    await fireEvent.click(screen.getByRole("button", { name: "Preview" }));

    await screen.findByText("GitHub API error: boom");
    expect(screen.queryByText("acme/api")).toBeNull();
  });
});
```

- [ ] **Step 3: Add RepoSettings integration tests**

Append to `RepoSettings.test.ts`:

```ts
it("opens the repository import modal", async () => {
  render(RepoSettings, {
    props: {
      repos: [],
      onUpdate: vi.fn(),
    },
  });

  await fireEvent.click(screen.getByRole("button", { name: "Add repositories…" }));

  expect(screen.getByRole("dialog", { name: "Add repositories" })).toBeTruthy();
  expect(screen.getByLabelText("Repository pattern")).toBeTruthy();
});

it("keeps direct glob add in an advanced section", () => {
  render(RepoSettings, {
    props: {
      repos: [],
      onUpdate: vi.fn(),
    },
  });

  expect(screen.getByText("Advanced: add exact repo or tracking glob directly")).toBeTruthy();
  expect(screen.queryByPlaceholderText("owner/name")).toBeNull();
});
```

Add `fireEvent` import from `@testing-library/svelte`.

- [ ] **Step 4: Run component tests and verify failure**

Run:

```bash
cd frontend && bun run test src/lib/components/settings/RepoSettings.test.ts src/lib/components/settings/RepoImportModal.test.ts
```

Expected: FAIL because components are missing or UI not implemented.

- [ ] **Step 5: Implement presentational table**

Create `frontend/src/lib/components/settings/RepoPreviewTable.svelte`:

```svelte
<script lang="ts">
  import { Chip } from "@middleman/ui";
  import type { RepoImportRow, SortState, StatusFilter } from "./repoImportSelection.js";
  import { rowKey } from "./repoImportSelection.js";

  interface Props {
    rows: RepoImportRow[];
    selected: Set<string>;
    filterText: string;
    statusFilter: StatusFilter;
    sort: SortState;
    onFilterText: (value: string) => void;
    onStatusFilter: (value: StatusFilter) => void;
    onSort: (field: SortState["field"]) => void;
    onToggle: (row: RepoImportRow, checked: boolean, shiftKey: boolean) => void;
    onSelectVisible: () => void;
    onDeselectVisible: () => void;
  }

  let {
    rows,
    selected,
    filterText,
    statusFilter,
    sort,
    onFilterText,
    onStatusFilter,
    onSort,
    onToggle,
    onSelectVisible,
    onDeselectVisible,
  }: Props = $props();

  function sortLabel(field: SortState["field"]): string {
    if (sort.field !== field) return "";
    return sort.direction === "asc" ? " ↑" : " ↓";
  }

  function formatPushedAt(value: string | null): string {
    if (!value) return "Never pushed";
    return new Date(value).toLocaleString();
  }
</script>

<div class="repo-preview-controls">
  <input
    class="filter-input"
    type="text"
    aria-label="Filter repositories"
    placeholder="Filter by name or description…"
    value={filterText}
    oninput={(event) => onFilterText(event.currentTarget.value)}
  />
  <select
    aria-label="Repository selection filter"
    value={statusFilter}
    onchange={(event) => onStatusFilter(event.currentTarget.value as StatusFilter)}
  >
    <option value="all">All rows</option>
    <option value="selected">Selected</option>
    <option value="unselected">Unselected</option>
    <option value="already-added">Already added</option>
  </select>
  <button type="button" class="shortcut-btn" onclick={onSelectVisible}>All</button>
  <button type="button" class="shortcut-btn" onclick={onDeselectVisible}>None</button>
</div>

<div class="table-wrap">
  <table class="repo-preview-table">
    <thead>
      <tr>
        <th scope="col" class="select-col">Select</th>
        <th scope="col"><button type="button" class="sort-btn" onclick={() => onSort("name")}>Repository{sortLabel("name")}</button></th>
        <th scope="col">Description</th>
        <th scope="col"><button type="button" class="sort-btn" onclick={() => onSort("pushed_at")}>Last pushed{sortLabel("pushed_at")}</button></th>
        <th scope="col">Visibility</th>
        <th scope="col">Status</th>
      </tr>
    </thead>
    <tbody>
      {#each rows as row (rowKey(row))}
        {@const key = rowKey(row)}
        <tr class:disabled-row={row.already_configured}>
          <td>
            <input
              type="checkbox"
              aria-label={`Select ${row.owner}/${row.name}`}
              checked={selected.has(key)}
              disabled={row.already_configured}
              onchange={(event) => onToggle(row, event.currentTarget.checked, event.shiftKey)}
            />
          </td>
          <td class="repo-name">{row.owner}/{row.name}</td>
          <td class="description">{row.description ?? ""}</td>
          <td>{formatPushedAt(row.pushed_at)}</td>
          <td><Chip size="sm" class="chip--muted">{row.private ? "Private" : "Public"}</Chip></td>
          <td>{#if row.already_configured}<Chip size="sm" class="chip--amber">Already added</Chip>{/if}</td>
        </tr>
      {:else}
        <tr><td colspan="6" class="empty-cell">No repositories match current filters.</td></tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .repo-preview-controls { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .filter-input { flex: 1; min-width: 220px; font-size: 13px; padding: 6px 10px; background: var(--bg-inset); border: 1px solid var(--border-muted); border-radius: var(--radius-sm); }
  select { font-size: 13px; padding: 6px 8px; background: var(--bg-inset); color: var(--text-primary); border: 1px solid var(--border-muted); border-radius: var(--radius-sm); }
  .shortcut-btn, .sort-btn { font-size: 12px; color: var(--accent-blue); }
  .table-wrap { overflow: auto; border: 1px solid var(--border-muted); border-radius: var(--radius-md); }
  .repo-preview-table { width: 100%; border-collapse: collapse; font-size: 12px; }
  th, td { padding: 8px 10px; border-bottom: 1px solid var(--border-muted); text-align: left; vertical-align: middle; }
  th { color: var(--text-muted); font-weight: 600; background: var(--bg-inset); }
  tr:last-child td { border-bottom: none; }
  .select-col { width: 52px; }
  .repo-name { font-weight: 600; color: var(--text-primary); white-space: nowrap; }
  .description { color: var(--text-secondary); min-width: 180px; }
  .disabled-row { opacity: 0.72; }
  .empty-cell { text-align: center; color: var(--text-muted); padding: 24px; }
</style>
```

- [ ] **Step 6: Implement modal**

Create `frontend/src/lib/components/settings/RepoImportModal.svelte`:

```svelte
<script lang="ts">
  import { tick } from "svelte";
  import { ActionButton } from "@middleman/ui";
  import type { Settings } from "@middleman/ui/api/types";
  import { bulkAddRepos, previewRepos, type RepoPreviewRow } from "../../api/settings.js";
  import RepoPreviewTable from "./RepoPreviewTable.svelte";
  import {
    applyRangeSelection,
    filterRows,
    parseImportPattern,
    rowKey,
    selectedRowsForSubmit,
    setAllVisible,
    sortRows,
    type SortState,
    type StatusFilter,
  } from "./repoImportSelection.js";

  interface Props {
    open: boolean;
    onClose: () => void;
    onImported: (settings: Settings) => void;
  }

  let { open, onClose, onImported }: Props = $props();

  let patternInput = $state("");
  let rows = $state.raw<RepoPreviewRow[]>([]);
  let selected = $state<Set<string>>(new Set());
  let filterText = $state("");
  let statusFilter = $state<StatusFilter>("all");
  let sort = $state<SortState>({ field: "pushed_at", direction: "desc" });
  let anchorKey = $state<string | null>(null);
  let loading = $state(false);
  let submitting = $state(false);
  let error = $state<string | null>(null);
  let requestToken = 0;
  let inputEl = $state<HTMLInputElement | null>(null);

  const sortedRows = $derived(sortRows(rows, sort));
  const visibleRows = $derived(filterRows(sortedRows, filterText, statusFilter, selected));
  const selectedCount = $derived(rows.filter((row) => selected.has(rowKey(row)) && !row.already_configured).length);
  const selectableCount = $derived(rows.filter((row) => !row.already_configured).length);
  const submitRows = $derived(selectedRowsForSubmit(sortedRows, selected));

  $effect(() => {
    if (open) {
      void tick().then(() => inputEl?.focus());
    } else {
      resetAll();
    }
  });

  function resetPreviewState(): void {
    rows = [];
    selected = new Set();
    filterText = "";
    statusFilter = "all";
    sort = { field: "pushed_at", direction: "desc" };
    anchorKey = null;
  }

  function resetAll(): void {
    patternInput = "";
    resetPreviewState();
    error = null;
    loading = false;
    submitting = false;
    requestToken += 1;
  }

  function handlePatternInput(value: string): void {
    patternInput = value;
    requestToken += 1;
    resetPreviewState();
    error = null;
    loading = false;
  }

  async function handlePreview(): Promise<void> {
    let parsed: { owner: string; pattern: string };
    try {
      parsed = parseImportPattern(patternInput);
    } catch (err) {
      resetPreviewState();
      error = err instanceof Error ? err.message : String(err);
      return;
    }
    const token = ++requestToken;
    loading = true;
    error = null;
    resetPreviewState();
    try {
      const resp = await previewRepos(parsed.owner, parsed.pattern);
      if (token !== requestToken) return;
      rows = resp.repos;
      selected = new Set(resp.repos.filter((row) => !row.already_configured).map(rowKey));
    } catch (err) {
      if (token !== requestToken) return;
      resetPreviewState();
      error = err instanceof Error ? err.message : String(err);
    } finally {
      if (token === requestToken) loading = false;
    }
  }

  async function handleSubmit(): Promise<void> {
    if (submitRows.length === 0) return;
    submitting = true;
    error = null;
    try {
      const settings = await bulkAddRepos(submitRows.map((row) => ({ owner: row.owner, name: row.name })));
      onImported(settings);
      onClose();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      submitting = false;
    }
  }

  function toggleSort(field: SortState["field"]): void {
    sort = sort.field === field
      ? { field, direction: sort.direction === "asc" ? "desc" : "asc" }
      : { field, direction: field === "pushed_at" ? "desc" : "asc" };
  }

  function toggleRow(row: RepoPreviewRow, checked: boolean, shiftKey: boolean): void {
    const key = rowKey(row);
    if (shiftKey) {
      const result = applyRangeSelection({ selected, visibleRows, anchorKey, clickedKey: key, checked });
      selected = result.selected;
      anchorKey = result.anchorKey;
      return;
    }
    const next = new Set(selected);
    if (checked) next.add(key);
    else next.delete(key);
    selected = next;
    anchorKey = key;
  }

  function closeIfAllowed(): void {
    if (!submitting) onClose();
  }

  function handleKeydown(event: KeyboardEvent): void {
    if (event.key === "Escape") closeIfAllowed();
  }
</script>

{#if open}
  <div class="modal-backdrop" role="presentation" onkeydown={handleKeydown}>
    <section class="modal" role="dialog" aria-modal="true" aria-labelledby="repo-import-title">
      <header class="modal-header">
        <div>
          <h2 id="repo-import-title">Add repositories</h2>
          <p>Preview repositories before adding them.</p>
        </div>
        <button type="button" class="close-btn" aria-label="Close" onclick={closeIfAllowed}>×</button>
      </header>

      <div class="preview-form">
        <label>
          <span>Repository pattern</span>
          <input
            bind:this={inputEl}
            value={patternInput}
            placeholder="owner/pattern"
            oninput={(event) => handlePatternInput(event.currentTarget.value)}
            onkeydown={(event) => { if (event.key === "Enter") void handlePreview(); }}
          />
        </label>
        <ActionButton tone="info" surface="soft" onclick={() => void handlePreview()} disabled={loading || !patternInput.trim()}>
          {loading ? "Previewing…" : "Preview"}
        </ActionButton>
      </div>

      {#if error}
        <div class="error-msg">{error}</div>
      {/if}

      {#if rows.length > 0}
        <RepoPreviewTable
          rows={visibleRows}
          {selected}
          {filterText}
          {statusFilter}
          {sort}
          onFilterText={(value) => { filterText = value; }}
          onStatusFilter={(value) => { statusFilter = value; }}
          onSort={toggleSort}
          onToggle={toggleRow}
          onSelectVisible={() => { selected = setAllVisible(selected, visibleRows, true); }}
          onDeselectVisible={() => { selected = setAllVisible(selected, visibleRows, false); }}
        />
      {:else if !loading && !error}
        <div class="empty-preview">Preview repositories before adding them.</div>
      {/if}

      <footer class="modal-footer">
        <span>Selected {selectedCount} of {rows.length}</span>
        <div class="footer-actions">
          <ActionButton onclick={closeIfAllowed} disabled={submitting}>Cancel</ActionButton>
          <ActionButton tone="info" surface="soft" onclick={() => void handleSubmit()} disabled={submitting || selectedCount === 0}>
            {submitting ? "Adding…" : "Add selected repositories"}
          </ActionButton>
        </div>
      </footer>
    </section>
  </div>
{/if}

<style>
  .modal-backdrop { position: fixed; inset: 0; z-index: 40; display: flex; align-items: center; justify-content: center; padding: 24px; background: color-mix(in srgb, black 38%, transparent); }
  .modal { width: min(1040px, 100%); max-height: min(760px, 92vh); display: flex; flex-direction: column; gap: 14px; background: var(--bg-surface); color: var(--text-primary); border: 1px solid var(--border-default); border-radius: var(--radius-lg); box-shadow: 0 24px 80px rgb(0 0 0 / 35%); padding: 18px; }
  .modal-header, .modal-footer { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
  h2 { margin: 0; font-size: 16px; }
  p { margin: 4px 0 0; color: var(--text-muted); font-size: 12px; }
  .close-btn { color: var(--text-muted); font-size: 20px; }
  .preview-form { display: flex; gap: 10px; align-items: end; }
  label { flex: 1; display: flex; flex-direction: column; gap: 6px; font-size: 12px; color: var(--text-secondary); }
  input { font-size: 13px; padding: 7px 10px; color: var(--text-primary); background: var(--bg-inset); border: 1px solid var(--border-muted); border-radius: var(--radius-sm); }
  .error-msg { color: var(--accent-red); font-size: 12px; }
  .empty-preview { border: 1px dashed var(--border-muted); border-radius: var(--radius-md); padding: 28px; color: var(--text-muted); text-align: center; font-size: 13px; }
  .footer-actions { display: flex; gap: 8px; }
</style>
```

- [ ] **Step 7: Integrate modal into RepoSettings**

Modify `frontend/src/lib/components/settings/RepoSettings.svelte`:

1. Import modal:

```ts
import RepoImportModal from "./RepoImportModal.svelte";
```

2. Add state:

```ts
let importOpen = $state(false);
```

3. Add primary action and advanced details before the repo list:

```svelte
{#if !embedded}
  <div class="repo-import-entry">
    <button class="primary-import-btn" type="button" onclick={() => { importOpen = true; }}>Add repositories…</button>
    <p>Preview a glob, filter results, and add selected repositories as exact entries.</p>
  </div>
{/if}

<RepoImportModal
  open={importOpen}
  onClose={() => { importOpen = false; }}
  onImported={(settings) => {
    onUpdate(settings.repos);
    void sync.refreshSyncStatus();
  }}
/>
```

4. Wrap existing `.add-form` and `addError` block in:

```svelte
{#if !embedded}
  <details class="advanced-add">
    <summary>Advanced: add exact repo or tracking glob directly</summary>
    <div class="advanced-body">
      <div class="add-form">
        <input class="add-input" type="text" placeholder="owner/name" bind:value={inputValue} onkeydown={handleInputKeydown} disabled={adding} />
        <button class="add-btn" onclick={() => void handleAdd()} disabled={adding || !inputValue.trim()}>
          {adding ? "Adding..." : "Add"}
        </button>
      </div>

      {#if addError}
        <div class="error-msg">{addError}</div>
      {/if}
    </div>
  </details>
{/if}
```

5. Add compact CSS:

```css
.repo-import-entry { display: flex; flex-direction: column; gap: 4px; padding-bottom: 12px; border-bottom: 1px solid var(--border-muted); }
.primary-import-btn { align-self: flex-start; padding: 6px 14px; font-size: 13px; font-weight: 600; color: white; background: var(--accent-blue); border-radius: var(--radius-sm); }
.repo-import-entry p { margin: 0; color: var(--text-muted); font-size: 12px; }
.advanced-add { padding-top: 8px; }
.advanced-add summary { cursor: pointer; color: var(--text-secondary); font-size: 12px; }
.advanced-body { padding-top: 8px; display: flex; flex-direction: column; gap: 6px; }
```

- [ ] **Step 8: Run Svelte autofixer**

Run:

```bash
cd frontend && bunx --bun @sveltejs/mcp@0.1.22 svelte-autofixer ./src/lib/components/settings/RepoImportModal.svelte --svelte-version 5
cd frontend && bunx --bun @sveltejs/mcp@0.1.22 svelte-autofixer ./src/lib/components/settings/RepoPreviewTable.svelte --svelte-version 5
cd frontend && bunx --bun @sveltejs/mcp@0.1.22 svelte-autofixer ./src/lib/components/settings/RepoSettings.svelte --svelte-version 5
```

Expected: no required fixes remain. Apply any concrete fixes suggested by the tool.

- [ ] **Step 9: Run component tests**

Run:

```bash
cd frontend && bun run test src/lib/components/settings/RepoSettings.test.ts src/lib/components/settings/RepoImportModal.test.ts src/lib/components/settings/repoImportSelection.test.ts
```

Expected: PASS.

- [ ] **Step 10: Run Svelte typecheck**

Run:

```bash
cd frontend && bun run check
```

Expected: PASS.

- [ ] **Step 11: Commit UI work**

Run:

```bash
git add frontend/src/lib/components/settings/RepoPreviewTable.svelte frontend/src/lib/components/settings/RepoImportModal.svelte frontend/src/lib/components/settings/RepoImportModal.test.ts frontend/src/lib/components/settings/RepoSettings.svelte frontend/src/lib/components/settings/RepoSettings.test.ts
git commit -m "feat: add repository import modal"
```

---

### Task 5: Full-stack e2e support and tests

**TDD scenario:** Full-stack e2e for user-visible workflow.

**Files:**
- Modify: `cmd/e2e-server/main.go`
- Modify: `frontend/tests/e2e-full/settings-globs.spec.ts`

- [ ] **Step 1: Add richer e2e repository metadata**

In `cmd/e2e-server/main.go`, inside `fc.ListRepositoriesByOwnerFn` for `owner == "roborev-dev"`, add descriptions, privacy, and pushed times:

```go
pushedMiddleman := gh.Timestamp{Time: time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)}
pushedWorker := gh.Timestamp{Time: time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)}
pushedBot := gh.Timestamp{Time: time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)}
privateFalse := false
repos := []*gh.Repository{
	{
		Name:        new("middleman"),
		Owner:       &gh.User{Login: new(owner)},
		Description: new("Main dashboard"),
		Private:     &privateFalse,
		Archived:    new(false),
		PushedAt:    &pushedMiddleman,
	},
	{
		Name:        new("worker"),
		Owner:       &gh.User{Login: new(owner)},
		Description: new("Background jobs"),
		Private:     &privateFalse,
		Archived:    new(false),
		PushedAt:    &pushedWorker,
	},
	{
		Name:     new("archived"),
		Owner:    &gh.User{Login: new(owner)},
		Archived: new(true),
		PushedAt: &pushedBot,
	},
}
```

When appending `review-bot`, include:

```go
Description: new("Review automation"),
Private:     &privateFalse,
PushedAt:    &pushedBot,
```

- [ ] **Step 2: Add e2e import flow**

Append to `frontend/tests/e2e-full/settings-globs.spec.ts`:

```ts
test("settings imports a selected subset from a repository glob", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  await page.getByRole("button", { name: "Add repositories…" }).click();
  await expect(page.getByRole("dialog", { name: "Add repositories" })).toBeVisible();
  await page.getByLabel("Repository pattern").fill("roborev-dev/*");
  await page.getByRole("button", { name: "Preview" }).click();

  await expect(page.getByText("roborev-dev/middleman")).toBeVisible();
  await expect(page.getByText("roborev-dev/worker")).toBeVisible();
  await expect(page.getByText("roborev-dev/archived")).toHaveCount(0);

  await page.getByLabel("Filter repositories").fill("worker");
  await page.getByRole("button", { name: "None" }).click();
  await page.getByLabel("Filter repositories").fill("");
  await page.getByRole("button", { name: "Add selected repositories" }).click();

  await expect(page.getByRole("dialog", { name: "Add repositories" })).toHaveCount(0);
  await expect(page.locator(".repo-row", { hasText: "roborev-dev/middleman" })).toBeVisible();
  await expect(page.locator(".repo-row", { hasText: "roborev-dev/worker" })).toBeVisible();

  if (!api) throw new Error("settings-globs API context not initialized");
  const response = await api.get("/api/v1/settings");
  const settings = await response.json() as { repos: Array<{ owner: string; name: string; is_glob: boolean }> };
  const exactNames = settings.repos
    .filter((repo) => repo.owner === "roborev-dev" && !repo.is_glob)
    .map((repo) => repo.name)
    .sort();
  expect(exactNames).toEqual(["middleman"]);
});
```

- [ ] **Step 3: Add e2e stale-preview test**

Append another test to `settings-globs.spec.ts`:

```ts
test("repository import clears stale preview results after failed preview", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  await page.getByRole("button", { name: "Add repositories…" }).click();
  await page.getByLabel("Repository pattern").fill("roborev-dev/*");
  await page.getByRole("button", { name: "Preview" }).click();
  await expect(page.getByText("roborev-dev/middleman")).toBeVisible();

  await page.getByLabel("Repository pattern").fill("bad-owner/[invalid");
  await page.getByRole("button", { name: "Preview" }).click();
  await expect(page.getByText(/invalid glob pattern|GitHub API error|glob syntax/)).toBeVisible();
  await expect(page.getByText("roborev-dev/middleman")).toHaveCount(0);
});

test("repository import ignores older preview responses", async ({ page }) => {
  let firstPreviewRelease: (() => void) | undefined;
  let previewCalls = 0;
  await page.route("**/api/v1/repos/preview", async (route) => {
    previewCalls += 1;
    if (previewCalls === 1) {
      await new Promise<void>((resolve) => { firstPreviewRelease = resolve; });
    }
    const response = await route.fetch();
    await route.fulfill({ response });
  });

  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
  await page.getByRole("button", { name: "Add repositories…" }).click();

  await page.getByLabel("Repository pattern").fill("roborev-dev/*");
  await page.getByRole("button", { name: "Preview" }).click();
  await expect.poll(() => previewCalls).toBe(1);

  await page.getByLabel("Repository pattern").fill("roborev-dev/review-*");
  await page.getByRole("button", { name: "Preview" }).click();
  await expect.poll(() => previewCalls).toBe(2);
  await expect(page.getByText("No repositories match current filters.")).toBeVisible();

  firstPreviewRelease?.();
  await expect(page.getByText("roborev-dev/middleman")).toHaveCount(0);
});
```

- [ ] **Step 4: Run targeted e2e tests**

Run:

```bash
make frontend
cd frontend && bun run playwright test --config=playwright-e2e.config.ts --project=chromium settings-globs.spec.ts
```

Expected: PASS.

- [ ] **Step 5: Commit e2e work**

Run:

```bash
git add cmd/e2e-server/main.go frontend/tests/e2e-full/settings-globs.spec.ts
git commit -m "test: cover repository import workflow"
```

---

### Task 6: Final verification and cleanup

**TDD scenario:** Verification before completion.

**Files:**
- Review all modified files.
- No planned source changes unless tests reveal issues.

- [ ] **Step 1: Run Go server tests**

Run:

```bash
go test ./internal/server -shuffle=on
```

Expected: PASS.

- [ ] **Step 2: Run frontend tests for touched area**

Run:

```bash
cd frontend && bun run test src/lib/api/settings.test.ts src/lib/components/settings/RepoSettings.test.ts src/lib/components/settings/RepoImportModal.test.ts src/lib/components/settings/repoImportSelection.test.ts
```

Expected: PASS.

- [ ] **Step 3: Run Svelte check**

Run:

```bash
cd frontend && bun run check
```

Expected: PASS.

- [ ] **Step 4: Run full-stack e2e target**

Run:

```bash
make test-e2e
```

Expected: PASS.

- [ ] **Step 5: Inspect git diff**

Run:

```bash
git status --short
git diff --stat
```

Expected: only intended files changed, no generated `package-lock.json`, no unrelated edits.

- [ ] **Step 6: Commit final fixes if any**

If verification required fixes, commit them:

```bash
git add internal/server/repo_import_handlers.go internal/server/server.go internal/server/settings_test.go frontend/src/lib/api/settings.ts frontend/src/lib/api/settings.test.ts frontend/src/lib/components/settings frontend/tests/e2e-full/settings-globs.spec.ts cmd/e2e-server/main.go
git commit -m "fix: stabilize repository import workflow"
```

If no fixes are needed, do not create an empty commit.

---

## Self-review checklist

- Backend API requirements map to Tasks 1 and 2.
- Frontend modal, table, helper, and API requirements map to Tasks 3 and 4.
- Full-stack browser e2e maps to Task 5.
- Verification maps to Task 6.
- Existing live glob support remains via advanced direct add.
- No config schema changes are planned.
- Archived repositories are excluded in `buildRepoPreviewRows`.
- `pushed_at` null display is fixed to `Never pushed`.
- Pattern edits invalidate outstanding preview responses.
- Bulk add persists canonical exact repositories and preserves string-based existing duplicate behavior.

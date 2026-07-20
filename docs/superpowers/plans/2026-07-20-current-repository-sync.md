# Current Repository Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a split header sync control whose main segment keeps the existing full sync and whose menu can sync only the current repository.

**Architecture:** `POST /sync` accepts a provider-qualified `only_repo` filter that resolves against tracked repositories and dispatches a restricted syncer run. The shared sync store exposes a separate scoped method. AppHeader resolves the route repository before a single global-selector value and renders a fixed-position, dismissable menu from the existing split control.

**Tech Stack:** Go, Huma/OpenAPI, Svelte 5 runes, TypeScript, Vite+, Testing Library, testify.

## Global Constraints

- Repository identity is `provider|platform_host/repo_path`; owner/repo alone is invalid.
- `triggerSync()` and `priority_repo` keep their current full-sync behavior.
- `only_repo` never falls back to a full sync when resolution fails.
- Notification sync stays attached only to the full-sync path.
- Use shared kit-ui popover positioning, dismissal, and surface chrome.
- Invoke direct Go tests with `-shuffle=on`; use Vite+ instead of npm.

---

### Task 1: Restrict a syncer run to selected repositories

**Files:**
- Modify: `internal/github/sync.go`
- Test: `internal/github/syncertest/syncer_test.go`

**Interfaces:**
- Produces: `func (s *Syncer) TriggerRunForRepos(ctx context.Context, repos []RepoRef)`.
- Preserves: `TriggerRunWithPriority(ctx, priorityRepos)` and `RunOnce(ctx)`.

- [ ] **Step 1: Write the failing syncer test**

Add a table-backed fake with three tracked repositories, set parallelism to one, call `TriggerRunForRepos` with the third repository, and assert that the upstream list call records only `o/third`:

```go
func TestSyncerTriggerRunForReposSyncsOnlySelectedRepos(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	var mu sync.Mutex
	var calls []string
	mock := &mockClient{
		openPRs: []*gh.PullRequest{},
		listOpenPRsFn: func(_ context.Context, owner, repo string) ([]*gh.PullRequest, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, owner+"/"+repo)
			return []*gh.PullRequest{}, nil
		},
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			ids := map[string]int64{"first": 1, "second": 2, "third": 3}
			id := ids[repo]
			nodeID := "repo-" + owner + "-" + repo
			return &gh.Repository{
				ID: &id, NodeID: &nodeID, Name: &repo,
				Owner: &gh.User{Login: &owner}, Archived: new(bool),
			}, nil
		},
	}
	d := openTestDB(t)
	s := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock}, d, nil,
		[]ghclient.RepoRef{
			{Owner: "o", Name: "first", PlatformHost: "github.com"},
			{Owner: "o", Name: "second", PlatformHost: "github.com"},
			{Owner: "o", Name: "third", PlatformHost: "github.com"},
		},
		time.Hour, nil, nil,
	)
	s.SetParallelism(1)
	done := make(chan struct{}, 1)
	s.SetOnStatusChange(func(status *ghclient.SyncStatus) {
		if !status.Running {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	s.TriggerRunForRepos(t.Context(), []ghclient.RepoRef{{
		Owner: "o", Name: "third", PlatformHost: "github.com",
	}})
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow("scoped TriggerRun did not complete within 1s")
	}
	s.Stop()

	mu.Lock()
	got := slices.Clone(calls)
	mu.Unlock()
	assert.Equal([]string{"o/third"}, got)
}
```

Keep helpers local only when the existing priority test also uses them; otherwise inline the fixture to avoid one-test helpers.

- [ ] **Step 2: Run the focused test and confirm RED**

Run:

```bash
go test ./internal/github/syncertest -run TestSyncerTriggerRunForReposSyncsOnlySelectedRepos -shuffle=on
```

Expected: compile failure because `TriggerRunForRepos` does not exist.

- [ ] **Step 3: Add the restricted trigger**

Extend the internal run path with an optional repository override. Clone caller-owned input before starting the goroutine and preserve the existing single-flight and lifecycle behavior:

```go
func (s *Syncer) TriggerRunForRepos(ctx context.Context, repos []RepoRef) {
	s.triggerRun(ctx, nil, slices.Clone(repos))
}

func (s *Syncer) TriggerRunWithPriority(ctx context.Context, priorityRepos []RepoRef) {
	s.triggerRun(ctx, slices.Clone(priorityRepos), nil)
}

func (s *Syncer) triggerRun(ctx context.Context, priorityRepos, onlyRepos []RepoRef) {
	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return
	}
	merged, cancel := s.mergeWithRunCtx(ctx)
	s.wg.Add(1)
	s.lifecycleMu.Unlock()

	go func() {
		defer s.wg.Done()
		defer cancel()
		s.runOnce(merged, true, priorityRepos, onlyRepos)
	}()
}
```

Update `RunOnce` and `runOnce` callers to pass `nil` for `onlyRepos`. After cloning tracked repositories inside `runOnce`, filter them by `repoPriorityKey` when `onlyRepos != nil`, then apply normal priority ordering:

```go
repos := slices.Clone(s.repos)
if onlyRepos != nil {
	repos = selectRepos(repos, onlyRepos)
}
repos = prioritizeRepos(repos, priorityRepos)
```

`selectRepos` keeps tracked-order output and matches on the existing provider/host/repo-path key.

- [ ] **Step 4: Run syncer tests and confirm GREEN**

Run:

```bash
go test ./internal/github/syncertest -run 'TestSyncerTriggerRun(WithPrioritySyncsSelectedReposFirst|ForReposSyncsOnlySelectedRepos)$' -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Commit the syncer slice**

```bash
git add internal/github/sync.go internal/github/syncertest/syncer_test.go
git commit -m "feat: support repository-scoped sync runs"
```

### Task 2: Expose scoped sync through the API and UI store

**Files:**
- Modify: `internal/server/huma_routes.go`
- Test: `internal/server/sync_routes_test.go`
- Modify generated: `docs/openapi.json`
- Modify generated: `internal/apiclient/generated/client.gen.go`
- Modify generated: `packages/ui/src/api/generated/schema.ts`
- Modify: `packages/ui/src/stores/sync.svelte.ts`
- Test: `packages/ui/src/stores/sync.svelte.test.ts`

**Interfaces:**
- Consumes: `Syncer.TriggerRunForRepos(ctx, repos)` from Task 1.
- Produces: repeated query parameter `only_repo` and `SyncStore.triggerRepoSync(repo: string): Promise<void>`.

- [ ] **Step 1: Write failing server and store tests**

Add a wire-level server test that posts:

```text
/api/v1/sync?only_repo=gitlab|gitlab.example.com/group/subgroup/project
```

Assert HTTP 202 and that the fake syncer receives only the matching full identity. Add a second case for an unknown identity and assert HTTP 400 with `code: "validationError"` and `details.field: "query.only_repo"`; assert no trigger ran.

Add a store test:

```ts
it("passes one provider-qualified repository as the only sync scope", async () => {
  const { store, post } = syncStoreFixture();
  await store.triggerRepoSync("gitlab|gitlab.example.com/group/subgroup/project");
  expect(post).toHaveBeenCalledWith("/sync", {
    params: { query: { only_repo: ["gitlab|gitlab.example.com/group/subgroup/project"] } },
  });
});
```

Keep the existing priority test unchanged to guard full-sync behavior.

- [ ] **Step 2: Run focused tests and confirm RED**

Run:

```bash
go test ./internal/server -run 'TestTriggerSync(OnlyRepo|RejectsUnknownOnlyRepo)$' -shuffle=on
./node_modules/.bin/vp test packages/ui/src/stores/sync.svelte.test.ts
```

Expected: the Go test cannot populate `only_repo`; the TypeScript test cannot find `triggerRepoSync`.

- [ ] **Step 3: Implement server validation and dispatch**

Extend input without changing `PriorityRepos`:

```go
type triggerSyncInput struct {
	PriorityRepos []string `query:"priority_repo" doc:"Optional repository filters to sync first. Accepts repeated provider|platform_host/repo_path values or comma-separated values."`
	OnlyRepos     []string `query:"only_repo" doc:"Optional repository filters to sync exclusively. Accepts repeated provider|platform_host/repo_path values or comma-separated values."`
}
```

Resolve every `only_repo` value against `s.syncer.TrackedRepos()`. Reject malformed or unmatched values with:

```go
return nil, problemValidation("query.only_repo", "repository must match a configured provider|platform_host/repo_path")
```

When `OnlyRepos` is non-empty, call `TriggerRunForRepos(context.WithoutCancel(ctx), repos)` and skip notification sync. Otherwise preserve the current priority and notification path exactly.

- [ ] **Step 4: Regenerate API artifacts**

Run:

```bash
make api-generate
```

Expected: OpenAPI, Go client, and TypeScript schema expose `only_repo?: string[] | null`.

- [ ] **Step 5: Implement the store method**

Extract the optimistic status/error wrapper only if it removes duplication cleanly. Expose a scoped method that sends exactly one full identity:

```ts
async function triggerRepoSync(repo: string): Promise<void> {
  await trigger({ params: { query: { only_repo: [repo] } } });
}
```

`triggerSync()` continues to calculate `priority_repo` from `getPriorityRepos()` and calls the same internal trigger helper.

- [ ] **Step 6: Run API and store tests and confirm GREEN**

Run:

```bash
go test ./internal/server -run 'TestTriggerSync|TestMatchPriorityRepo' -shuffle=on
./node_modules/.bin/vp test packages/ui/src/stores/sync.svelte.test.ts
```

Expected: PASS.

- [ ] **Step 7: Commit the contract slice**

```bash
git add internal/server/huma_routes.go internal/server/sync_routes_test.go docs/openapi.json internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts packages/ui/src/stores/sync.svelte.ts packages/ui/src/stores/sync.svelte.test.ts
git commit -m "feat: expose repository-only sync requests"
```

### Task 3: Build the split sync control

**Files:**
- Modify: `frontend/src/lib/utils/repoSelectionSync.ts`
- Test: `frontend/src/lib/utils/repoSelectionSync.test.ts`
- Modify: `frontend/src/lib/components/layout/AppHeader.svelte`
- Test: `frontend/src/lib/components/layout/AppHeader.test.ts`

**Interfaces:**
- Consumes: `sync.triggerRepoSync(repo)` from Task 2.
- Produces: `syncRepoForRoute(route: Route): string | undefined` and the header split-menu interaction.

- [ ] **Step 1: Write route-resolution and header interaction tests**

Add table cases proving pull, issue, focus, and repo-browser routes return:

```text
provider|platformHost/repoPath
```

Add AppHeader tests with mocked `triggerSync` and `triggerRepoSync`:

```ts
it("keeps full sync on the primary segment", async () => {
  render(AppHeader);
  await fireEvent.click(screen.getByRole("button", { name: "Sync" }));
  expect(triggerSync).toHaveBeenCalledOnce();
  expect(triggerRepoSync).not.toHaveBeenCalled();
});

it("syncs the route repository from the chevron menu", async () => {
  navigate("/host/ghe.example.com/pulls/github/acme/widget/7");
  render(AppHeader);
  await fireEvent.click(screen.getByRole("button", { name: "Sync options" }));
  await fireEvent.click(screen.getByRole("menuitem", { name: "Sync current repo" }));
  expect(triggerRepoSync).toHaveBeenCalledWith("github|ghe.example.com/acme/widget");
});
```

Add cases for single selector fallback, disabled menu item for All repos/multi-select, Escape focus restoration, and shared running-state disabled controls.

- [ ] **Step 2: Run focused frontend tests and confirm RED**

Run:

```bash
./node_modules/.bin/vp test frontend/src/lib/utils/repoSelectionSync.test.ts frontend/src/lib/components/layout/AppHeader.test.ts
```

Expected: missing resolver, options button, and menu action failures.

- [ ] **Step 3: Implement repository resolution**

Add `syncRepoForRoute(route)` beside `globalRepoForSelectedRoute`. Reuse the selected-item resolution and add repo-browser identity. AppHeader derives:

```ts
const currentSyncRepo = $derived.by(() => {
  const routeRepo = syncRepoForRoute(getRoute());
  if (routeRepo) return routeRepo;
  const selected = parseRepoFilterValue(getGlobalRepo());
  return selected.length === 1 ? selected[0] : undefined;
});
```

- [ ] **Step 4: Implement the fixed split-menu control**

Import `autoReposition`, `dismissable`, and `floatingPopoverStyle` from kit-ui. Keep the primary button handler unchanged. Add a chevron button with `aria-label="Sync options"`, `aria-haspopup="menu"`, and `aria-expanded`. The open menu uses:

```svelte
<ul
  class="sync-menu kit-popover-card"
  role="menu"
  aria-label="Sync options"
  bind:this={syncMenu}
  style={syncMenuStyle}
>
  <li>
    <button
      type="button"
      role="menuitem"
      disabled={!currentSyncRepo || syncing}
      onclick={handleCurrentRepoSync}
    >Sync current repo</button>
  </li>
</ul>
```

Position it with `floatingPopoverStyle({ align: "end", triggerGap: 2 })`. Wire `dismissable` and `autoReposition` in an open-state effect. Use `kit-popover-card` for chrome and local CSS only for split-button geometry and menu-item layout.

- [ ] **Step 5: Run Svelte analysis and focused tests**

Run:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer ./frontend/src/lib/components/layout/AppHeader.svelte
./node_modules/.bin/vp test frontend/src/lib/utils/repoSelectionSync.test.ts frontend/src/lib/components/layout/AppHeader.test.ts
```

Expected: autofixer reports no actionable findings; tests PASS.

- [ ] **Step 6: Commit the UI slice**

```bash
git add frontend/src/lib/utils/repoSelectionSync.ts frontend/src/lib/utils/repoSelectionSync.test.ts frontend/src/lib/components/layout/AppHeader.svelte frontend/src/lib/components/layout/AppHeader.test.ts
git commit -m "feat: add current repository sync menu"
```

### Task 4: Final verification

**Files:**
- Review all files changed by Tasks 1 through 3.

**Interfaces:**
- Verifies the complete contract without adding new behavior.

- [ ] **Step 1: Run Go verification**

```bash
go test ./internal/github/syncertest ./internal/server -shuffle=on
```

Expected: PASS.

- [ ] **Step 2: Run the complete frontend suite and checks**

```bash
./node_modules/.bin/vp test
./node_modules/.bin/vp run check
```

Expected: PASS with no warnings attributable to this change.

- [ ] **Step 3: Review generated and source diffs**

```bash
git status --short
git diff HEAD~3 --stat
git diff HEAD~3 -- docs/openapi.json packages/ui/src/api/generated/schema.ts internal/apiclient/generated/client.gen.go
```

Expected: generated changes only add `only_repo`; no unrelated files appear.

- [ ] **Step 4: Run repository context sync before the final commit**

```bash
scripts/context-sync --check
```

Inspect the intended diff and update the smallest mapped context document only if the work introduced a durable rule not already captured by the design spec and existing provider/UI context.

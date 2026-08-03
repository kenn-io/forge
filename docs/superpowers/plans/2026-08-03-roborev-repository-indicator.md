# Roborev Repository Indicator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show an accessible bottom-right icon on a repository card when any Roborev-tracked checkout with the same remote identity has a Roborev-managed post-commit hook.

**Approved spec/design:** `docs/superpowers/specs/2026-08-03-roborev-repository-indicator-design.md`

**Architecture:** A process-local server probe fetches Roborev's checkout inventory once, resolves effective hook paths with bounded concurrency, caches definitive per-checkout results, and retries only transient failures after a cooldown. A dedicated Huma endpoint exposes deduplicated provider route identities; the Repositories page loads that optional signal separately from summaries and passes a boolean to the card presentation component.

**Tech Stack:** Go 1.26, Huma/OpenAPI, stdlib HTTP/process/filesystem primitives, Svelte 5 runes, TypeScript, Vite+ unit tests, Playwright full-stack e2e.

## Global Constraints

- Support GitHub, GitLab, Forgejo, and Gitea through provider-neutral remote parsing; do not add GitHub-only matching.
- Treat a repository as configured only when at least one matching tracked checkout has a regular executable Roborev-managed `post-commit` hook.
- Cache a successful Roborev inventory and definitive checkout results for the Kenn Forge process lifetime; transient errors use a 30-second retry cooldown.
- Bound concurrent hook-path probes at four and coalesce concurrent endpoint requests into one in-flight probe.
- Never expose local checkout or hook paths in the Kenn Forge API.
- Roborev failures must not block `/repos/summary` or turn into a Repositories page error.
- The indicator is a muted 16px Bot icon with accessible label and tooltip `Roborev hooks installed`; it has no click behavior, count, badge, or reserved space when absent.
- Use `huma` for the route and run `make api-generate` after the API shape changes.
- Use Bun/Vite+ commands, never npm.
- Before every commit, run the repository-local `context-sync --commit` workflow and then the mandatory `commit-push-pr:commit` skill. Never amend or bypass hooks.

---

## File Structure

- Create `internal/server/roborev_repositories.go`: Roborev inventory client, hook recognition, bounded probing, cache/single-flight coordinator, response conversion, and Huma handler.
- Create `internal/server/roborev_repositories_test.go`: focused cache, concurrency, retry, hook-content, and API tests.
- Modify `internal/server/server.go`: own and initialize one process-local Roborev repository probe.
- Modify `internal/server/api_types.go`: define the path-free configured-repository response types.
- Modify `internal/server/huma_routes.go`: register `GET /roborev/configured-repositories`.
- Regenerate `frontend/openapi/openapi.yaml`, `internal/apiclient/spec/openapi.json`, `internal/apiclient/generated/client.gen.go`, and `packages/ui/src/api/generated/schema.ts`.
- Modify `packages/ui/src/api/types.ts`: export aliases for the generated configured-repository response.
- Modify `frontend/src/lib/icons.ts`: export Lucide's Bot icon through the local icon boundary.
- Modify `frontend/vite.config.ts`: pre-bundle the new direct Lucide Bot import.
- Modify `frontend/src/lib/components/repositories/repoSummary.ts`: reuse one canonical provider/host/repository-path key builder for summaries and configured references.
- Modify `frontend/src/lib/components/repositories/repoSummary.test.ts`: pin canonical key behavior.
- Modify `frontend/src/lib/components/repositories/RepoSummaryPage.svelte`: independently load the optional configured-reference set and pass card state.
- Modify `frontend/src/lib/components/repositories/RepoSummaryCard.svelte`: render and position the accessible indicator.
- Modify `frontend/src/lib/components/repositories/RepoSummaryPage.test.ts`: pin matched, unmatched, and optional-endpoint-failure behavior.
- Create `frontend/tests/e2e-full/roborev-repository-indicator.spec.ts`: exercise a real temporary Git checkout, fake Roborev daemon, real Kenn Forge API/SQLite process, embedded SPA, and process-lifetime cache.

---

### Task 1: Cached Roborev Hook Discovery

**Files:**
- Create: `internal/server/roborev_repositories.go`
- Create: `internal/server/roborev_repositories_test.go`
- Modify: `internal/server/api_types.go:20-90`
- Modify: `internal/server/server.go:140-220,709-790`

**Interfaces:**
- Consumes: `config.Config.RoborevEndpoint()`, `projects.ParseRemoteURLWithKnownPlatforms`, `workspaceConfigSnapshot(cfg, nil).KnownPlatformHosts`, `procutil.CommandContext`, and `gitenv.StripAll`.
- Produces: `newRoborevRepositoryProbe(endpoint string, knownHosts []projects.KnownPlatformHost) *roborevRepositoryProbe` and `(*roborevRepositoryProbe).configuredRepositories(context.Context) ([]roborevConfiguredRepositoryResponse, error)` for Task 2.
- Internal test seams: `roborevRepositoryProbeDeps{now, loadInventory, resolveHookPath, inspectHook, onWaitForInFlight}`; production defaults use HTTP, Git, and `os`, and the waiter callback is nil.

- [ ] **Step 1: Write failing cache and identity tests**

Add table-driven tests that feed inventory entries through injected functions. The central fixtures and assertions should have this shape:

```go
func TestRoborevRepositoryProbeCachesDefinitiveResultsAndDeduplicatesIdentity(t *testing.T) {
	var inventoryCalls atomic.Int32
	var hookCalls atomic.Int32
	var inspectCalls atomic.Int32
	probe := newRoborevRepositoryProbeWithDeps(
		"http://roborev.test",
		[]projects.KnownPlatformHost{{Platform: "github", Host: "github.com"}},
		roborevRepositoryProbeDeps{
			now: time.Now,
			loadInventory: func(context.Context) ([]roborevTrackedRepository, error) {
				inventoryCalls.Add(1)
				return []roborevTrackedRepository{
					{RootPath: "/checkout/main", Identity: "https://github.com/acme/widgets.git"},
					{RootPath: "/checkout/worktree", Identity: "git@github.com:acme/widgets.git"},
				}, nil
			},
			resolveHookPath: func(context.Context, string) (string, error) {
				hookCalls.Add(1)
				return "/shared/hooks/post-commit", nil
			},
			inspectHook: func(string) (bool, error) {
				inspectCalls.Add(1)
				return true, nil
			},
		},
	)

	first, err := probe.configuredRepositories(t.Context())
	require.NoError(t, err)
	second, err := probe.configuredRepositories(t.Context())
	require.NoError(t, err)

	require.Len(t, first, 1)
	assert.Equal(t, first, second)
	assert.Equal(t, int32(1), inventoryCalls.Load())
	assert.Equal(t, int32(2), hookCalls.Load())
	assert.Equal(t, int32(1), inspectCalls.Load())
}
```

Add adjacent cases for GitLab nested namespaces, configured self-hosted Forgejo/Gitea hosts, `local://` and malformed identities being ignored, one unhooked checkout plus one hooked checkout producing one positive identity, and response structs containing no root-path field.

- [ ] **Step 2: Run the focused tests and confirm the red state**

Run:

```bash
go test ./internal/server -run 'TestRoborevRepositoryProbe' -shuffle=on
```

Expected: FAIL to compile because `newRoborevRepositoryProbeWithDeps`, `roborevTrackedRepository`, and the probe types do not exist.

- [ ] **Step 3: Implement only the synchronous definitive cache**

Add the tracked-repository, response, dependency, and probe types plus a
synchronous implementation that loads inventory once, parses identities,
resolves and inspects each checkout, caches definitive booleans, deduplicates
responses, and sorts the result. Do not add worker goroutines, retry cooldowns,
or single-flight waiting yet.

Run `go test ./internal/server -run 'TestRoborevRepositoryProbeCachesDefinitiveResultsAndDeduplicatesIdentity' -shuffle=on`.

Expected: PASS. This closes the first red/green cycle before adding concurrency.

- [ ] **Step 4: Write failing single-flight, bound, and retry tests**

Add three focused tests:

```go
func TestRoborevRepositoryProbeCoalescesConcurrentRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	waiterJoined := make(chan struct{})
	var calls atomic.Int32
	probe := newProbeForTest(roborevRepositoryProbeDeps{
		onWaitForInFlight: func() { close(waiterJoined) },
		loadInventory: func(context.Context) ([]roborevTrackedRepository, error) {
			if calls.Add(1) == 1 { close(started) }
			<-release
			return nil, nil
		},
	})

	results := make(chan error, 2)
	go func() { _, err := probe.configuredRepositories(t.Context()); results <- err }()
	<-started
	go func() { _, err := probe.configuredRepositories(t.Context()); results <- err }()
	<-waiterJoined
	close(release)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	assert.Equal(t, int32(1), calls.Load())
}

func TestRoborevRepositoryProbeBoundsHookResolution(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	probe := newProbeWithCheckoutCountForTest(12, func(_ context.Context, root string) (string, error) {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) { break }
		}
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
		return root + "/post-commit", nil
	})

	_, err := probe.configuredRepositories(t.Context())
	require.NoError(t, err)
	assert.LessOrEqual(t, maximum.Load(), int32(roborevHookProbeWorkers))
}

func TestRoborevRepositoryProbeRetriesTransientCheckoutFailureAfterCooldown(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	probe := newSingleCheckoutProbeForTest(&now, func(context.Context, string) (string, error) {
		if calls.Add(1) == 1 { return "", errors.New("temporary git failure") }
		return "/hooks/post-commit", nil
	})

	first, err := probe.configuredRepositories(t.Context())
	require.NoError(t, err)
	assert.Empty(t, first)
	now = now.Add(29 * time.Second)
	_, err = probe.configuredRepositories(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load())
	now = now.Add(time.Second)
	third, err := probe.configuredRepositories(t.Context())
	require.NoError(t, err)
	assert.Len(t, third, 1)
	assert.Equal(t, int32(2), calls.Load())
}
```

Define `newProbeForTest`, `newProbeWithCheckoutCountForTest`, and
`newSingleCheckoutProbeForTest` in the same test file as small constructors
that fill every dependency with deterministic in-memory defaults. They must not
call the network, Git, or the host filesystem.

Run:

```bash
go test ./internal/server -run 'TestRoborevRepositoryProbe(Coalesces|Bounds|Retries)' -shuffle=on
```

Expected: FAIL on the concurrency bound, missing retry, or duplicate inventory
work while the new tests compile and execute against the synchronous cache.

Also pin an inventory transport failure: calls inside the cooldown return the cached service error without another HTTP attempt, while the first call after the cooldown retries. A successful empty inventory must remain cached without retries.

- [ ] **Step 5: Implement single-flight, bounded probing, and cooldown retries**

Complete the production and dependency types. Keep
`roborevConfiguredRepositoryResponse` in `api_types.go`; keep the inventory,
cache, and dependency types in `roborev_repositories.go`:

```go
const (
	roborevHookProbeWorkers = 4
	roborevProbeRetryCooldown = 30 * time.Second
)

type roborevTrackedRepository struct {
	RootPath string `json:"root_path"`
	Identity string `json:"identity"`
}

type roborevRepositoryInventory struct {
	Repos      []roborevTrackedRepository `json:"repos"`
	TotalCount int                         `json:"total_count"`
}

type roborevConfiguredRepositoryResponse struct {
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	RepoPath     string `json:"repo_path"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
}

type roborevRepositoryProbeDeps struct {
	now             func() time.Time
	loadInventory   func(context.Context) ([]roborevTrackedRepository, error)
	resolveHookPath func(context.Context, string) (string, error)
	inspectHook     func(string) (bool, error)
	onWaitForInFlight func()
}
```

Implement a mutex-protected probe with one retained successful inventory, one `inFlight chan struct{}` for concurrent callers, per-root definitive state or `retryAfter`, a per-run hook-path map so duplicate paths are read once, four workers consuming unresolved roots, and deterministic sorting by provider, host, then repository path.

The production inventory loader must use `http.NewRequestWithContext`, a two-second client timeout, `strings.TrimRight(endpoint, "/") + "/api/repos"`, reject non-2xx status codes, limit the response to 2 MiB, and decode the exact `{ "repos": [...], "total_count": n }` envelope into `roborevRepositoryInventory`. It retains only `root_path` plus `identity` after decoding and must not log or return paths to the handler. Unit and HTTP fixtures must always use this envelope, never a bare array.

The production hook resolver must run `git -C <root_path> rev-parse --path-format=absolute --git-path hooks/post-commit` through `procutil.CommandContext` with `gitenv.StripAll(os.Environ())`. The inspector treats `os.IsNotExist` as definitive false, requires a regular executable file on Unix (regular file on Windows), reads at most 64 KiB of hook content, and recognizes case-insensitive Roborev generated markers plus current/legacy invocations: `roborev post-commit`, `"$ROBOREV" post-commit`, `roborev enqueue`, and `"$ROBOREV" enqueue`.

Initialize the probe once in `newServer` without dereferencing a nil config:

```go
roborevConfig := cfg
if roborevConfig == nil {
	roborevConfig = &config.Config{}
}
s.roborevRepositories = newRoborevRepositoryProbe(
	roborevConfig.RoborevEndpoint(),
	workspaceConfigSnapshot(cfg, nil).KnownPlatformHosts,
)
```

Default public provider hosts continue to resolve inside `projects.ParseRemoteURLWithKnownPlatforms` even when the known-host slice is empty.

- [ ] **Step 6: Write and pass the production hook-inspector matrix**

Add `TestInspectRoborevPostCommitHook` with table cases for:

- current marker plus `"$ROBOREV" post-commit`;
- current direct `roborev post-commit`;
- legacy `roborev enqueue --quiet` and `"$ROBOREV" enqueue --quiet`;
- generated marker-only content;
- unrelated executable content;
- non-executable Roborev content on Unix;
- a directory at the hook path;
- a missing file;
- a permission/read error classified as transient.

Use `t.TempDir`, `os.WriteFile`, and explicit modes. On Windows, skip only the
Unix execute-bit assertion while retaining every content and file-type case.
Add a probe test where one checkout is definitively positive while a second
checkout's `inspectHook` returns an error: the positive identity remains in the
partial result, the failed checkout does not become definitive false, and only
the failed checkout retries after 30 seconds.

Run:

```bash
go test ./internal/server -run 'Test(InspectRoborevPostCommitHook|RoborevRepositoryProbeRetriesInspectError)' -shuffle=on
```

Expected before production inspector implementation: FAIL on missing recognition
and retry behavior. Implement only the 64 KiB bounded inspector and per-checkout
error classification, rerun, and expect PASS.

- [ ] **Step 7: Run focused tests and race coverage**

Run:

```bash
go test ./internal/server -run 'TestRoborevRepositoryProbe' -shuffle=on
go test -race ./internal/server -run 'TestRoborevRepositoryProbe(Coalesces|Bounds|Retries)' -shuffle=on
```

Expected: PASS with no race reports.

- [ ] **Step 8: Commit the discovery slice**

Run `scripts/context-sync --check`, inspect the intended diff, invoke `context-sync --commit`, then invoke `commit-push-pr:commit` and create a hook-enforced commit with subject `feat: cache roborev repository hook state`. The body must explain that the process-lifetime cache avoids repeated daemon, Git, and filesystem work while transient failures remain retryable.

---

### Task 2: Huma Endpoint and Generated Contract

**Files:**
- Modify: `internal/server/api_types.go:20-100`
- Modify: `internal/server/huma_routes.go:90-110,220-255,650-705`
- Modify: `internal/server/roborev_repositories.go`
- Modify: `internal/server/roborev_repositories_test.go`
- Regenerate: `frontend/openapi/openapi.yaml`
- Regenerate: `internal/apiclient/spec/openapi.json`
- Regenerate: `internal/apiclient/generated/client.gen.go`
- Regenerate: `packages/ui/src/api/generated/schema.ts`
- Modify: `packages/ui/src/api/types.ts:1-25`

**Interfaces:**
- Consumes: Task 1's `(*roborevRepositoryProbe).configuredRepositories(context.Context)`.
- Produces: `GET /roborev/configured-repositories` with operation ID `list-roborev-configured-repositories` and body `{ "repositories": RoborevConfiguredRepositoryResponse[] }`; generated frontend call `client.GET("/roborev/configured-repositories")` for Task 3.

- [ ] **Step 1: Write the failing HTTP contract tests**

Use `setupTestServerWithRoborev`, an `httptest.Server` standing in for Roborev, and temporary Git repositories. Create one custom `core.hooksPath` containing an executable generated-style `post-commit`, a duplicate worktree identity, and one unhooked identity. Assert the real Kenn Forge HTTP response contains only one path-free configured ref:

```go
func TestListRoborevConfiguredRepositories(t *testing.T) {
	forge := httptest.NewServer(setupTestServerWithRoborev(t, daemon.URL))
	defer forge.Close()

	resp, err := http.Get(forge.URL + "/api/v1/roborev/configured-repositories")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Repositories []roborevConfiguredRepositoryResponse `json:"repositories"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Repositories, 1)
	assert.Equal(t, "github|github.com/acme/widgets", configuredRepositoryKey(body.Repositories[0]))
}
```

The fake `/api/repos` returns `https://github.com/acme/widgets.git` twice and `https://github.com/acme/tools.git` once; only widgets has a managed hook. Add a transport-failure test expecting a typed 503 from only the dedicated endpoint, followed by 200 from `/api/v1/repos/summary`. Assert the endpoint error body never includes the configured Roborev URL or a local path.

- [ ] **Step 2: Run the HTTP tests and confirm the red state**

Run `go test ./internal/server -run 'TestListRoborevConfiguredRepositories' -shuffle=on`.

Expected: FAIL with HTTP 404 while the test compiles and exercises the missing route.

- [ ] **Step 3: Register the endpoint and response schema**

Define:

```go
type roborevConfiguredRepositoriesResponse struct {
	Repositories []roborevConfiguredRepositoryResponse `json:"repositories"`
}

type roborevConfiguredRepositoriesOutput =
	httpapi.BodyOutput[roborevConfiguredRepositoriesResponse]
```

Register beside `/roborev/status`:

```go
huma.Register(api, huma.Operation{
	OperationID:   "list-roborev-configured-repositories",
	Method:        http.MethodGet,
	Path:          "/roborev/configured-repositories",
	DefaultStatus: http.StatusOK,
	Summary:       "List repositories configured for Roborev",
	Tags:          []string{"Roborev"},
}, s.listRoborevConfiguredRepositories)
```

The handler returns `httpapi.ServiceUnavailable("roborev repository configuration unavailable")` only when inventory loading fails. Per-checkout transient errors return the definitive partial set and stay retryable inside the probe. The failure test must assert status 503, `Content-Type: application/problem+json`, top-level `code: "serviceUnavailable"`, the generic detail, and absence of daemon/local-path values.

- [ ] **Step 4: Verify the endpoint tests pass**

Run `go test ./internal/server -run 'Test(ListRoborevConfiguredRepositories|RoborevRepositoryProbe)' -shuffle=on`.

Expected: PASS.

- [ ] **Step 5: Regenerate and verify the API clients**

Run:

```bash
make api-generate
git diff --check
```

Export the generated aliases in `packages/ui/src/api/types.ts`:

```ts
export type RoborevConfiguredRepository = components["schemas"]["RoborevConfiguredRepositoryResponse"];
export type RoborevConfiguredRepositories = components["schemas"]["RoborevConfiguredRepositoriesResponse"];
```

Update the HTTP test to call `ListRoborevConfiguredRepositoriesWithResponse` through the generated Go test client as an additional contract assertion. Then run `go test ./internal/server -run 'TestListRoborevConfiguredRepositories' -shuffle=on` and `make frontend-api-client-check`.

Expected: PASS and no hand-built `/api/v1` frontend URL.

- [ ] **Step 6: Commit the API slice**

Run `scripts/context-sync --check`, inspect generated and source diffs, invoke `context-sync --commit`, then invoke `commit-push-pr:commit` and create a hook-enforced commit with subject `feat: expose roborev configured repositories`. The body must explain why the optional signal has a dedicated endpoint instead of delaying repository summaries.

---

### Task 3: Repository Card Indicator

**Files:**
- Modify: `frontend/src/lib/icons.ts:1-20`
- Modify: `frontend/vite.config.ts:390-470`
- Modify: `frontend/src/lib/components/repositories/repoSummary.ts:1-85`
- Modify: `frontend/src/lib/components/repositories/repoSummary.test.ts`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryPage.svelte:1-190,315-545`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryCard.svelte:1-45,360-725`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryPage.test.ts:1-340`

**Interfaces:**
- Consumes: Task 2's generated `client.GET("/roborev/configured-repositories")` and `RoborevConfiguredRepository` type.
- Produces: `providerRepoStateKey(ref)`, a comparison-only normalized key, a `roborevConfigured?: boolean` card prop, and a rendered `role="img"` indicator named `Roborev hooks installed`.

- [ ] **Step 1: Pin the shared identity key**

Add a unit test proving summaries and configured refs produce the same full key, including a nested GitLab route:

```ts
it("keys provider repository refs by provider host and full path", () => {
  expect(providerRepoStateKey({
    provider: "Forgejo",
    platform_host: "CODEBERG.org",
    repo_path: "MixedCase/Service",
  })).toBe("forgejo|codeberg.org/mixedcase/service");
});
```

Add a second assertion that a lowercased remote-derived configured ref matches
a mixed-case stored summary ref without modifying either ref's display fields.

Run `(cd frontend && ../node_modules/.bin/vp test run src/lib/components/repositories/repoSummary.test.ts --project unit)`.

Expected: FAIL because `providerRepoStateKey` is not exported.

- [ ] **Step 2: Implement the canonical key helper**

Add:

```ts
export function providerRepoStateKey(repo: {
  provider: string;
  platform_host: string;
  repo_path: string;
}): string {
  const provider = repo.provider.trim().toLowerCase();
  const host = repo.platform_host.trim().toLowerCase();
  const path = repo.repo_path.trim().replace(/^\/+|\/+$/g, "").toLowerCase();
  return `${provider}|${host}/${path}`;
}

export function repoStateKey(summary: { repo: Parameters<typeof providerRepoStateKey>[0] }): string {
  return providerRepoStateKey(summary.repo);
}
```

Rerun the focused test and expect PASS.

- [ ] **Step 3: Write failing component behavior tests**

Route `mockGet` by path for a two-card fixture. Return one configured ref and assert only its card contains the indicator:

```ts
it("shows the Roborev hook indicator only on matching repository cards", async () => {
  const widgets = repoSummaryFixture({
    provider: "github", platformHost: "github.com", owner: "acme", name: "widgets",
  });
  const tools = repoSummaryFixture({
    provider: "github", platformHost: "github.com", owner: "acme", name: "tools",
  });
  mockGet.mockImplementation((path: string) => {
    if (path === "/roborev/configured-repositories") {
      return Promise.resolve({
        data: { repositories: [{
          provider: "github",
          platform_host: "github.com",
          repo_path: "acme/widgets",
          owner: "acme",
          name: "widgets",
        }] },
        error: undefined,
      });
    }
    return Promise.resolve({ data: [widgets, tools], error: undefined });
  });

  render(RepoSummaryPage);
  await waitFor(() => expect(
    screen.getByRole("img", { name: "Roborev hooks installed" }),
  ).toBeTruthy());
});
```

Scope assertions to each `.repo-card`: widgets has exactly one indicator and tools has none. Add a second test where the optional endpoint returns `{ error: { detail: "unavailable" } }`: both summary cards remain visible, no flash/page error appears, and no indicator renders.

Run `(cd frontend && ../node_modules/.bin/vp test run src/lib/components/repositories/RepoSummaryPage.test.ts --project unit)`.

Expected: FAIL because the page never loads the endpoint and the card has no indicator.

- [ ] **Step 4: Load optional configuration independently**

In `RepoSummaryPage.svelte`, maintain:

```ts
let roborevConfiguredRepos = $state<Set<string>>(new Set());

async function loadRoborevConfiguredRepositories(): Promise<void> {
  try {
    const { data, error } = await client.GET("/roborev/configured-repositories");
    if (error || !data) return;
    roborevConfiguredRepos = new Set(data.repositories.map(providerRepoStateKey));
  } catch {
    // Roborev is optional; repository summaries remain authoritative.
  }
}
```

Call this once from `onMount` alongside `loadSummaries`, not from the 30-second summary refresh. Pass `roborevConfigured={roborevConfiguredRepos.has(repoStateKey(summary))}` to each `RepoSummaryCard`. Do not add loading state, flash messages, polling, or page errors for this request.

- [ ] **Step 5: Render the bottom-right accessible icon**

Export Lucide Bot from `frontend/src/lib/icons.ts` and add the bare `"@lucide/svelte/icons/bot"` entry to `frontend/vite.config.ts`'s complete direct-icon `optimizeDeps.include` list. Add `roborevConfigured?: boolean` to card props and render as the last footer child:

```svelte
{#if roborevConfigured}
  <span
    class="repo-card__roborev"
    role="img"
    aria-label="Roborev hooks installed"
    title="Roborev hooks installed"
  >
    <BotIcon size={16} strokeWidth={2} aria-hidden="true" />
  </span>
{/if}
```

Style only semantic placement and color:

```css
.repo-card__roborev {
  display: inline-flex;
  margin-left: auto;
  color: var(--text-secondary);
}
```

The conditional element must be absent—not hidden—when false.

- [ ] **Step 6: Run Svelte analysis and focused tests**

Because Svelte files changed, run the required Svelte tooling against both edited components, fix every reported issue, and then run:

```bash
(cd frontend && ../node_modules/.bin/vp test run src/lib/components/repositories/repoSummary.test.ts src/lib/components/repositories/RepoSummaryPage.test.ts --project unit)
make frontend-check-no-deps
```

Expected: PASS with no Svelte, lint, formatting, or kit-ui findings.

- [ ] **Step 7: Commit the frontend slice**

Run `scripts/context-sync --check`, inspect the frontend diff, invoke `context-sync --commit`, then invoke `commit-push-pr:commit` and create a hook-enforced commit with subject `feat: show roborev on configured repository cards`. The body must explain that the icon reflects verified hooks and that optional daemon failure remains invisible to core repository state.

---

### Task 4: Full-Stack Browser Coverage and Final Verification

**Files:**
- Create: `frontend/tests/e2e-full/roborev-repository-indicator.spec.ts`
- Modify only if the test exposes a real defect: files owned by Tasks 1-3, followed by rerunning their focused tests.

**Interfaces:**
- Consumes: the real endpoint and SPA behavior from Tasks 2-3.
- Produces: browser proof that a real effective hook produces the indicator and repeated page loads do not repeat Roborev inventory work.

- [ ] **Step 1: Write the failing full-stack test**

Build a test-local fixture with Node stdlib only: create a temporary Git repository; configure `remote.origin.url=https://github.com/acme/widgets.git` and an absolute `core.hooksPath`; write an executable `post-commit` with `# roborev post-commit hook v4` and `roborev post-commit`; start a test-owned HTTP server whose `/api/repos` response is exactly `{ repos: [{ name: "widgets", root_path: checkout, identity: "https://github.com/acme/widgets.git", count: 0 }], total_count: 1 }`, whose `/api/status` response is healthy, and which records request paths. Set `ROBOREV_ENDPOINT`, start a fresh isolated Kenn Forge e2e server, and restore the environment immediately after spawn.

The browser assertions should be:

```ts
await page.goto(`${forge.info.base_url}/repos`);
const widgets = page.locator(".repo-card").filter({
  has: page.getByRole("button", { name: /acme\s*\/\s*widgets/ }),
}).first();
await expect(widgets.getByRole("img", { name: "Roborev hooks installed" })).toBeVisible();

const tools = page.locator(".repo-card").filter({
  has: page.getByRole("button", { name: /acme\s*\/\s*tools/ }),
}).first();
await expect(tools.getByRole("img", { name: "Roborev hooks installed" })).toHaveCount(0);

await page.reload();
await expect(widgets.getByRole("img", { name: "Roborev hooks installed" })).toBeVisible();
expect(requests.filter((path) => path === "/api/repos")).toHaveLength(1);
```

Always close the test-owned Forge process, HTTP server, and temporary directory in `finally`.

- [ ] **Step 2: Run the affected e2e test**

Run both affected browser projects:

```bash
(cd frontend && ../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/roborev-repository-indicator.spec.ts --project=roborev --project=roborev-firefox)
```

Expected: PASS in Chromium and Firefox. If it fails, fix production behavior rather than weakening assertions, rerun focused unit/Go tests, and rerun Playwright.

- [ ] **Step 3: Run final generated, Go, frontend, and browser verification**

Run after the final source or test edit:

```bash
make api-generate
git diff --exit-code -- frontend/openapi/openapi.yaml internal/apiclient/spec/openapi.json internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts
go test ./internal/server -shuffle=on
(cd frontend && ../node_modules/.bin/vp test run --project unit)
make frontend-check-no-deps
(cd frontend && ../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/roborev-repository-indicator.spec.ts --project=roborev --project=roborev-firefox)
```

Expected: every command passes; API regeneration produces no further diff; the full frontend unit suite is green after the final frontend/e2e edit.

- [ ] **Step 4: Review the complete diff against the approved spec**

Run:

```bash
git status --short
git diff --check
git diff HEAD --stat
```

Confirm every spec item has evidence: exact hook detection, any-checkout semantics, provider-neutral identity, no local paths on the wire, process-lifetime caching, bounded/single-flight probing, transient retries, independent page failure behavior, bottom-right accessible icon, and full-stack coverage.

- [ ] **Step 5: Commit the e2e coverage and any final fixes**

Run `scripts/context-sync --check`, inspect the intended diff, invoke `context-sync --commit`, then invoke `commit-push-pr:commit` and create a hook-enforced commit with subject `test: cover roborev repository indicators end to end`. The body must record that the test uses a real temporary Git hook and proves repeated page loads reuse the server's cached Roborev inventory. Do not push unless explicitly requested.

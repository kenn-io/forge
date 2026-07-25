# GitHub Credential-Aware Rate Accounting Implementation Plan

> **For Codex:** Execute this plan with `superpowers:executing-plans`. Follow
> strict red-green-refactor for every behavior change and keep `.lanes/`
> untracked.

**Goal:** Schedule GitHub background work from the actual credential/resource
quota while retaining `sync_budget_per_hour` only as a high, atomic local
emergency ceiling.

**Architecture:** Add a process-local GitHub quota registry keyed by host,
safe credential identity, and REST/GraphQL resource. Authentication annotates
each authorized wire attempt with the identity that supplied its token; quota
transports observe the resulting response headers. The syncer predicts the
credential for repo admission, reconciles each credential through `/rate_limit`
at a three-minute cadence, and exposes provider pools separately from local
ceilings.

**Tech Stack:** Go, `net/http`, go-github, SQLite-backed legacy provider rate
trackers, Huma/OpenAPI, Svelte 5, Vitest/Vite+.

---

## Task 1: Carry a safe credential identity through authenticated requests

**Files:**

- Modify: `internal/tokenauth/source.go`
- Modify: `internal/tokenauth/descriptor.go`
- Modify: `internal/tokenauth/transport.go`
- Test: `internal/tokenauth/source_test.go`
- Test: `internal/tokenauth/transport_test.go`

1. Add failing table-driven tests proving an App token resolves as
   `github_app:<installation_id>`, PAT/file/CLI credentials resolve as `user`,
   owner mismatch falls through to `user`, and mutation auth resolves as
   `user`.
2. Run the focused tokenauth tests and confirm the expected missing API or
   identity assertions fail.
3. Add a resolved-credential value and context accessor. Make
   `ManagedSource.Resolve` return the token plus the identity of the candidate
   that actually supplied it; retain `Token` as the token-only Source contract.
4. Update `AuthTransport` to resolve once and pass the safe identity on the
   cloned request context to the base transport, including each 401 retry.
5. Re-run the focused tests and `go test ./internal/tokenauth -shuffle=on`.

## Task 2: Add the provider quota registry and wire response observations

**Files:**

- Create: `internal/github/quota.go`
- Create: `internal/github/quota_test.go`
- Modify: `internal/github/client.go`
- Modify: `internal/github/graphql.go`
- Test: `internal/github/client_test.go`
- Test: `internal/github/graphql_test.go`
- Test: `internal/github/mutation_auth_test.go`

1. Add failing registry tests proving two credentials on one host cannot
   overwrite each other, REST and GraphQL remain independent, missing headers
   do not erase a prior observation, and reserve checks reject unknown or
   insufficient pools.
2. Add failing wire tests proving App REST, user notification REST, App
   GraphQL, and user GraphQL mutation responses update only their exact pool.
3. Run the focused tests and confirm failures are caused by the missing
   registry/transport behavior.
4. Implement the mutex-protected in-memory registry, immutable snapshot
   values, header parser, quota-observing transport, and resource context marker.
5. Pass one shared registry into each GitHub REST client and GraphQL fetcher;
   mark direct GraphQL requests before transport execution.
6. Keep existing trackers temporarily for non-GitHub status and operation
   availability, but make provider scheduling/status consume the registry.
7. Re-run focused tests and `go test ./internal/github -shuffle=on`.

## Task 3: Reconcile each GitHub credential at most once every three minutes

**Files:**

- Modify: `internal/github/rate.go`
- Modify: `internal/github/client.go`
- Modify: `internal/github/sync.go`
- Test: `internal/github/sync_test.go`
- Test: `internal/server/e2etest/github_app_split_auth_test.go`

1. Add failing tests with App and user snapshots returning different values.
   Assert representative-owner context selects the App, mutation context
   selects the user, both REST and GraphQL facts update their own credential,
   and a second refresh inside three minutes performs no calls.
2. Run the focused tests and observe the ownerless/host-only implementation
   fail.
3. Expose deterministic credential prediction from the managed source/client
   without exposing or storing token bytes.
4. Give the syncer the quota registry, group configured GitHub owners by
   predicted credential, call `/rate_limit` with representative owner or
   explicit user auth, and key refresh claims by host plus credential.
5. Change the cadence constant to three minutes. Preserve prior quota on
   snapshot failure.
6. Re-run focused sync and split-auth e2e tests.

## Task 4: Gate GitHub background/archive work by the matching provider pools

**Files:**

- Modify: `internal/github/sync.go`
- Test: `internal/github/archive_lifecycle_test.go`
- Test: `internal/github/sync_test.go`

1. Add failing tests for two owners on one host: an exhausted App pool blocks
   only its repo, a healthy user pool stays eligible, unknown pools hold archive
   work, and archive hydration requires safe REST and GraphQL surplus above the
   existing 200-unit reserve.
2. Run focused tests and confirm the current host-wide tracker makes the wrong
   admission decision.
3. Resolve the predicted credential per GitHub repo and consult registry pools
   for full sync, watched sync, worker backoff, and archive admission. Retain the
   existing provider/host tracker path for non-GitHub providers.
4. Use the constraining provider reset for retry timing. Unknown GitHub quota
   pauses archive work but does not block explicit foreground paths.
5. Re-run focused tests and the full `internal/github` package.

## Task 5: Make the local emergency ceiling atomic and non-negative

**Files:**

- Modify: `internal/github/budget.go`
- Modify: `internal/github/budget_transport.go`
- Modify: `internal/platform/errors.go`
- Test: `internal/github/budget_test.go`
- Test: `internal/github/budget_transport_test.go`
- Test: `internal/github/archive_attempt_allowance_test.go`

1. Add failing concurrency tests proving simultaneous attempts cannot spend
   past the ceiling, remaining never becomes negative, archive and total spend
   reserve atomically, a refused attempt performs no provider I/O, and a 304
   refunds its pre-reservation.
2. Run the focused tests and observe post-request spending fail them.
3. Add atomic `TrySpendArchive`, clamp public remaining, and reserve before
   calling the base transport. Return a distinct local-ceiling error when the
   reservation fails; refund a successful reservation on 304.
4. Apply equivalent reservation behavior to shared non-GitHub budget
   transports if they use the same `SyncBudget` API.
5. Re-run focused budget, GitHub, and affected provider tests.

## Task 6: Replace the rate-limit API contract with provider pools plus local ceilings

**Files:**

- Modify: `internal/server/api_types.go`
- Modify: `internal/server/huma_routes.go`
- Modify: `internal/server/api_test.go`
- Modify generated OpenAPI and Go/TypeScript clients via `make api-generate`

1. Add a failing wire-level API test asserting provider host entries contain
   separately labeled credential pools with independent REST/GraphQL states,
   while local ceilings are returned in a separate map/object.
2. Run the focused server test and confirm the old flattened host response
   fails the new wire contract.
3. Implement the new response types and read only in-memory quota snapshots for
   GitHub provider pools. Continue surfacing neutral tracker facts for other
   providers without a compatibility alias for the old GitHub fields.
4. Run `make api-generate`, inspect every generated diff for the intended
   contract only, and update direct generated-client consumers.
5. Run the narrow server/API tests and route metadata tests with
   `-shuffle=on`.

## Task 7: Render provider quota and local ceilings as distinct concepts

**Files:**

- Modify: `frontend/src/lib/components/layout/StatusBar.svelte`
- Modify: `frontend/src/lib/components/layout/BudgetPopover.svelte`
- Modify: `frontend/src/lib/components/layout/BudgetBars.svelte`
- Modify: `frontend/src/lib/components/layout/budget-utils.ts`
- Modify: `frontend/src/test/mockApiFetch.ts`
- Test: relevant `frontend/src/lib/components/layout/*test*`

1. Before Svelte analysis or edits, load `svelte-code-writer` and
   `svelte-core-bestpractices` completely and run the prescribed Svelte tooling.
2. Add failing unit/component assertions that provider credential pools are
   labeled separately from a “Local sync ceiling” section and no local counter
   is described as GitHub remaining quota.
3. Run the focused Vite+ test and confirm the old flattened presentation fails.
4. Update utilities, fixtures, and Svelte components to consume the generated
   new contract and render the distinct concepts.
5. Run the Svelte autofixer/check tools required by the skills, the focused
   suite, then the full `vp test` suite after the final frontend edit.

## Task 8: Integrate startup wiring, verify, document durable invariants, and commit

**Files:**

- Modify: `cmd/middleman/provider_startup.go`
- Modify: `cmd/middleman/main.go`
- Test: `cmd/middleman/provider_startup_split_test.go`
- Modify if needed: `context/github-sync-invariants.md`

1. Add/update a failing startup test proving one shared GitHub quota registry is
   wired through the REST clients, GraphQL fetchers, and syncer while local
   budgets remain provider/host emergency ceilings.
2. Implement startup wiring and re-run focused `cmd/middleman` tests.
3. Run `gofmt` on changed Go files and review `git diff --check` plus the full
   diff, excluding `.lanes/`.
4. Run at minimum:
   `go test ./internal/tokenauth ./internal/github ./internal/server ./cmd/middleman -shuffle=on`,
   affected provider tests, `make api-generate` with a clean second diff, and
   the full frontend `vp test` suite. Use broader `make test-short` if the
   focused suite does not cover all touched packages.
5. Invoke `superpowers:verification-before-completion` before claiming success.
6. Invoke repository-local `context-sync --commit`, apply any required context
   update, then invoke the mandatory commit skill and create a new conventional
   commit without amending or bypassing hooks. Do not stage `.lanes/`.

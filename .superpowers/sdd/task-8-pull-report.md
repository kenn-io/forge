# Task 8 Pull API report

## Implementation

- Added `internal/server/pullapi.Handler` as the owner of pull-request list/detail, comments and discussions, labels, assignees, reviewers, review drafts and threads, review suggestions, approvals, state changes, merge/deferred merge, commits, files/diffs/previews, and stack routes.
- Kept explicit sync, CI-refresh, and async-sync endpoints root-local for the later `syncapi` subdivision. Those handlers consume Pull's canonical exported detail response through `Handler.BuildDetail`.
- Extended `httpapi.RepositoryResolver` with the shared provider route lookup, default-host identity, capability lookup, provider-error mapping, and platform repository-ref contract used across the extracted boundary.
- Moved the Issue/Pull shared label, assignee, and GitHub-state wire DTOs into `httpapi`, avoiding duplicate Huma schemas while retaining the existing schema names.
- Root now constructs and registers Pull through narrow dependencies, publishes a synchronized committed `ConfigSnapshot` after successful settings persistence/config reload, and shuts Pull's background work down before its lower-level Fleet/Workspace dependencies.
- Pull owns deferred-merge admission, in-flight state, supersession, worker context, wait group, event payloads, and context-bounded shutdown.
- Moved Pull-focused deferred merge, diff-review, stack-health, and mid-stack config tests into `pullapi`. Root keeps cross-domain/full `ServeHTTP` and provider-sync coverage.

## TDD and debugging record

- RED: `go test ./internal/server/pullapi -run '^TestHandlerRegistersPullRoutes$' -shuffle=on` failed because `New` and `Deps` were undefined.
- GREEN: the focused registration test passed after adding the Handler boundary (`0.296s`).
- Root test compilation initially failed because tests referenced DTOs that moved with Pull. Tests now consume exported canonical Pull DTOs; no root compatibility aliases or wrappers were added.
- The first affected `apitest` run exposed a Huma duplicate-schema panic for independently declared Issue/Pull label types. Directly sharing those wire types through `httpapi` removed the collision.
- Exact route comparison found extraction drift in comment/discussion operation IDs, the discussion reply path, and comment response statuses. Restoring `post/edit/delete-pr-comment`, `reply-to-discussion`, singular `/reply`, `201 Created`, and `204 No Content` returned the contract to parity.
- A provider-and-host-qualified Pull-list test exposed filtering drift when root had no config. The injected repo filter now retains the original nil-config behavior while still filtering configured servers.
- The first full server-tree run passed all affected packages but one unrelated Workspace tmux assertion reported `signal: killed` instead of the deterministic fake-command `exit status 1`. Read-only host inspection showed high process/tmux pressure; the focused Workspace test passed immediately. No process or tmux cleanup, assertion weakening, retry, or product change was performed. The second full tree run passed.

## Verification

- `GOMAXPROCS=24 go test ./internal/server/pullapi -parallel=8 -shuffle=on`
- `GOMAXPROCS=24 go test ./internal/server/apitest -run 'Pull|PR' -parallel=8 -shuffle=on`
- `GOMAXPROCS=24 go test ./internal/server/e2etest -run 'Pull|PR|Merge|Review|Assignee|Label' -parallel=8 -shuffle=on`
- `GOMAXPROCS=24 go test ./internal/server/... -parallel=8 -shuffle=on` — passed on the final run; root `125.039s`, Pull `2.883s`, total wall approximately `100s`.
- `go test -race ./internal/server/pullapi -shuffle=on` — passed (`4.970s`).
- `go test ./internal/server ./cmd/... -run '^$' -shuffle=on`
- `make api-generate` — passed with no generated artifact drift.
- `make testify-helper-check`
- `make lint` — zero issues.
- `scripts/context-sync --check`
- `git diff --check`

The final capped server-tree run is materially below the recorded Task 8 baselines of `500.8s` unrestricted and `337.8s` capped.

## Self-review

- Confirmed `pullapi` does not import the root `internal/server` package.
- Confirmed explicit `/sync`, `/ci-refresh`, and `/sync/async` Pull routes and handlers remain root-owned and are registered exactly once.
- Confirmed Pull paths, methods, operation IDs, response statuses, schemas, provider/host identity, capability errors, and generated clients remain unchanged.
- Confirmed deferred merge and mid-stack policy no longer read mutable root config or root-owned lifecycle state.
- Confirmed root Issue/repository helpers and repository commit-diff behavior remain in place.
- Confirmed no compatibility shim, duplicate route registration, generated-artifact drift, branch change, Roborev invocation, push, or external resource cleanup was introduced.

## Files

- Added `internal/server/pullapi/` and `internal/server/httpapi/item_mutation_types.go`.
- Removed the former root-owned Pull deferred-merge, diff-review, stack-health implementations and their root-focused tests after moving ownership.
- Updated root API composition, shared resolver contracts, settings/config publication, root sync detail assembly, and wire tests for the new boundary.

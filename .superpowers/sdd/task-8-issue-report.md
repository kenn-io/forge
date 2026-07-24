# Task 8 Issue API report

## Result

- Added `internal/server/issueapi` as the owner of Issue list, detail, create,
  content edit, comment, label, assignee, and state routes.
- Kept explicit Issue sync/enqueue routes and Issue workspace creation in the
  root composition boundary.
- Preserved the existing operation IDs, methods, paths, statuses, problem
  codes, provider/host lookup, capabilities, mutation behavior, timeline
  persistence, workspace references, workflow metadata, and response schemas.
- Reused `httpapi.RepositoryResolver` and shared mutation request/response
  types. The handler receives narrow dependencies and immutable callbacks; it
  does not retain root config.
- Removed the superseded root Issue handlers, wrappers, response types, and
  helpers. No compatibility aliases, fallback registrations, or root import
  were introduced.

## Registration ownership

The package-local registration test covers all 19 owned operation IDs and
their method/path/status contracts. It also verifies that Issue sync/enqueue,
Issue workspace, Pull, and Repo operation IDs are not registered by
`issueapi`.

Root `api_test.go` cases remain where they exercise the real `Server`
ServeHTTP/provider-sync composition. The handler registration and ownership
contract is isolated in `issueapi`.

## Verification

- TDD red: `go test ./internal/server/issueapi -run TestHandlerRegistersOnlyIssueRoutes -shuffle=on`
  failed on missing `New`/`Deps` before implementation.
- TDD green: the same command passed after registration was implemented.
- Focused Issue/provider root tests:
  `GOMAXPROCS=24 go test ./internal/server -run 'Test(API.*Issue|ProviderIssueRoute|E2E.*Issue)' -parallel=8 -shuffle=on`
  passed in 1.204s package time.
- Race:
  `GOMAXPROCS=24 go test -race ./internal/server/issueapi ./internal/server -run 'TestHandlerRegistersOnlyIssueRoutes|Test(API.*Issue|ProviderIssueRoute)' -parallel=8 -shuffle=on`
  passed (`issueapi` 1.272s, root 6.162s).
- Full server subtree, post-cleanup:
  `GOMAXPROCS=24 go test ./internal/server/... -parallel=8 -shuffle=on`
  passed in 130.31s wall time. Against the Task 8 recorded baselines, the
  current accumulated package split is 370.49s / 74.0% below the 500.8s
  unrestricted baseline and 207.49s / 61.4% below the 337.8s capped baseline.
- `make testify-helper-check`, `make lint`, `go vet ./internal/server/issueapi ./internal/server`,
  and `git diff --check` passed.
- `make api-generate` passed and all checked-in OpenAPI/Go/TypeScript client
  artifacts have zero diff.
- `scripts/context-sync --check` passed. The commit-mode server context audit
  found no durable context update: this change moves greppable ownership while
  preserving the documented contracts.

## Concerns

- The wall-time comparison reflects all package extractions already present on
  the branch, not an isolated before/after measurement for Issue alone.
- No push or Roborev command was run.

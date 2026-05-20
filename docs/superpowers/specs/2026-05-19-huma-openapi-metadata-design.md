# Huma OpenAPI Operation Metadata Coverage Design

## Goal

Make every non-Hidden operation in middleman's Huma-served OpenAPI document carry an explicit, useful `Summary`, at least one `Tag`, and a stable, unique `OperationID`, and guard that invariant with a live-OpenAPI walker test so it cannot regress.

## Why

The middleman backend registers most routes through Huma's shorthand convenience helpers (`huma.Get(api, "/pulls", s.listPulls)`). Those calls auto-generate a `Summary` and `OperationID` from the method and path and stash a marker (`_convenience_summary`, `_convenience_id`) in `Operation.Metadata` so callers can detect that the value is a default. Today nothing forces the registrations to override those defaults, so the live OpenAPI document carries:

- Verbose, path-derived `OperationID` values (e.g., `GetHostByPlatformHostPullsByProviderByOwnerByNameByNumberCommits`) that surface in the generated Go API client as method names. These can shift when paths change and are unpleasant to type and read.
- Auto-generated `Summary` strings that read like sentences scraped from the route pattern ("Get pulls by provider by owner by name by number"), not what the operation actually does.
- No `Tags`, so middleman's API docs UI (Huma serves Stoplight Elements at `/docs` by default) renders the entire API as one flat alphabetical pile with no grouping.

middleman already has an AST-level guardrail at `internal/server/route_registration_test.go:16` that blocks raw `http.ServeMux.Handle` registrations on `/api/...` paths. The metadata equivalent is missing: nothing prevents a maintainer from adding a new `huma.Get` shorthand without metadata. This design adds that guardrail at the live-OpenAPI level and backfills the existing routes to satisfy it.

## Scope

In scope:

- A Go test (suggested name `TestHumaContractMetadata`) in `internal/server/` that builds the live OpenAPI document with `server.NewOpenAPI()`, walks `Paths`, and asserts metadata coverage on every non-Hidden operation.
- A small registration-site helper that attaches `Summary`, `Tags`, and `OperationID` in one expression and is the canonical way to fill metadata on Huma convenience helpers.
- A mechanical backfill of `Summary`, `Tags`, and `OperationID` on every existing non-Hidden route reachable through `/api/v1/openapi.json`. That covers handlers registered in `huma_routes.go`, `settings_routes.go`, and (where applicable) any other file registering routes on the same `huma.API`.
- A `make api-generate` regeneration of `frontend/openapi/openapi.yaml`, `internal/apiclient/spec/openapi.json`, the Go API client, and the TypeScript schema so the new metadata flows into the checked-in artifacts.

Out of scope:

- Health endpoints (`/healthz`, `/livez`) — registered on a separate Huma API with OpenAPI/docs output disabled. They never appear in `/api/v1/openapi.json` and need no taxonomy decisions.
- Hidden operations — terminal WebSocket upgrades and the roborev proxy. They are registered through `api.Adapter().Handle` rather than `huma.Register`, so they never call `oapi.AddOperation` and never enter `api.OpenAPI().Paths` regardless of the `Hidden=true` flag. The test walks `Paths` and naturally excludes them.
- A route-inventory-from-markdown pattern. middleman has high route churn and the maintenance burden of a separate inventory exceeds its value.
- Bulk renaming of already-conforming explicit `OperationID` values. Where an existing ID conflicts with the new convention or duplicates another after backfill, this design renames it; otherwise it stays.
- Frontend changes. The TypeScript schema regenerates, but no frontend code is touched in this design.

## Design

### Tag taxonomy

Each non-Hidden operation is tagged with exactly one tag from this set:

- `Pull Requests` — every operation whose primary resource is a pull/merge request. Includes per-PR sync, CI refresh, comments, labels, approval, merge, ready-for-review, GitHub-state mutation, commits/diff/files/stack listings, and the `import-metadata` and `file-preview` reads. Host-prefixed variants get the same tag.
- `Issues` — every operation whose primary resource is an issue. Same coverage shape as Pull Requests, plus `create-issue-workspace`.
- `Repositories` — repo-level reads and mutations not handled by Settings: `list-repos`, `list-repo-summaries`, `list-repo-labels`, `comment-autocomplete`, `get-repo`, `resolve-item`, `preview-repos`, `bulk-add-repos`.
- `Settings` — configuration mutations: `get-settings`, `update-settings`, `add-repo`, `refresh-repo`, `delete-repo`, plus their host variants. Also `set-starred` and `unset-starred` because they configure the user's pinned set.
- `Sync` — global sync orchestration: `trigger-sync`, `sync-status`, `rate-limits`. Per-PR / per-issue sync remains under Pull Requests / Issues.
- `Activity` — `list-activity`.
- `Stacks` — `list-stacks`.
- `Workspaces` — workspace lifecycle and runtime operations.
- `Projects` — project + worktree registration and listing, plus `list-launch-targets`.
- `Roborev` — `get-roborev-status` only. The roborev proxy operations live on a different `huma.API` instance entirely and never reach the `/api/v1/openapi.json` document.
- `System` — `/version` and `/events` (SSE). Catch-all for cross-domain server-info endpoints.

Each route gets exactly one tag. Host-prefixed variants get the same tag as their non-host counterpart, because the `host/{platform_host}/` prefix is a URL routing concern, not a semantic one.

### Summary phrasing

Imperative-mood, present tense, first word capitalized, no trailing period.

Form: `Verb resource [qualifier]`.

Examples:

- `List pull requests`
- `Get pull request`
- `Approve pull request`
- `Set pull request kanban state`
- `Post pull request comment`
- `Trigger sync`
- `Get sync status`
- `Connect workspace terminal` — only if a Hidden route is later un-hidden; Hidden ops do not need a Summary today.

Imperative-mood summaries are the OpenAPI convention used by Stripe, GitHub, and Linear. The same resource verbiage is used in both the Summary and the OperationID, varying only by separator and capitalization, so a maintainer who writes one almost has the other.

Host-prefixed and non-host variants share the same Summary. They're the same operation; the path difference is a routing concern. Differentiating them in the Summary would read like duplication noise in the docs UI.

### OperationID convention

`verb-resource[-qualifier]`, kebab-case.

Examples:

- `list-pulls`
- `get-pull`
- `approve-pull`
- `set-pull-kanban-state`
- `post-pull-comment`
- `trigger-sync`
- `get-sync-status`

Conventions:

- Plural for collection reads (`list-pulls`, `list-issues`), singular for item reads/mutations (`get-pull`, `approve-pull`).
- Host-prefixed variants append `-on-host` so each route still has a unique OperationID. Many existing routes already follow this pattern (`approve-pull`/`approve-pull-on-host`). This is the only place where the host-vs-non-host distinction surfaces in metadata.
- Where an existing `OperationID` already conforms, keep it (e.g., `edit-pr-content`, `set-kanban-state`). Where renaming is cheaper than working around an inconsistency, rename. The test catches uniqueness violations.

Comments and labels are nested under their parent resource:

- `post-pull-comment` / `edit-pull-comment` / `set-pull-labels`
- `post-issue-comment` / `edit-issue-comment` / `set-issue-labels`

This avoids `comment`-only or `label`-only IDs that could collide once new resources gain comments.

### Test design

The test lives in `internal/server/` (suggested filename `route_metadata_test.go`, suggested function name `TestHumaContractMetadata`). It constructs the OpenAPI document with `server.NewOpenAPI()` — the same call `cmd/middleman-openapi/main.go` uses to write the checked-in `frontend/openapi/openapi.yaml` — so the test asserts against the exact same document the generated clients are built from. It needs no database, mock GitHub client, or running server because `NewOpenAPI` builds against a fresh `*Server{}`.

Procedure:

- Build the document. `openAPI := server.NewOpenAPI()` calls `s.registerAPI(api)` on a fresh `*Server{}` and returns `api.OpenAPI()`.
- Walk `openAPI.Paths`. For each `(path, *huma.PathItem)`, walk each non-nil HTTP-method operation pointer (`Get`, `Put`, `Post`, `Delete`, `Options`, `Head`, `Patch`, `Trace`).
- For each `*huma.Operation` found, append one entry to a `failures []string` slice for every check that does not pass:
  - `strings.TrimSpace(op.Summary) == ""` → `METHOD PATH: missing Summary`.
  - `strings.TrimSpace(op.OperationID) == ""` → `METHOD PATH: missing OperationID`.
  - `len(op.Tags) < 1` → `METHOD PATH: missing Tags`.
  - Any tag entry is an empty trimmed string → `METHOD PATH: empty Tag`.
  - `op.Tags` is not exactly one tag from the taxonomy above → `METHOD PATH: expected exactly one tag from the API tag taxonomy`.
  - The OperationID already appears in a `seen map[string]string` populated by prior iterations → `METHOD PATH: duplicate OperationID with METHOD PATH` (the prior entry).
- After the walk, call `assert.Empty(t, failures, strings.Join(failures, "\n"))` once. This surfaces every offending route in one failed assertion so a maintainer who breaks the test sees exactly which routes to fix and why.

The test uses testify (`require` for the document build; `assert.Empty` on the final `failures` slice). It does not run per-route assertions — collecting into the slice lets one test invocation report every offender at once rather than aborting on the first.

A negative-case smoke test in the same file verifies the assertion has teeth: it constructs a tiny in-process `huma.API` with one route registered via `huma.Get` and no metadata, walks it with the same helper used by the production test, and asserts the helper returns at least one failure. Another smoke test rejects unknown or multiple tags. A source-level AST test rejects production Huma convenience registrations (`Get`, `Post`, `Put`, `Patch`, `Delete`, `Head`, `Options`) that do not use `documentOperation`, because the live OpenAPI document cannot always distinguish an intentionally supplied summary or operation ID from Huma's generated default when the strings match.

### Source-level backfill style

A small helper attaches the metadata at the registration site:

```go
// documentOperation returns an operationHandler that sets Summary, Tags, and
// OperationID on the resulting *huma.Operation. Use it with the huma.Get/Post/
// Put/Patch/Delete convenience helpers.
func documentOperation(operationID, summary string, tags ...string) func(*huma.Operation) {
    return func(o *huma.Operation) {
        o.OperationID = operationID
        o.Summary = summary
        o.Tags = tags
    }
}
```

It lives in `internal/server/huma_routes.go` next to the other registration helpers. Callers look like:

```go
huma.Get(api, "/pulls", s.listPulls,
    documentOperation("list-pulls", "List pull requests", "Pull Requests"))
huma.Post(api, pullPath+"/approve", s.approvePR,
    documentOperation("approve-pull", "Approve pull request", "Pull Requests"))
```

Existing `huma.Register(api, huma.Operation{OperationID: "...", Method: ..., Path: ...}, h)` blocks keep their structure; they gain `Summary` and `Tags` fields inline. Where an existing block lacks `OperationID`, it gains that too. The helper is intentionally not used for `huma.Register` call sites: they already have a verbose form, and forcing the helper through them would lose the `Method`/`Path`/`DefaultStatus` clarity.

The helper's signature does not allow mistakes. `Summary` and `OperationID` are required positional parameters; `Tags` is variadic with the existing taxonomy supplied as string literals. Passing an empty tag list is possible at the type level but fails the test, which is the desired feedback loop.

### Generated-artifact regeneration

`make api-generate` updates four artifacts that the metadata flows into:

- `frontend/openapi/openapi.yaml` — checked in. Used by the TypeScript schema generator.
- `internal/apiclient/spec/openapi.json` — generated, fed into `go tool oapi-codegen` for the Go client. Not checked in.
- `internal/apiclient/generated/client.gen.go` — generated Go client. Checked in. Method names derive from `OperationID`, so the diff here will be large but mechanical.
- `packages/ui/src/api/generated/schema.ts` — generated TypeScript schema. Checked in. Tag groupings appear here as TS namespaces if the generator supports them.

The plan runs `make api-generate` once after the source backfill is complete and commits all four artifacts together so the regeneration diff is bounded and reviewable.

## Files affected

- `internal/server/route_metadata_test.go` (new) — `TestHumaContractMetadata` and the negative-case smoke test.
- `internal/server/huma_routes.go` — `documentOperation` helper; backfill on every convenience call and every existing `huma.Register` block inside `registerAPI` and `registerProviderRepoAPI`.
- `internal/server/settings_routes.go` — backfill on every block inside `registerSettingsAPI`.
- `frontend/openapi/openapi.yaml` — regenerated, checked in.
- `internal/apiclient/generated/client.gen.go` — regenerated, checked in.
- `packages/ui/src/api/generated/schema.ts` — regenerated, checked in.

On the order of a hundred routes are backfilled, split between `registerAPI`/`registerProviderRepoAPI` in `huma_routes.go` and `registerSettingsAPI` in `settings_routes.go`. Most repo-scoped routes are duplicated as host-prefixed variants and contribute two entries each.

## Risks

- **Diff size.** The regenerated `client.gen.go` and `schema.ts` will be large because every operation's generated method/type name changes. Reviewer load is mitigated by committing the source changes in one logical commit and the regeneration in a second commit so the human-edited and machine-emitted changes can be reviewed separately.
- **OperationID collisions.** Renaming an existing explicit OperationID can collide with another route in the same document. The test catches this, but it surfaces only after the change. Mitigation: the plan introduces the helper + the new OperationID in small batches per file (`huma_routes.go` first, then `settings_routes.go`) and runs the test after each batch.
- **Adapter().Handle bypass.** Routes registered through `api.Adapter().Handle(op, handler)` never reach `huma.Register`, which means they never call `oapi.AddOperation` and never enter `api.OpenAPI().Paths` regardless of whether `Hidden=true` is set. The test will not see them. middleman uses this path today for the terminal upgrades and the roborev proxy, all of which are intentional. The risk is a future maintainer adding a public route via Adapter().Handle and expecting the test to police it. Mitigation: the existing AST-level guardrail (`route_registration_test.go`) already blocks raw mux registrations; the metadata test does not extend to Adapter().Handle, and that's an explicit limitation. Adding an AST check for Adapter().Handle public routes is a separate follow-up if it becomes a real problem.
- **Huma internals shift.** The implementation intentionally avoids Huma's internal `Metadata["_convenience_*"]` markers because they are not a stable signal of maintainer intent when an explicit value matches Huma's default. The live OpenAPI walker enforces the public document shape, and the AST guardrail enforces use of `documentOperation` for convenience helpers.

## Open questions

None. All three design choices (taxonomy, value style, helper shape) have been settled in brainstorming with the codex consult agreeing on each.

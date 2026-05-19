# Huma OpenAPI Operation Metadata Coverage Design

## Goal

Make every non-Hidden operation in middleman's Huma-served OpenAPI document carry an explicit, useful `Summary`, at least one `Tag`, and a stable, unique `OperationID`, and guard that invariant with a live-OpenAPI walker test so it cannot regress.

## Why

The middleman backend registers most routes through Huma's shorthand convenience helpers (`huma.Get(api, "/pulls", s.listPulls)`). Those calls auto-generate a `Summary` and `OperationID` from the method and path and stash a marker (`_convenience_summary`, `_convenience_id`) in `Operation.Metadata` so callers can detect that the value is a default. Today nothing forces the registrations to override those defaults, so the live OpenAPI document carries:

- Verbose, path-derived `OperationID` values (e.g., `GetHostByPlatformHostPullsByProviderByOwnerByNameByNumberCommits`) that surface in the generated Go API client as method names. These can shift when paths change and are unpleasant to type and read.
- Auto-generated `Summary` strings that read like sentences scraped from the route pattern ("Get pulls by provider by owner by name by number"), not what the operation actually does.
- No `Tags`, so Swagger UI renders the entire API as one flat alphabetical pile with no grouping.

middleman already has an AST-level guardrail at `internal/server/route_registration_test.go:16` that blocks raw `http.ServeMux.Handle` registrations on `/api/...` paths. The metadata equivalent is missing: nothing prevents a maintainer from adding a new `huma.Get` shorthand without metadata. This design adds that guardrail at the live-OpenAPI level and backfills the existing routes to satisfy it.

## Scope

In scope:

- A Go test (suggested name `TestHumaContractMetadata`) in `internal/server/` that builds the live OpenAPI document with `server.NewOpenAPI()`, walks `Paths`, and asserts metadata coverage on every non-Hidden operation.
- A small registration-site helper that attaches `Summary`, `Tags`, and `OperationID` in one expression and is the canonical way to fill metadata on Huma convenience helpers.
- A mechanical backfill of `Summary`, `Tags`, and `OperationID` on every existing non-Hidden route reachable through `/api/v1/openapi.json`. That covers handlers registered in `huma_routes.go`, `settings_routes.go`, and (where applicable) any other file registering routes on the same `huma.API`.
- A `make api-generate` regeneration of `frontend/openapi/openapi.yaml`, `internal/apiclient/spec/openapi.json`, the Go API client, and the TypeScript schema so the new metadata flows into the checked-in artifacts.

Out of scope:

- Health endpoints (`/healthz`, `/livez`) — registered on a separate Huma API with OpenAPI/docs output disabled. They never appear in `/api/v1/openapi.json` and need no taxonomy decisions.
- Hidden operations — terminal WebSocket upgrades and the roborev proxy. Hidden ops never enter the OpenAPI document (`internal/server/huma_routes.go` and the Huma runtime both honor `Hidden=true`), so the test naturally excludes them.
- A route-inventory-from-markdown pattern. middleman has high route churn and the maintenance burden of a separate inventory exceeds its value.
- Renaming the existing already-explicit `OperationID` values that already match the convention. Where an existing ID conflicts with the new convention or duplicates another, this design renames it; otherwise it stays.
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
- `Roborev` — `get-roborev-status` only. The proxy operations are Hidden.
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

This matches the OpenAPI convention used by Stripe, GitHub, and Linear, and matches Huma's own `GenerateSummary` shape so the diff against the auto-generated values is small. The same resource verbiage is used in both the Summary and the OperationID, varying only by separator and capitalization.

Host-prefixed and non-host variants share the same Summary. They're the same operation; the path difference is a routing concern. Differentiating them in the Summary would read like duplication noise in Swagger UI.

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

The test lives in `internal/server/` (suggested filename `route_metadata_test.go`, suggested function name `TestHumaContractMetadata`). It does not need a database, mock GitHub client, or a running server: it constructs the OpenAPI document with `server.NewOpenAPI()`, which is already used by `cmd/middleman-openapi/main.go`.

Procedure:

1. Build the document: `openAPI := server.NewOpenAPI()`. This calls `s.registerAPI(api)` on a fresh `*Server{}` and returns `api.OpenAPI()`.
2. Walk `openAPI.Paths`. For each `(path, *huma.PathItem)`, walk each non-nil HTTP-method operation pointer (`Get`, `Put`, `Post`, `Delete`, `Options`, `Head`, `Patch`, `Trace`).
3. For each `*huma.Operation` found:
   - Assert `strings.TrimSpace(op.Summary) != ""`.
   - Assert `op.Metadata["_convenience_summary"] == nil`. This is what catches auto-generated values that happen to be non-empty.
   - Assert `strings.TrimSpace(op.OperationID) != ""` and `op.Metadata["_convenience_id"] == nil`.
   - Assert `len(op.Tags) >= 1` and each entry is a non-empty trimmed string.
   - Record `op.OperationID -> "METHOD PATH"` in a `map[string]string`. If the key already exists, record the collision.
4. After the walk, collect failures into a slice and call `assert.Empty` on it. The failure message lists every failing route as `METHOD PATH: <issue>` so a maintainer who breaks the test sees exactly which route to fix and why (`missing Summary`, `auto-generated OperationID`, `missing Tags`, `duplicate OperationID with METHOD PATH`).

The test uses testify (`require` for the document build; `assert` plus a local `assert := assert.New(t)` helper for per-route checks so multiple failures surface in one run).

A negative-case smoke test in the same file verifies the assertion has teeth: it constructs a tiny in-process `huma.API` with one route registered via `huma.Get` and no metadata, walks it with the same helper used by the production test, and asserts the helper returns at least one failure. This guards against the test becoming a no-op if Huma changes how it marks auto-generated values.

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

Roughly 130 routes in total are backfilled (~100 reachable through `registerAPI`/`registerProviderRepoAPI` plus ~10 under `registerSettingsAPI`, with host-variant doubling on most repo-scoped routes).

## Risks

- **Diff size.** The regenerated `client.gen.go` and `schema.ts` will be large because every operation's generated method/type name changes. Reviewer load is mitigated by committing the source changes in one logical commit and the regeneration in a second commit so the human-edited and machine-emitted changes can be reviewed separately.
- **OperationID collisions.** Renaming an existing explicit OperationID can collide with another route in the same document. The test catches this, but it surfaces only after the change. Mitigation: the plan introduces the helper + the new OperationID in small batches per file (`huma_routes.go` first, then `settings_routes.go`) and runs the test after each batch.
- **Hidden marker false negatives.** If a future maintainer adds a route via `api.Adapter().Handle` without setting `Hidden=true` and without going through `huma.Register`, the operation might bypass the OpenAPI document entirely (`api.OpenAPI().Paths` won't see it) and the test won't notice. This is the same gap the existing AST-level guardrail addresses (it blocks raw mux registrations); the AST test continues to cover it. No new mitigation needed.
- **Huma internals shift.** If Huma changes the `Metadata["_convenience_*"]` marker key or removes it entirely, the test loses its primary signal for distinguishing auto-generated values. The negative-case smoke test catches this on the next Huma upgrade. If the marker disappears, the test falls back to checking `op.Summary != huma.GenerateSummary(method, path, response)`, but that path is not implemented in v1 because the marker is the cleaner signal.

## Open questions

None. All three design choices (taxonomy, value style, helper shape) have been settled in brainstorming with the codex consult agreeing on each.

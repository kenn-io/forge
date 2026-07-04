# Middleman MCP Server

## Problem

Middleman already keeps a useful local cache of maintainer work: synced pull
requests, issues, activity rows, notifications, PR kanban state, workspace
links, stacks, CI snapshots, and timeline events. External model-driven tools
should be able to use that cache without scraping the UI or learning the full
REST API.

The first use case is a periodic local system that asks a model to inspect
recent cached activity, find pull requests or issues that look worth reviewing,
and mark selected items as `reviewing` in middleman-local workflow state. The
periodic system itself is out of scope. Middleman only needs to provide the MCP
primitives that make such a system safe and useful.

The MCP server must not become a mirror of the full middleman HTTP API. The
surface should be task-shaped, small, and explicitly model-friendly.

## Goals

- Add a `middleman mcp` companion command that exposes middleman data through
  MCP.
- Support both stdio and HTTP MCP transports.
- Use the running middleman daemon as the data and mutation authority.
- Expose recent cached activity and compact PR/issue review candidates.
- Expose the tracked repository inventory so provider-aware filters are
  discoverable instead of guessed.
- Expose cached PR/issue search so clients can find quiet items, not only
  recently active ones.
- Expose cached PR/issue details when the model has selected an item.
- Expose PR diff evidence as a compact per-file summary, with an opt-in
  full-diff temp-file handoff for local inspection.
- Expose PR stack context so a model can reason about review order.
- Expose middleman-local workflow state for both PRs and issues.
- Preserve the existing PR kanban API and UI behavior.
- Let MCP clients set local item workflow state, including `reviewing`.
- Include workspace and stack context because those affect review decisions.
- Include a guidance document for configuring and using the MCP capabilities.

## Success Criteria

- A stdio MCP client can connect to `middleman mcp`, list review candidates,
  fetch compact cached context for one candidate, and claim it as `reviewing`.
- The same workflow-state write is visible through middleman's daemon API and
  SQLite-backed state.
- An MCP claim with `expected_status = "new"` succeeds for an item that has no
  stored workflow row, because missing state is treated as effective `new`.
- An MCP claim with stale `expected_status` returns a conflict and does not
  overwrite the current local state.
- An MCP client can list tracked repositories, search cached PRs/issues by
  text, and fetch a per-file diff summary for a PR without any provider call.
- With `emit_diff_file` set, the diff tool writes the full unified diff to a
  companion-owned `0600` temp file and returns its path; the file is removed
  on companion shutdown.
- Existing PR kanban list/detail and state mutation behavior remains compatible
  with current clients and the board UI.
- No MCP tool can perform provider writes.
- The HTTP MCP transport accepts only loopback, token-authenticated,
  same-origin requests.

## Non-Goals

- Building or scheduling the periodic system.
- Calling provider APIs from the MCP companion.
- Performing provider writes through MCP, including comments, labels, reviews,
  merges, workflow approval, issue edits, or PR edits.
- Exposing an arbitrary "call any middleman API" MCP tool.
- Mirroring the full OpenAPI surface as MCP tools.
- Designing a new issue kanban UI.
- Adding workflow-state history in this version.
- Exposing Kata, Docs, Messages, repo browser, terminal, or fleet controls
  through MCP.
- Supporting non-loopback remote MCP access in v1.

## Recommended Approach

Add `middleman mcp` as a companion process. The command starts an MCP server
using stdio by default, with an HTTP transport option for clients that require
it. Both transports expose the same tools, resources, and prompts. The companion
discovers the running middleman daemon through the same
runtime metadata and auth-token files used by `middleman api`, then talks to
the daemon over loopback.

Use the official Go MCP SDK, `github.com/modelcontextprotocol/go-sdk/mcp`, for
v1. The SDK provides the server abstraction and transports middleman needs, and
keeps protocol details out of the application code. If the SDK cannot satisfy
the stdio and tokenized loopback HTTP requirements during implementation, stop
and update the design rather than hand-rolling JSON-RPC silently.

Do not open SQLite directly from the MCP companion. The daemon remains
authoritative for cached data, local workflow writes, auth, host validation,
problem envelopes, provider-aware repository identity, and response shaping.

Add one new middleman-owned workflow-state model for provider PRs and issues.
Existing PR kanban behavior is preserved by backing the current PR kanban route
and board data with the new generic state. Issues use the same status vocabulary
as PRs: `new`, `reviewing`, `waiting`, and `awaiting_merge`.

## Alternatives Considered

### Direct DB-Backed MCP Companion

The companion could open middleman's SQLite database directly and read cached
state from WAL mode.

This is rejected for v1. Direct reads would duplicate response-shaping logic,
provider identity filtering, issue/PR detail behavior, workspace lookups, and
future schema assumptions. A direct process can also accidentally become a
second migrator or writer. The daemon API is the safer boundary.

### MCP Inside The Main Daemon

The daemon could expose MCP directly beside `/api/v1`.

This is rejected for v1. It mixes MCP transport and origin/auth concerns into
the main server lifecycle. A companion process is easier to configure in MCP
clients, keeps the main daemon focused, and can still use HTTP transport when
needed.

### Full OpenAPI-To-MCP Mapping

Middleman already has a large OpenAPI document, so the MCP server could
generate tools from every operation.

This is rejected. The desired MCP use case needs curated maintainer primitives,
not every mutation and detail path. A full mapping would expose provider writes
that are explicitly out of scope and would push too much API selection burden
onto the model.

## Architecture

### Process Model

`middleman mcp` runs as a separate process launched by an MCP client or by a
local supervisor.

Default mode:

```bash
middleman mcp
```

This starts a stdio MCP server and discovers the daemon from the default
middleman config path.

Useful flags:

```bash
middleman mcp --config /path/to/config.toml
middleman mcp --transport stdio
middleman mcp --transport http --addr 127.0.0.1:0 --http-token-env MIDDLEMAN_MCP_TOKEN
```

These flags are the user-facing contract:

- `stdio` is the default.
- HTTP binds only to loopback in v1.
- HTTP requires `--http-token-env` and refuses to start when that environment
  variable is unset or blank.
- Port `0` is allowed for an ephemeral local HTTP listener.
- The companion reads daemon runtime metadata and auth token from `data_dir`.
- If no daemon is running, tools return a clear daemon-unavailable error.

### Responsibility Split

The daemon owns:

- cached SQLite data;
- repository identity and provider-host normalization;
- activity feed queries;
- PR and issue list/detail behavior;
- workspace and stack lookup;
- generic local workflow state;
- PR kanban route compatibility;
- API auth, CSRF, host checks, and problem envelopes.

The MCP companion owns:

- MCP protocol handling;
- stdio/HTTP transport startup;
- MCP tool, resource, and prompt definitions;
- daemon discovery and authenticated loopback requests;
- compact model-oriented response shapes;
- candidate grouping from daemon responses;
- translating daemon problem documents into MCP errors.

### MCP Library And HTTP Safety

The HTTP MCP transport is local-only in v1. The companion should reject
non-loopback bind addresses rather than inheriting the daemon's broader bind
options. Remote MCP access can be designed later with explicit auth and origin
policy.

HTTP MCP requests must be protected independently from the daemon auth token:

- require `Authorization: Bearer <token>` where `<token>` comes from
  `--http-token-env`;
- require the `Host` header to match the listener address, with loopback
  aliases accepted only for the actual bound port;
- reject browser requests whose `Origin` is present and not the same loopback
  origin;
- do not emit permissive CORS headers;
- do not include the daemon API auth token in MCP responses, HTTP errors, or
  logs.

Errors that mention daemon discovery paths should avoid leaking secrets.

## Local Workflow State

### Model

Introduce a canonical generic workflow-state table for provider items:

```text
middleman_item_workflow_state
  repo_id
  item_type        -- "pr" or "issue"
  item_number
  status           -- "new", "reviewing", "waiting", "awaiting_merge"
  updated_at
  updated_source   -- "ui", "api", "mcp", or another local caller label
  updated_actor    -- optional user/client/agent label, max 120 bytes
  updated_reason   -- optional short free-text reason, max 500 bytes
```

`(repo_id, item_type, item_number)` is unique. `repo_id` preserves the existing
provider-aware identity rule through the `middleman_repos` row. Datetimes remain
UTC across storage and API boundaries.

The existing `middleman_kanban_state` table is no longer the canonical storage
after migration. Existing rows are migrated into
`middleman_item_workflow_state` as `item_type = "pr"` by joining through
`middleman_merge_requests`. No new compatibility SQL view or duplicate-write
shim is introduced. Public API compatibility is maintained at the Go/API layer,
not by keeping two state stores live.

### Status Vocabulary

Both PRs and issues accept:

- `new`
- `reviewing`
- `waiting`
- `awaiting_merge`

The vocabulary intentionally matches the current PR kanban states, even though
`awaiting_merge` is less natural for issues. Keeping one vocabulary makes MCP
instructions and state filtering simple.

### Metadata

Workflow state stores last-writer metadata, not a full transition log.

`updated_source` identifies the local surface that changed the state. It is
validated against a short allowlist or a conservative identifier pattern and is
stored with a 40-byte maximum. MCP writes use `mcp`. `updated_actor` should be
the MCP client name or supplied agent label when available. `updated_reason` is
optional and should be short enough to show in future UI without becoming an
unbounded log.

The API should also accept an optional expected current status. The comparison
is against the effective current status, where a missing workflow row is
`new`. If provided and the effective status has changed, the daemon returns a
conflict. This lets a periodic model avoid overwriting a human or another agent
that already moved the item while still allowing the first claim of a never-moved
item with `expected_status = "new"`.

### PR Kanban Compatibility

Existing PR list/detail responses continue to expose `KanbanStatus` with the
same wire behavior as today, byte-compatible with current clients:

- missing workflow rows read as `new` on PR list/detail wire responses: the
  DB layer scans an empty string for a missing row, and the existing
  response-layer normalization converts empty and unexpected values to
  `new` before serialization, exactly as today (existing server API tests
  assert `new` on both list and detail);
- the empty string for missing rows is an internal DB-layer detail only and
  must never reach the wire;
- the same effective-`new` rule drives filtering, workflow-state listings,
  and `expected_status` comparisons;
- `PUT /pulls/{provider}/{owner}/{name}/{number}/state` and the host-prefixed
  variant keep their current request and response shape.

Internally those paths read and write `middleman_item_workflow_state` with
`item_type = "pr"`.

Issues gain the same local workflow state, but v1 does not require a new issue
board or visible issue-state UI. Issue list/detail responses expose
`WorkflowStatus` and local workflow metadata so API consumers do not need to
special-case PRs for local review state.

## Daemon API Additions

The companion should use existing daemon routes where they already fit:

- `GET /activity`
- `GET /repos/summary`
- `GET /pulls` (including the `q` search filter)
- `GET /pulls/{provider}/{owner}/{name}/{number}`
- `GET /pulls/{provider}/{owner}/{name}/{number}/diff`
- `GET /pulls/{provider}/{owner}/{name}/{number}/files`
- `GET /issues` (including the `q` search filter)
- `GET /issues/{provider}/{owner}/{name}/{number}`
- `GET /pulls/{provider}/{owner}/{name}/{number}/stack`
- `GET /workspaces`

Add focused daemon endpoints only for the generic local workflow state:

- `GET /workflow-state`
- `PUT /workflow-state/{item_type}/{provider}/{owner}/{name}/{number}`
- host-prefixed variants for non-default provider hosts

`GET /workflow-state` supports repo, item type, state, `include_closed`, limit,
and cursor filters. It returns compact provider-aware item refs and last-writer
metadata joined to PR/issue title, state, URL, author, and last activity. It
treats missing rows as `new`, so `state=new` includes open items that have never
been moved. It is not a replacement for PR/issue list endpoints.

Default listing semantics:

- closed/merged PRs and closed issues are excluded unless `include_closed` is
  true;
- explicit workflow rows sort by `updated_at DESC`, then item
  `last_activity_at DESC`;
- generated `new` rows without workflow storage sort by item
  `last_activity_at DESC`;
- ties break by `(platform, platform_host, owner, name, item_type, number)`;
- pagination uses an opaque cursor carrying the ordering tuple, not offset.

`PUT /workflow-state/...` validates the item type, state, provider-aware route,
and item existence. It writes only middleman-local state. It never calls a
provider mutator.

The existing PR kanban route remains because it is part of the current public
API and UI contract.

## MCP Surface

The MCP server exposes curated tools, resources, and prompts. It does not expose
generic HTTP passthrough.

Every tool that takes an item or repo ref matches it by the full identity
`(provider, platform_host, owner, name, number)`. When `platform_host` is
present and differs from the provider's default host, the companion must call
the daemon's `/host/{platform_host}/...` route variants; when it is omitted,
the default-host routes are used. This applies to every ref-taking tool,
including diff and stack context.

### Tool: `middleman_find_review_candidates`

Find compact PR/issue candidates with recent cached activity.

Inputs:

- `since`: RFC3339 timestamp or duration string such as `24h`; default `24h`.
- `repo`: optional provider-aware repo filter, supporting the same logical shape
  as middleman activity filters.
- `item_types`: optional list of `pr`, `issue`; default both.
- `workflow_states`: optional included local workflow states.
- `exclude_workflow_states`: optional excluded local workflow states.
- `include_drafts`: PR draft inclusion flag; default false.
- `include_closed`: include closed/merged PRs and closed issues; default false.
- `limit`: default 25, capped by the companion.
- `activity_types`: optional activity type filter.

Behavior:

1. Call the daemon activity endpoint for cached activity since the requested
   time.
2. Keep only PR/issue-anchored rows by default.
3. Group rows by `(platform, platform_host, owner, name, item_type, number)`.
4. Fetch compact current item state for the grouped items.
5. Drop closed or merged items unless `include_closed` is true.
6. Attach local workflow state, workspace presence, PR stack summary, and a
   small activity reason summary.
7. Order by latest activity time descending.

The tool does not score candidates with business policy. It returns enough
evidence for the model to decide.

Response shape:

```json
{
  "candidates": [
    {
      "item": {
        "type": "pr",
        "provider": "github",
        "platform_host": "github.com",
        "owner": "acme",
        "name": "widget",
        "repo_path": "acme/widget",
        "number": 42,
        "title": "Fix retry budget accounting",
        "url": "https://github.com/acme/widget/pull/42",
        "state": "open",
        "author": "alice",
        "is_draft": false
      },
      "workflow": {
        "status": "new",
        "updated_at": "",
        "updated_source": "",
        "updated_actor": "",
        "updated_reason": ""
      },
      "activity": {
        "latest_at": "2026-07-01T14:12:00Z",
        "event_count": 3,
        "types": ["comment", "commit"],
        "actors": ["bob", "alice"],
        "reasons": [
          "bob commented",
          "alice pushed commits"
        ]
      },
      "workspace": {
        "exists": true,
        "id": "ws_..."
      },
      "stack": {
        "present": true,
        "position": 2,
        "size": 4,
        "health": "blocked"
      },
      "cache": {
        "detail_loaded": true,
        "detail_fetched_at": "2026-07-01T14:00:00Z"
      }
    }
  ],
  "capped": false
}
```

### Tool: `middleman_get_item_context`

Return cached detail for one PR or issue after the model has selected it.

Inputs:

- provider-aware item ref;
- `event_limit`, default 30;
- booleans for `include_events`, `include_checks`, `include_workspace`,
  `include_stack`; defaults favor useful PR review context without returning
  every cached event.

Behavior:

- PRs use the daemon PR detail route.
- Issues use the daemon issue detail route.
- The tool returns cached data only. It does not trigger sync.
- The companion performs v1 filtering after fetching one selected item from the
  existing detail routes. `event_limit` and include flags limit the MCP response
  shape, not the daemon payload.
- Because the tool is only used after candidate narrowing, one full cached
  detail fetch per selected item is acceptable for v1. If implementation needs
  bulk context extraction or the cached event payload becomes too large, add a
  focused daemon context endpoint before expanding MCP behavior.
- The response includes `detail_loaded` and `detail_fetched_at` so the model can
  decide whether stale or missing detail should reduce confidence.

### Tool: `middleman_set_item_workflow_state`

Set middleman-local workflow state for one PR or issue.

Inputs:

- provider-aware item ref;
- `status`;
- optional `expected_status`;
- optional `reason`;
- optional `actor`.

Behavior:

- Calls the daemon workflow-state endpoint.
- Writes only local middleman state.
- Uses `updated_source = "mcp"`.
- Returns the previous and new status plus metadata.
- Returns conflict when `expected_status` does not match.

This is the only v1 MCP write tool.

### Tool: `middleman_list_activity`

Return raw recent cached activity rows for cases where a client wants to inspect
the feed directly instead of candidate grouping.

Inputs mirror the relevant subset of `/activity`: `since`, `repo`, `types`,
`search`, `limit`, and cursor.

The response stays compact and should not include full bodies beyond existing
activity previews.

### Tool: `middleman_list_items_by_workflow_state`

List PRs and issues by local workflow state.

Inputs:

- `states`
- `item_types`
- `repo`
- `include_closed`, default false
- `limit`
- `cursor`

This lets a model answer questions such as "what am I already reviewing?" or
"what did another agent mark waiting?" without scanning all cached PRs/issues.

### Tool: `middleman_list_repos`

List the repositories middleman tracks. Every other tool's `repo` filter takes
a provider-aware identity; this tool is how a model discovers the valid values
instead of guessing them.

Inputs: none beyond an optional `limit`.

The response wraps `GET /repos/summary` into compact rows:

- `provider`, `platform_host`, `owner`, `name`, `repo_path`;
- open PR and issue counts;
- `last_sync_completed_at` and the last sync error, if any.

Guidance should tell periodic agents to call this first: it doubles as a
staleness map, since a repo whose last sync failed or is old should reduce
confidence in candidates from that repo.

### Tool: `middleman_search_items`

Search cached PRs and issues by text. Candidates only surface recently active
items; this tool answers "find the PR about X" for items that have been quiet.

Inputs:

- `query`: required text query;
- `item_types`: optional list of `pr`, `issue`; default both;
- `repo`: optional provider-aware repo filter;
- `state`: `open`, `closed`, `merged`, or `all`; default `open`;
- `limit`: default 25, capped by the companion.

State semantics: the daemon list routes accept `open`, `closed`, and `all`
only, and issues have no merged state. `merged` is therefore PR-only: the
companion narrows the search to PRs, queries the daemon with `state=all`, and
keeps only PRs whose cached state is `merged`. Issues are skipped for
`state=merged` without error.

Behavior:

- PRs use `GET /pulls` with `q`; issues use `GET /issues` with `q`. Each
  source is fetched with the tool `limit`, then the companion merges the two
  lists, orders by item `last_activity_at` descending with ties broken by
  `(platform, platform_host, owner, name, item_type, number)`, and truncates
  to `limit`. Ordering is deterministic; v1 intentionally has no pagination —
  a capped flag reports truncation.
- Results are compact item refs with title, state, author, URL, local workflow
  status, and `last_activity_at`. Search never returns bodies or events; the
  model follows up with `middleman_get_item_context`.
- The tool searches cached data only. It does not query provider search APIs.

### Tool: `middleman_get_item_diff`

Return diff evidence for one PR: always a compact per-file summary, and
optionally the full unified diff written to a local temp file whose path is
returned so the model can inspect it with its own file tools.

Inputs:

- provider-aware PR ref (`item_type` must be `pr`; issue refs return an
  invalid-item error);
- `emit_diff_file`: boolean, default false.

Whitespace-only detection is intentionally out of scope for this tool: the
files route does not compute it and the review use case does not need
whitespace-aware line counts. The tool passes the daemon's per-file rows
through without whitespace filtering or whitespace counts.

Summary response:

```json
{
  "stale": false,
  "total_additions": 120,
  "total_deletions": 45,
  "files": [
    {
      "path": "internal/db/queries.go",
      "old_path": "",
      "status": "modified",
      "is_binary": false,
      "is_generated": false,
      "additions": 80,
      "deletions": 20
    }
  ],
  "diff_file": {
    "path": "/tmp/middleman-mcp-…/pr-42.diff",
    "bytes": 24576
  }
}
```

Behavior:

- The summary uses `GET /pulls/{...}/files`; per-file patches and hunks are
  never inlined into the MCP response.
- With `emit_diff_file`, the companion fetches `GET /pulls/{...}/diff` (a
  structured JSON response with per-file patch text, not raw diff bytes) and
  serializes it into one unified diff file. The patch text is the single
  canonical serialization form: on the diff route, the daemon guarantees
  every changed file's `patch` value is a complete per-file section —
  starting with its own `diff --git` header, carrying the extended headers
  git would emit (`rename from`/`rename to`, `copy from`/`copy to`,
  `old mode`/`new mode`, real new/deleted file modes) with git-style path
  quoting, `---`/`+++` lines and hunks only when the file has content
  changes, and a `Binary files <a> and <b> differ` line for binary files.
  This guarantee is scoped to patch-serving routes; the files summary route
  stays metadata-only with empty patch fields. The companion concatenates `patch` values verbatim in daemon
  response order, adding and synthesizing nothing; a changed file with an
  empty `patch` is a daemon bug and the tool must fail rather than emit a
  partial diff. File modes are deliberately NOT exposed as separate API
  fields — one representation only, so structured metadata and patch text
  cannot drift apart.
  Fidelity rule: the emitted file is a faithful serialization of everything
  the daemon's cached diff data carries — no changed file may be silently
  dropped, and rename-only, copy-only, and mode-only changes keep their
  extended headers — but it is
  a review artifact, not guaranteed byte-identical to `git diff` output. The
  file lands under a companion-owned temp directory with `0600` permissions
  and the tool returns the absolute path and size. `diff_file` is omitted
  when the flag is false.
- Temp files are ephemeral: the companion creates one private directory per
  process, overwrites per-item files on repeat calls, and removes the
  directory on shutdown. Clients must not treat the path as durable.
- The temp-file handoff assumes the MCP client shares the companion's
  filesystem. That holds for stdio and for the loopback-only HTTP transport in
  v1; a future remote transport must revisit this tool before reusing it.
- Diff routes are backed by the daemon's local clone manager. When the daemon
  reports diff unavailable (clone manager not configured, commit not found),
  the tool returns a typed diff-unavailable error rather than an empty
  summary. The `stale` flag from the daemon is passed through untouched.

### Tool: `middleman_get_stack_context`

Return stack context for one PR so a model can reason about review order, not
just membership.

Inputs: provider-aware PR ref.

Behavior:

- Wraps the provider-aware per-PR stack route,
  `GET /pulls/{provider}/{owner}/{name}/{number}/stack` (host-prefixed variant
  for non-default hosts). The repo-wide `GET /stacks` list is not used: it
  filters by owner/name only, so with the same owner/name on multiple
  providers or hosts it could select the wrong stack and violate the repo
  identity invariant.
- Returns `present: false` when the PR is not part of a stack.
- When present, returns the stack health plus ordered members, each with
  number, title, state, draft flag, and local workflow status, and marks which
  member is the requested PR.

This complements the four-field stack summary in candidate rows: candidates
say "this PR is in a stack"; this tool answers "what should be reviewed before
it".

### Resource: `middleman://mcp/guidance`

Expose the guidance document content as an MCP resource so a client can load the
recommended usage patterns.

### Prompt: `middleman-review-candidates`

Provide a reusable prompt template for periodic review triage. The prompt should
tell the model to:

- call `middleman_list_repos` first to learn valid repo filters and sync
  freshness;
- use `middleman_find_review_candidates`;
- inspect details only for plausible items;
- use `middleman_get_item_diff` to check the size and shape of a change before
  claiming it, requesting the full diff file only when the summary is not
  enough;
- consult `middleman_get_stack_context` before claiming a stacked PR so
  review order respects the stack;
- prefer cached evidence over assumptions;
- avoid provider writes;
- set workflow state only when the reason is clear;
- include `expected_status` when marking an item;
- treat `awaiting_merge` as a PR-oriented state and avoid setting it on issues
  unless the user prompt explicitly asks for that state;
- report uncertainty and stale-cache signals.

## Candidate Semantics

The candidate tool is evidence gathering, not policy.

It should summarize why an item surfaced using recent activity rows:

- new PR or issue;
- comment;
- review;
- commit;
- force push;
- linked notification rows;
- issue comment.

Repo-level default-branch activity is not a review candidate by default because
it is not anchored to a PR or issue. A later design can expose repository-level
watch candidates separately.

The tool should include activity counts and latest actors, but should avoid
large bodies. Full event bodies are available through the detail tool when the
model selects an item.

## Error Handling

Daemon problem documents map to MCP errors with stable, concise messages and
structured details where the MCP library supports them.

Important cases:

- daemon unavailable;
- daemon auth token missing or rejected;
- invalid provider-aware item ref;
- item not found;
- invalid workflow status;
- expected-status conflict;
- daemon route unavailable due to version mismatch;
- daemon timeout.

Provider errors should appear only on read paths that depend on cached daemon
behavior. MCP workflow writes never call provider APIs.

## Timeouts, Retries, And Compatibility

Each MCP tool call should use a bounded daemon request timeout. The default is
10 seconds, configurable by a `--daemon-timeout` flag. Candidate discovery and
detail reads may retry once on a transient connection failure after rediscovering
daemon runtime metadata. Workflow writes do not retry automatically because a
retry could obscure whether a local state transition was applied before the
connection failed.

Daemon discovery and capability checking are lazy and uniform across
transports: companion startup never contacts the daemon, and `middleman mcp`
starts successfully with no daemon running — tools then return a clear
daemon-unavailable error when called. The workflow-state capability probe
runs on the first workflow tool call after a successful discovery, before
that tool executes. Its result is cached keyed by the daemon identity from
runtime metadata (PID plus start time), so a daemon restart or upgrade while
the companion stays alive invalidates the cache and triggers a re-probe.

If the daemon is older than the MCP companion expects, the workflow tools
return a version/capability error that names the missing route or capability.
The companion must not silently downgrade into partial semantics that change
tool behavior.

## Staleness And Cache Signals

Every candidate/detail response should make cache state explicit where the
daemon can provide it:

- item `last_activity_at`;
- PR/issue `detail_loaded`;
- `detail_fetched_at`;
- repository `last_sync_completed_at` when available;
- whether the activity response was capped.

The MCP server must not hide stale or missing detail. The guidance doc should
teach users to treat stale cache as lower confidence, not as absence of
activity.

## Guidance Document

Implementation should add `docs/middleman-mcp.md`.

The guidance doc should cover:

- how to configure an MCP client for `middleman mcp` stdio;
- how to use the HTTP transport locally when needed;
- that the MCP server reads cached middleman data and does not force provider
  refreshes;
- that v1 writes only middleman-local workflow state;
- example periodic-agent flows;
- safe prompts for "find recent review candidates";
- discovering repo filters with `middleman_list_repos` before filtering other
  tools;
- finding quiet items with `middleman_search_items` when activity-based
  candidates are not enough;
- inspecting diffs via the summary-first flow and the temp-file handoff,
  including that diff files are ephemeral and local to the companion host;
- when to mark an item `reviewing`;
- how to use `expected_status` to avoid overwriting humans or other agents;
- how to inspect already reviewing/waiting items;
- how to interpret stale cache fields;
- troubleshooting daemon discovery and auth errors.

Example guidance flow:

```text
1. Call middleman_find_review_candidates with since equal to the scheduler's
   last successful run.
2. For the top candidates, call middleman_get_item_context.
3. Decide whether the activity needs human or agent review.
4. If claiming the item, call middleman_set_item_workflow_state with
   status="reviewing", expected_status from the candidate row, and a short
   reason.
5. Report what was claimed and what was skipped.
```

## Testing

Backend tests:

- migration test copies existing PR kanban rows into generic workflow state;
- DB query tests cover PR and issue workflow state reads/writes;
- DB query tests prove missing state reads as `new` where public responses need
  that behavior;
- DB/API tests prove `expected_status = "new"` succeeds when no workflow row
  exists and stale `expected_status` conflicts;
- server API tests cover workflow-state GET/PUT, host-prefixed identity,
  invalid status, missing item, closed-item filtering, deterministic cursor
  pagination, and expected-status conflict;
- existing PR kanban API tests continue to pass against the generic store;
- issue list/detail API tests cover `WorkflowStatus` and local workflow
  metadata exposure.

MCP tests:

- protocol/tool registration test lists exactly the curated tools/resources;
- candidate grouping test uses controlled daemon responses and asserts compact
  grouped output;
- detail tool test verifies event limiting and stale-cache fields;
- workflow write tool test verifies local-only request shape and conflict
  mapping;
- repo listing tool test maps `/repos/summary` rows to compact refs including
  sync freshness fields;
- search tool test merges PR and issue results from controlled daemon
  responses in `last_activity_at` order and never includes bodies;
- diff tool tests cover the summary-only default, `emit_diff_file` writing a
  `0600` file inside the companion temp directory and returning its path,
  issue-ref rejection, stale-flag passthrough, and the typed diff-unavailable
  error;
- diff temp directory lifecycle test verifies per-item overwrite on repeat
  calls and removal on companion shutdown;
- stack context tool test covers `present: false` and ordered members with the
  requested PR marked;
- daemon discovery/auth tests reuse the runtime metadata pattern used by the
  API CLI where possible.
- full-stack stdio MCP e2e starts a real middleman daemon against SQLite,
  connects through MCP, lists candidates from seeded cached data, performs a
  workflow-state write, and verifies the state through the daemon API; the
  same e2e also exercises `middleman_list_repos`, `middleman_search_items`,
  `middleman_get_item_context`, `middleman_get_item_diff` (summary and
  emitted file), and `middleman_get_stack_context` against the real daemon so
  route selection, JSON casing, and temp-file output are proven outside
  fake-daemon unit tests;
- HTTP transport e2e covers token-required startup, non-loopback bind rejection,
  accepted loopback requests with a bearer token, and rejected missing-token or
  cross-origin requests.

CLI tests:

- `middleman mcp` defaults to stdio;
- HTTP transport rejects non-loopback bind addresses;
- unavailable daemon produces a clear error without exposing secrets.

Generation:

- run `make api-generate` after adding daemon API endpoints.

No Playwright coverage is required unless implementation changes visible UI.

## Rollout

1. Add the schema migration that creates generic workflow-state storage and
   copies existing PR kanban rows.
2. Add DB query helpers for generic PR/issue workflow state, including effective
   `new`, expected-status conflicts, metadata limits, closed filtering, and
   cursor ordering.
3. Update PR kanban read/write paths to use generic workflow state while keeping
   the public API stable.
4. Add issue workflow-state API exposure.
5. Add the workflow-state daemon API endpoints and regenerate API artifacts.
6. Add `docs/middleman-mcp.md`.
7. Add `middleman mcp` with stdio transport, curated read tools, the workflow
   write tool, and the guidance resource.
8. Add HTTP MCP transport with token, Host, Origin, and loopback checks.

The implementation plan can split these into smaller commits, but the generic
workflow state should land before MCP write tools so the MCP surface does not
depend on a PR-only concept.

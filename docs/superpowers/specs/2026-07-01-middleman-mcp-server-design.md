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
- Expose cached PR/issue details when the model has selected an item.
- Expose middleman-local workflow state for both PRs and issues.
- Preserve the existing PR kanban API and UI behavior.
- Let MCP clients set local item workflow state, including `reviewing`.
- Include workspace and stack context because those affect review decisions.
- Include a guidance document for configuring and using the MCP capabilities.

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
middleman mcp --transport http --addr 127.0.0.1:0
```

These flags are the user-facing contract:

- `stdio` is the default.
- HTTP binds only to loopback in v1.
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

### HTTP Safety

The HTTP MCP transport is local-only in v1. The companion should reject
non-loopback bind addresses rather than inheriting the daemon's broader bind
options. Remote MCP access can be designed later with explicit auth and origin
policy.

The companion does not expose the daemon's auth token to MCP tool responses or
logs. Errors that mention daemon discovery paths should avoid leaking secrets.

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
  updated_actor    -- optional user/client/agent label
  updated_reason   -- optional short free-text reason
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

`updated_source` identifies the local surface that changed the state. MCP writes
use `mcp`. `updated_actor` should be the MCP client name or supplied agent label
when available. `updated_reason` is optional and should be short enough to show
in future UI without becoming an unbounded log.

The API should also accept an optional expected current status. If provided and
the stored status has changed, the daemon returns a conflict. This lets a
periodic model avoid overwriting a human or another agent that already moved the
item.

### PR Kanban Compatibility

Existing PR list/detail responses continue to expose `KanbanStatus` with the
same behavior as today:

- missing workflow rows read as `new`;
- unexpected values normalize to `new`;
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
- `GET /pulls`
- `GET /pulls/{provider}/{owner}/{name}/{number}`
- `GET /issues`
- `GET /issues/{provider}/{owner}/{name}/{number}`
- `GET /stacks`
- `GET /workspaces`

Add focused daemon endpoints only for the generic local workflow state:

- `GET /workflow-state`
- `PUT /workflow-state/{item_type}/{provider}/{owner}/{name}/{number}`
- host-prefixed variants for non-default provider hosts

`GET /workflow-state` supports repo, item type, state, limit, and offset
filters. It returns compact provider-aware item refs and last-writer metadata
joined to PR/issue title, state, URL, author, and last activity. It treats
missing rows as `new`, so `state=new` includes open items that have never been
moved. It is not a replacement for PR/issue list endpoints.

`PUT /workflow-state/...` validates the item type, state, provider-aware route,
and item existence. It writes only middleman-local state. It never calls a
provider mutator.

The existing PR kanban route remains because it is part of the current public
API and UI contract.

## MCP Surface

The MCP server exposes curated tools, resources, and prompts. It does not expose
generic HTTP passthrough.

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
- `limit`
- `offset`

This lets a model answer questions such as "what am I already reviewing?" or
"what did another agent mark waiting?" without scanning all cached PRs/issues.

### Resource: `middleman://mcp/guidance`

Expose the guidance document content as an MCP resource so a client can load the
recommended usage patterns.

### Prompt: `middleman-review-candidates`

Provide a reusable prompt template for periodic review triage. The prompt should
tell the model to:

- use `middleman_find_review_candidates`;
- inspect details only for plausible items;
- prefer cached evidence over assumptions;
- avoid provider writes;
- set workflow state only when the reason is clear;
- include `expected_status` when marking an item;
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
- server API tests cover workflow-state GET/PUT, host-prefixed identity,
  invalid status, missing item, and expected-status conflict;
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
- daemon discovery/auth tests reuse the runtime metadata pattern used by the
  API CLI where possible.

CLI tests:

- `middleman mcp` defaults to stdio;
- HTTP transport rejects non-loopback bind addresses;
- unavailable daemon produces a clear error without exposing secrets.

Generation:

- run `make api-generate` after adding daemon API endpoints.

No Playwright coverage is required unless implementation changes visible UI.

## Rollout

1. Add generic workflow-state storage and migrate existing PR kanban rows.
2. Update PR kanban read/write paths to use generic workflow state while keeping
   the public API stable.
3. Add issue workflow-state API support.
4. Add `middleman mcp` companion with curated tools.
5. Add `docs/middleman-mcp.md` and expose it as an MCP resource.

The implementation plan can split these into smaller commits, but the generic
workflow state should land before MCP write tools so the MCP surface does not
depend on a PR-only concept.

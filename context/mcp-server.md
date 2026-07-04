# MCP Server Context

Middleman's MCP companion is a separate process started by `middleman mcp`.
It exposes cached daemon data through a curated MCP surface. It must not open
SQLite directly and must not call provider APIs; the daemon remains authoritative
for cached reads, local workflow writes, auth, host validation, and route
semantics.

The companion discovers the running daemon the same way `middleman api` does:
load the selected config, read runtimelock metadata from the config data dir,
build the loopback daemon base URL, and send the daemon auth token when present.
Startup is lazy: tools may report daemon-unavailable when called, but companion
startup itself should not require a running daemon.

The curated v1 tool set is intentionally fixed:

- `middleman_find_review_candidates`
- `middleman_get_item_context`
- `middleman_set_item_workflow_state`
- `middleman_list_activity`
- `middleman_list_items_by_workflow_state`
- `middleman_list_repos`
- `middleman_search_items`
- `middleman_get_item_diff`
- `middleman_get_stack_context`

`middleman_set_item_workflow_state` is the only MCP write tool. It calls the
daemon `PUT /workflow-state/{item_type}/{provider}/{owner}/{name}/{number}`
route, or the host-prefixed variant, and always sends `source: "mcp"`. It
passes `expected_status` through so agents can avoid overwriting humans or other
agents. A write without `expected_status` must include `force: true`; that
combination is an unconditional local workflow override and should be reserved
for deliberate overrides. Workflow writes never retry automatically.

Every item/repo ref sent through MCP must preserve the full provider identity:
`provider`, `platform_host`, `owner`, `name`, and item `number` where relevant.
If `platform_host` is omitted in a tool input, the companion uses the provider's
default-host route, but the logical identity still includes that normalized
default host. Host-prefixed daemon paths are required when `platform_host` is
present. Nested owners, such as GitLab groups, must be path-escaped as one
segment.

`middleman_search_items` uses the daemon's cached PR/issue list search fields.
It should not be documented or treated as full body/comment search unless those
daemon list endpoints explicitly support those fields.

`middleman_get_item_diff` uses `/files` only for summary-only calls. When
`emit_diff_file` is true, `internal/mcpserver/tools_diff.go::getItemDiff`
fetches `/diff` once and builds both the returned summary and temp file from
that single daemon response, so MCP clients never receive summary metadata from
one diff snapshot and patch text from another. Temp-file names are deterministic
per item but include a hash of the full provider/host/owner/name/number tuple
(`internal/mcpserver/tools_diff.go::diffFileName`) so sanitized nested owner
paths cannot collide. The daemon's gitclone diff parser must not silently omit
changed files when raw metadata and parsed patch sections disagree; unmatched
patch sections are preserved as structured diff rows so incomplete data is
visible instead of hidden.

The HTTP transport serves streamable MCP at the listener root, requires a
non-blank bearer token from `--http-token-env`, rejects non-loopback binds, and
checks bearer auth, loopback Host, and loopback Origin before handing requests
to the MCP SDK handler. Origin checks use strict `http` scheme and listener
port matching, while treating `localhost`, `127.0.0.1`, and `[::1]` as
equivalent loopback aliases for that port.

MCP guidance is exposed both as the resource `middleman://mcp/guidance` and as
the `middleman-review-candidates` prompt. User-facing setup and usage guidance
lives in `docs/middleman-mcp.md`.

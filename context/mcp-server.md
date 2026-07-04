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
agents. Workflow writes never retry automatically.

Every item/repo ref sent through MCP must preserve the full provider identity:
`provider`, `platform_host`, `owner`, `name`, and item `number` where relevant.
Host-prefixed daemon paths are required when `platform_host` is present. Nested
owners, such as GitLab groups, must be path-escaped as one segment.

MCP guidance is exposed both as the resource `middleman://mcp/guidance` and as
the `middleman-review-candidates` prompt. User-facing setup and usage guidance
lives in `docs/middleman-mcp.md`.

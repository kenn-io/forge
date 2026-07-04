# Middleman MCP

`middleman mcp` starts a companion MCP server for local agents. It exposes
cached middleman data and one local workflow-state write. It does not force
provider refreshes. In v1, it writes only middleman-local workflow state.

## Stdio Client Configuration

Configure stdio clients to launch the companion command:

```json
{
  "mcpServers": {
    "middleman": {
      "command": "middleman",
      "args": ["mcp"]
    }
  }
}
```

The companion discovers the running daemon from the configured middleman data
directory. Start `middleman` before using tools that read or write daemon data.

## HTTP Transport

Use the loopback HTTP transport only when a client cannot use stdio:

```bash
export MIDDLEMAN_MCP_TOKEN="$(openssl rand -hex 32)"
middleman mcp --transport http --addr 127.0.0.1:8092 --http-token-env MIDDLEMAN_MCP_TOKEN
```

Generate at least 32 random bytes for the token; `openssl rand -hex 32` is a
reasonable default. The companion checks that the token environment variable is
non-blank, but it does not enforce entropy.

Example bearer request:

```bash
curl -H "Authorization: Bearer $MIDDLEMAN_MCP_TOKEN" http://127.0.0.1:8092/mcp
```

## Usage Patterns

Call `middleman_list_repos` first to discover repo filters and sync freshness.
Use those provider-aware filters with other tools instead of guessing owner and
repo strings.

For periodic review triage:

1. Call `middleman_find_review_candidates` with `since` set to the scheduler's
   last successful run.
2. Inspect likely items with `middleman_get_item_context`.
3. For PRs, call `middleman_get_item_diff` in summary mode first. Request the
   full diff file only when the summary is not enough.
4. For stacked PRs, call `middleman_get_stack_context` before claiming work.
5. Mark an item `reviewing` only when there is a clear review reason.
6. Include `expected_status` when calling
   `middleman_set_item_workflow_state`, so stale runs do not overwrite humans
   or other agents.

Safe prompt shape:

```text
Find recent review candidates from cached middleman data. Call
middleman_list_repos first, then middleman_find_review_candidates. Inspect only
plausible items. Do not perform provider writes. If claiming work, set local
workflow state to reviewing with expected_status and a short reason. Report
stale cache fields and uncertainty.
```

Use `middleman_search_items` for quiet items that may not have recent
activity-based candidates.

Diff files produced by `middleman_get_item_diff` are ephemeral and local to the
companion host. They are intended for the current agent session and may
disappear when the companion exits or the operating system cleans its temp
directory.

To inspect already claimed or paused work, call
`middleman_list_items_by_workflow_state` with states such as `reviewing/waiting`.
Treat stale cache fields, missing detail, and old sync timestamps as lower
confidence signals, not as proof that no provider-side activity exists.

## Troubleshooting

`no middleman daemon is running on <data_dir>` means the companion could not find
a running daemon for that config. Start `middleman` with the same config path.

Auth errors usually mean the daemon runtime `auth_token` file is missing,
unreadable, or does not match the running daemon. Check the data directory and
file permissions.

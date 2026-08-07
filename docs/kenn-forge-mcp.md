# Kenn Forge MCP

`kenn-forge mcp` starts a companion MCP server for local agents. It exposes
cached Kenn Forge data and one local workflow-state write. It does not force
provider refreshes. In v1, it writes only Kenn Forge-local workflow state.

## Stdio Client Configuration

Configure stdio clients to launch the companion command:

```json
{
  "mcpServers": {
    "kenn-forge": {
      "command": "kenn-forge",
      "args": ["mcp"]
    }
  }
}
```

The companion discovers the running daemon from the configured Kenn Forge data
directory. Start `kenn-forge` before using tools that read or write daemon data.

## HTTP Transport

Use the loopback HTTP transport only when a client cannot use stdio:

```bash
export KENN_FORGE_MCP_TOKEN="$(openssl rand -hex 32)"
kenn-forge mcp --transport http --addr 127.0.0.1:8092 --http-token-env KENN_FORGE_MCP_TOKEN
```

Generate at least 32 random bytes for the token; `openssl rand -hex 32` is a
reasonable default. The companion checks that the token environment variable is
non-blank, but it does not enforce entropy.

The streamable HTTP MCP endpoint is the listener root. MCP clients send JSON-RPC
requests there with `Authorization: Bearer <token>`. Browser-style Origin
headers are accepted only when they use `http` and the same loopback listener
port; `localhost`, `127.0.0.1`, and `[::1]` aliases are treated as equivalent
loopback origins.

```bash
curl -H "Authorization: Bearer $KENN_FORGE_MCP_TOKEN" http://127.0.0.1:8092/
```

## Usage Patterns

Call `kenn_forge_list_repos` first to discover repo filters and sync freshness.
Use those provider-aware filters with other tools instead of guessing owner and
repo strings.

For periodic review triage:

1. Call `kenn_forge_find_review_candidates` with `since` set to the scheduler's
   last successful run.
2. Inspect likely items with `kenn_forge_get_item_context`.
3. For PRs, call `kenn_forge_get_item_diff` in summary mode first. Request the
   full diff file only when the summary is not enough.
4. For stacked PRs, call `kenn_forge_get_stack_context` before claiming work.
5. Mark an item `reviewing` only when there is a clear review reason.
6. Include `expected_status` when calling
   `kenn_forge_set_item_workflow_state`, so stale runs do not overwrite humans
   or other agents. Use `force: true` instead of `expected_status` only for a
   deliberate unconditional local override.

Safe prompt shape:

```text
Find recent review candidates from cached Kenn Forge data. Call
kenn_forge_list_repos first, then kenn_forge_find_review_candidates. Inspect only
plausible items. Do not perform provider writes. If claiming work, set local
workflow state to reviewing with expected_status and a short reason. Report
stale cache fields and uncertainty.
```

Use `kenn_forge_search_items` for quiet items that may not have recent
activity-based candidates. Search is backed by Kenn Forge's cached PR/issue list
query fields; it is not full body or comment search unless the daemon list
endpoint supports that field.

Diff files produced by `kenn_forge_get_item_diff` are ephemeral and local to the
companion host. They are intended for the current agent session and may
disappear when the companion exits or the operating system cleans its temp
directory.

To inspect already claimed or paused work, call
`kenn_forge_list_items_by_workflow_state` with states such as `reviewing/waiting`.
Treat stale cache fields, missing detail, and old sync timestamps as lower
confidence signals, not as proof that no provider-side activity exists.

## Troubleshooting

`no Kenn Forge daemon is running on <data_dir>` means the companion could not find
a running daemon for that config. Start `kenn-forge` with the same config path.

Auth errors usually mean the daemon runtime `auth_token` file is missing,
unreadable, or does not match the running daemon. Check the data directory and
file permissions.

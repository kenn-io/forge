# kenn-forge MCP

`kenn-forge mcp` starts a companion MCP server for local agents. It exposes
cached kenn-forge data, local workflow state, and an explicit local coding-agent
handoff. It does not force provider refreshes or perform provider writes. When
not explicitly handing off work, it writes only kenn-forge-local workflow state.

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

The companion discovers the running daemon from the configured kenn-forge data
directory. Start it with `kenn-forge daemon start` before using tools that read
or write daemon data.

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
Find recent review candidates from cached kenn-forge data. Call
kenn_forge_list_repos first, then kenn_forge_find_review_candidates. Inspect only
plausible items. Do not perform provider writes. If claiming work, set local
workflow state to reviewing with expected_status and a short reason. Report
stale cache fields and uncertainty.
```

Use `kenn_forge_search_items` for quiet items that may not have recent
activity-based candidates. Search is backed by kenn-forge's cached PR/issue list
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

Workflow lists default to 50 items and cap requests at 200. A stored workflow
row orders by its local update time; an item without one orders by provider
activity. Provider activity is the second key, followed by provider, repository,
item type, and number. A same-state write intentionally refreshes local metadata
and this ordering. Tool errors are JSON envelopes with stable
`kind`, daemon `code`, structured `details`, `retryable`, and `ambiguous`
fields; never retry an ambiguous mutation.

## Coding-agent handoff

Call `kenn_forge_list_agent_targets` to discover configured coding-agent keys
and whether each target is currently available. To hand off work, call
`kenn_forge_spawn_workspace_with_agent` with exactly one source:

- a provider-aware PR or issue reference; or
- an ad-hoc provider-aware repository reference with an optional branch.

The tool creates or reuses the workspace, waits for it to become ready, launches
a new agent runtime, waits for the agent hook to report its coding session ID,
and submits exactly one initial message. Item-backed workspaces suppress optional
automatic provider assignment. Existing workspaces still receive a new runtime.

The response reports the last completed stage plus every workspace, runtime,
and coding-session identifier learned so far. Created resources remain running
after a later failure. Do not retry an ambiguous create, launch, or message
mutation; the companion performs only a same-daemon status lookup after a lost
initial-message response.

Use `kenn_forge_list_workspace_agent_sessions` to list fresh, live coding
session IDs reported by hooks for a workspace. It intentionally omits expired
or historical sessions. Sending follow-up messages to an existing coding
session is outside the current MCP surface.

## Troubleshooting

`no kenn-forge daemon is running on <data_dir>` means the companion could not find
a running daemon for that config. Start `kenn-forge daemon start --config
<config_path>` with the same config path.

Auth errors usually mean the daemon runtime `auth_token` file is missing,
unreadable, or does not match the running daemon. Check the data directory and
file permissions.

Run the companion and daemon from the same kenn-forge installation. Endpoint
capability errors mean the daemon must be upgraded before that tool can run.

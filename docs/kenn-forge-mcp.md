# kenn-forge MCP

kenn-forge can expose cached maintainer workflows to local MCP clients from the
running daemon. The companion reads the same in-process repository, activity,
workflow, and workspace services as the UI. It does not force provider refreshes
or add provider mutations.

## Enable the companion

Add this section to `~/.kenn/forge/config.toml`:

```toml
[mcp]
enabled = true
# port = 8092 # defaults to the main backend port plus one
```

Restart the daemon after changing either setting:

```sh
kenn-forge daemon restart
```

With the default backend port, the sessionless Streamable HTTP endpoint is:

```text
http://127.0.0.1:8092/mcp
```

The listener is loopback-only. It follows `[api].require_auth`: when API
authentication is enabled, send the daemon bearer token; when it is disabled,
the MCP endpoint does not require a token. Runtime discovery publishes the
resolved MCP listener and URL.

Configure your MCP client with the HTTP URL. For clients that accept a JSON
server catalog, the shape is typically similar to:

```json
{
  "mcpServers": {
    "kenn-forge": {
      "type": "http",
      "url": "http://127.0.0.1:8092/mcp"
    }
  }
}
```

## Review workflow

Call `kenn_forge_list_repos` first to discover provider-aware repository filters
and sync freshness. For review triage:

1. Call `kenn_forge_find_review_candidates` with the desired time window and
   item types.
2. Inspect likely items with `kenn_forge_get_item_context`.
3. For pull requests, use `kenn_forge_get_item_diff` in summary mode first.
4. Use `kenn_forge_get_stack_context` before claiming work on a stacked pull
   request.
5. Set local workflow state with `expected_status` so a stale agent does not
   overwrite another actor. Use `force: true` only for a deliberate override.

`kenn_forge_search_items` finds quiet cached pull requests and issues that are
absent from recent activity. Candidate output defaults to 25 items and never
exceeds 100.

Diff files produced by `kenn_forge_get_item_diff` are temporary files on the
daemon host. Forge keeps the most recently requested files within the
`[mcp].diff_cache_mb` limit, which defaults to 128 MiB. Older files may be
deleted before daemon shutdown.

## Coding-agent handoff

Call `kenn_forge_list_agent_targets` to discover available coding agents. Then
call `kenn_forge_spawn_workspace_with_agent` with one source:

- a provider-aware pull request or issue; or
- a provider-aware repository with an optional ad-hoc branch.

The tool creates or reuses a workspace, launches a new agent runtime, waits for
its live hook session, and submits one initial message. It does not clean up a
workspace or runtime after a later failure. The response uses `stage` and
`initial_message.state` as the authoritative handoff evidence.

Use `kenn_forge_list_workspace_agent_sessions` to inspect fresh coding sessions
joined to live agent runtimes. Historical sessions and arbitrary terminal bytes
are outside the MCP surface.

## Troubleshooting

If the endpoint is unavailable, confirm `[mcp].enabled = true`, restart the
daemon, and inspect daemon status for the resolved listener. An explicit MCP
port must differ from the backend port and must be free at startup.

A `401` response means `[api].require_auth` is enabled and the bearer token is
missing or incorrect. A `403` response means the request did not arrive as a
direct same-origin loopback request.

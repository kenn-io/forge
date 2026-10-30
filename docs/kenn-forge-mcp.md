# Kenn Forge MCP

Forge can expose cached maintainer workflows to local MCP clients from the
running daemon. The companion reads the same in-process repository, activity,
workflow, and workspace services as the UI. It does not force provider refreshes
or add provider mutations.

## On by default

With `mcp.enabled` and `mcp.port` omitted, the companion runs on Forge's own
port when `host` is a loopback IP, such as `127.0.0.1` or `::1`, and Forge
isn't an execution worker. Other hosts, such as `0.0.0.0` or a tailnet IP,
require `enabled = true`. With the default backend port, the endpoint is:

```text
http://127.0.0.1:8091/mcp
```

To turn it off, save this in `~/.kenn/forge/config.toml`:

```toml
[mcp]
enabled = false
```

With `enabled = true` and no `port`, the companion also uses Forge's own port.
For a separate loopback listener, set `port`. A saved `port` with `enabled`
omitted keeps the companion off.

Older versions dropped `enabled = false` when saving. Configurations with no
`enabled` key and no MCP port start serving `/mcp` after upgrade when the default
applies, so set `enabled = false` to keep it off.

Restart the daemon after changing MCP enablement or its listener port:

```sh
kenn-forge daemon restart
```

Loopback MCP follows `[api].require_auth`, same as the HTTP API.
When authentication is enabled, send the daemon bearer token.
Runtime discovery publishes the resolved MCP listener and URL, and
`kenn-forge daemon status --json` shows both the listener address and the token
file path.

While MCP is enabled, `/mcp` on the main listener also accepts daemon bearer
holders and, with `[api.tailscale_serve]`, allowlisted Serve users. `/api/ping`
reports `mcp_url` on the origin the caller used, so a client that reaches Forge
through Tailscale Serve discovers the Serve URL.

For a machine-readable listener list, run:

```sh
kenn-forge mcp status --json
```

The command returns an array with `pid`, `transport: "http"`, `url`,
`backend_url`, and an optional `token_path`. It reports the active listener,
including when saved settings require a restart. A stopped daemon or inactive
companion returns `[]`; the command never starts either. Use `--config` to
select another configuration and `--timeout` to set the daemon request timeout.
Token contents never appear in the output. For client setup instructions and
restart guidance, use `kenn-forge mcp quickstart`.

Configure your MCP client with the HTTP URL. For clients that accept a JSON
server catalog, the shape is typically similar to:

```json
{
  "mcpServers": {
    "kenn-forge": {
      "type": "http",
      "url": "http://127.0.0.1:8091/mcp"
    }
  }
}
```

With `[api].require_auth` enabled, add the bearer token as an `Authorization`
header. The daemon mints the token into the `auth_token` file in its data
directory (mode 0600); read it from there rather than pasting it into shared
configuration:

```json
{
  "mcpServers": {
    "kenn-forge": {
      "type": "http",
      "url": "http://127.0.0.1:8091/mcp",
      "headers": {
        "Authorization": "Bearer ${KENN_FORGE_API_TOKEN}"
      }
    }
  }
}
```

Set the referenced environment variable from the token file, for example
`export KENN_FORGE_API_TOKEN="$(cat ~/.kenn/forge/auth_token)"`, using the
`token_path` reported by `kenn-forge daemon status --json`.

If Forge is published with Tailscale Serve and your Tailscale login is allowed
(see [Federated Forge](federated-fleet.md)), point the client at the Serve origin
instead. No token is needed:

```json
{
  "mcpServers": {
    "kenn-forge": {
      "type": "http",
      "url": "https://build-a.example.ts.net/mcp"
    }
  }
}
```

## Connect from another machine or a container

The companion listener stays on loopback. A client elsewhere on your network,
such as an MCP gateway in a container, connects to `/mcp` on the main Forge
port with the daemon bearer token instead. Bind the main listener to an
address that client can reach, list the name it uses in `allowed_hosts`, keep
`[mcp].enabled = true`, and turn on API authentication:

```toml
host = "192.0.2.10"
allowed_hosts = ["forge.lan:8091"]

[api]
require_auth = true

[mcp]
enabled = true
```

Background start (`kenn-forge daemon start` or `restart`) only binds loopback,
so run this setup in the foreground with `kenn-forge serve`, for example under
a service manager.

Then point the client at `http://forge.lan:8091/mcp` with the
`Authorization: Bearer ${KENN_FORGE_API_TOKEN}` header shown above. The main
port requires the token for MCP whether or not `[api].require_auth` is set,
but that setting is what protects the rest of the API on the same port.
Without it, anyone who can reach the address can use the web UI and API
without a token. Plain HTTP sends the token in the clear, so use this only on
a network you trust, such as a tailnet or a private container network, or put
an HTTPS proxy in front.

Requests whose Host isn't in `allowed_hosts` or the bind address get `403`, as
do requests with an `Origin` header other than the matching `https://` origin;
server-side clients normally send no `Origin`.

Earlier previews exposed a standalone `kenn-forge mcp` command with a stdio
transport. That command and transport are removed without a compatibility
shim: MCP is served only by the running daemon over the HTTP endpoint above,
so update any client configuration that launched the old command.

## Review workflow

Call `kenn_forge_list_repos` first to discover provider-aware repository filters,
stable repository keys, and sync freshness. Each repository has a
`platform_repo_id`, or a `bitbucket_repository_uuid` for Bitbucket Cloud. Copy
the returned key into later repository and item references so they keep
resolving after a rename. A reference with only provider, owner, and name
resolves to the repository that currently holds that route. `platform_host`
defaults to the provider's public host; pass it for self-hosted instances and
Bitbucket Data Center. For review triage:

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
exceeds 100. With `semantic: true` it ranks them by how closely their titles,
descriptions, and comments match the query instead of matching its text; that
needs [semantic search](configuration.md#semantic-search) configured.

Diff files produced by `kenn_forge_get_item_diff` are temporary files on the
daemon host. Forge keeps the most recently requested files within the
`[mcp].diff_cache_mb` limit, which defaults to 128 MiB. Older files may be
deleted before daemon shutdown.

## Coding-agent handoff

Call `kenn_forge_list_agent_targets` to discover configured coding agents,
including custom targets. Use the returned target key as `agent_target`; it
does not need to match the agent name reported by hooks. The launched agent
must report a supported hook session for the handoff to complete.

Call `kenn_forge_spawn_workspace_with_agent` with one source:

- a provider-aware pull request or issue; or
- a provider-aware repository with an optional ad-hoc branch.

The tool creates or reuses a workspace, launches a new agent runtime, submits
one initial message, and then waits for the resulting live hook session. It does
not clean up a workspace or runtime after a later failure. The response uses
`stage` and `initial_message.state` as the authoritative handoff evidence.

If prompt submission or hook observation times out after the response includes
a workspace ID and runtime session key, call the same tool with `resume` instead
of `source`. Pass those two IDs with the same `agent_target` and normalized
`initial_message`. Resume targets the existing runtime and does not launch a
second agent.

Use `kenn_forge_list_workspace_agent_sessions` to inspect live agent runtimes
and fresh coding sessions. A runtime with `hook_observed=false` has launched but
has not reported its first hook.

To send another instruction after the initial handoff, call
`kenn_forge_send_agent_message` with the persisted workspace ID, the live
runtime session key, and the message. The tool submits the prompt to that
running agent through the same runtime input path as the initial message. It
does not launch a runtime, resume a handoff, or wait for later agent activity.
Historical sessions and arbitrary terminal bytes are outside the MCP surface.

## Build worklists

An agent can hand the maintainer a ranked
[worklist](workflows/worklists.md) of synced pull requests and issues. None of
these tools call provider APIs.

- `kenn_forge_put_worklist` writes a list with a title, a recipe, and ranked
  items. Each item can carry a score, section, note, and caveat. Every item
  must be a pull request or issue Forge has synced; copy its identity from a
  Forge read. Pass `worklist_id` to replace an existing list, or omit it to
  create a new one.
- `kenn_forge_get_worklist` reads a list with its recipe and, for each item,
  whether it is fresh, changed, or gone since the list was written, what
  changed, and when the maintainer marked it handled.
- `kenn_forge_list_worklists` lists worklists, most recently updated first,
  with their item counts. Pass `include_dismissed` to include dismissed lists.
- `kenn_forge_dismiss_worklist` hides a list. Writing the list again brings it
  back.
- `kenn_forge_clear_worklist_marks` clears every handled mark on a list. Call
  it only when the maintainer asks.
- `kenn_forge_import_worklist` creates the list a worklist link carries, as
  opening the link in Forge does. It accepts a `kenn://` link, an HTTP(S) link
  to `/worklists/import#v1...`, or the bare `v1...` fragment.

To refresh a list, read its recipe, rerun the searches, and put the whole list
again. Handled marks survive a rewrite for items that stay on the list.

## Troubleshooting

If the endpoint is unavailable, check `kenn-forge mcp quickstart`, restart the
daemon, and inspect daemon status for the resolved listener. A separate MCP
port must be free at startup.

A `401` response means `[api].require_auth` is enabled and the bearer token is
missing or incorrect. A `403` response means the request did not arrive as a
direct same-origin loopback request. On the main port, `401` means the request
had neither the daemon bearer nor an allowed Tailscale Serve login, and `403`
means its Host is not allowed or its Origin names another site.

# MCP Companion

## Apps and media

- Interactive components are agent-authored; Forge supplies generic rendering and cached data
  primitives, not built-in dashboard layouts (`internal/mcpserver/apps.go`).
- Keep the ACP app kind marker in both text and structured output: real agent adapters flatten
  resources and may discard text for structured data (`internal/mcpserver/apps.go::AppDescriptorKind`).
- The first-party ACP app descriptor selects a renderer, not an authority; it does not establish
  third-party MCP server identity (`frontend/src/lib/components/acp/mcp-app.ts`).
- Generated anchors cross the same host-confirmed link path as `openLink`; confirmation links open
  a new tab and keep native browser navigation (`frontend/src/lib/components/acp/McpApp.svelte`).
- Generated apps reread cached evidence and cannot mutate; freshness and cached mergeability never
  certify permission to merge (`internal/mcpserver/apps.go::CallAppTool`).
- Chat moves replace a widget iframe's window; the host must bridge each new window, not connect
  once (`frontend/src/lib/components/acp/mcp-app.ts::hostApp`).
- Agent adapters forward tool results only as text or base64 images, so local media travels as a
  path descriptor resolved on the agent host; never put media bytes in tool output (`internal/mcpserver/media.go::MediaDescriptorKind`).

## Reads

- Event excerpts must disclose whether more cached events exist; never silently truncate
  (`internal/mcpserver/tools_items.go::getItemContextOutput`).
- Agent guidance prefers cached Forge reads over provider CLI/API reads; explain missing or stale
  evidence before falling back (`internal/mcpserver/guidance.go::serverInstructions`).
- Repository-wide PR scans use bulk cached list data, never one detail read per PR; review-event
  enrichment is opt-in (`internal/mcpserver/tools_pulls.go::Server.listPullContexts`).
- Cached readiness evidence in tool output never certifies permission to merge.
- PR contexts keep cached label names; exact label filters apply in the provider-owned list query
  before pagination, including across the hub (`internal/server/mcp_backend.go::mcpBackend.ListPulls`).
- Candidate output defaults to 25 and caps at 100; apply candidate `item_types` to Activity before
  its 5,000-row safety window (`internal/mcpserver/tools_candidates.go::Server.findReviewCandidates`).
- Treat only typed not-stacked responses as absence; surface other evidence failures with retry
  and ambiguity state (`internal/mcpserver/tools_stack.go::isStackAbsentError`).

## Transport and listeners

`cmd/kenn-forge/main.go` builds one `internal/mcpserver` server over the typed backend from
`internal/server/mcp_backend.go` and serves it on three entry points; `NewFederatedBackend` splits
that backend into hub and local parts:

```text
companion listener (explicit port)  own http.Server in cmd/kenn-forge/main.go
main listener /mcp (shared port)    hidden Huma route, internal/server/mcp_tailnet.go
ACP agent listener (execution host) cmd/kenn-forge/agent_mcp.go, independent of companion settings
discovery                           runtime record `mcp_listen_addr`; `/api/ping` `mcp_url`
```

- ACP launches and connection tests use a dedicated authenticated loopback listener on the
  execution host, independent of browser and companion settings; native harness servers and skills
  stay agent-owned (`cmd/kenn-forge/agent_mcp.go::newAgentMCPHTTP`).
- Forge MCP tools are optional for ACP agents: one without HTTP MCP support starts without them and
  gets a session notice, never a failed launch or test (`internal/workspace/localruntime/acp.go::startACPSession`).
- Durable ACP owners keep their injected MCP address across daemon restarts; reattachment updates
  the URL and credential behind it (`internal/workspace/localruntime/acp_mcp_proxy.go::acpMCPProxy.Bind`).
- With the port omitted, MCP uses the backend port; an explicit port is the only way to get a
  separate listener. Omitted enablement defaults on only for loopback-IP non-workers (`internal/config/config.go::Config.MCPPort`).
- When sharing the backend port, the main listener serves `/mcp` instead of binding a colliding
  socket. Direct loopback requests get the companion guard; others keep bearer and Serve policy
  (`internal/server/mcp_tailnet.go::Server.isDirectLocalMCPRequest`).
- The companion listener is startup-bound and loopback-only (`127.0.0.1` when `host` is not
  loopback); changing listener settings requires restart (`internal/config/config.go::Config.MCPListenAddr`).
- When the main listener serves `/mcp`, ping advertises `/mcp` on the caller's own origin, because
  host apps require the endpoint to match the origin they used (`internal/server/daemon_ping.go::Server.advertisedMCPURL`).
- Settings shows saved MCP config separately from the boot-active endpoint. Saving never
  hot-restarts the companion; report restart drift and give token-file guidance, never the bearer
  (`internal/server/settings_handlers.go::Server.buildLocalSettingsResponse`).
- `kenn-forge mcp quickstart` is the canonical agent discovery path; it exposes token paths and
  environment placeholders, never bearer contents (`cmd/kenn-forge/mcp_cli.go::newMCPCommand`).
- The companion listener serves only `/mcp` over stateless Streamable HTTP. Auth follows
  `[api].require_auth`; it requires a direct loopback peer and authority, no forwarding headers, and
  any Origin to be same-origin (`internal/server/mcpapi/mcp_http.go::NewMCPHTTPGuard`).
- On the main listener, keep `/mcp` policy in its route, not in `ServeHTTP` interception
  (`internal/server/mcp_tailnet.go::Server.registerMCPRoute`).
- While MCP is enabled, the main listener serves `/mcp` to the daemon bearer regardless of
  `require_auth`, and to allowlisted Serve users, after its host check. The identity header is
  ambient, so a cross-origin Origin is rejected (`internal/server/mcp_tailnet.go::Server.serveMCP`).
- Serve reaches the main listener with the public Host, so its `/mcp` path skips the SDK's localhost
  rebinding guard and relies on Forge's host check (`internal/mcpserver/server.go::Server.TailnetHTTPHandler`).
- Target MCP `2026-07-28`; do not advertise deprecated logging or catalog notifications on
  stateless HTTP. Kata tool availability refreshes per request (`internal/mcpserver/server.go::Server.httpHandler`).
- Use only canonical `kenn-forge` command/resource/prompt names and `kenn_forge_*` tools; do not
  add aliases (`internal/mcpserver/server.go::Server.registerTools`).
- Shutdown: stop MCP admission, wait the bounded grace, cancel handler contexts and force-close
  connections, and only then close the temp store and database. Handlers on the main listener follow
  this, not the primary drain (`internal/server/mcpapi/mcp_gate.go::RequestGate`).

## Backend and identity

- Tools call the typed in-process Forge backend, never the daemon's public HTTP API
  (`internal/mcpserver/backend.go::Backend`).
- Provider reads and workflow state use the hub; clone, workspace, runtime, and agent-session tools
  stay local, and hub outages remain typed (`internal/mcpserver/backend.go::NewFederatedBackend`).
- Execution workers have no hub; repository, pull request, and issue reads return
  `serviceUnavailable` (`internal/server/mcp_backend.go::mcpBackend.ListRepositories`).
- Resolve repository references once at tool input: `platform_repo_id` names the repository, a
  route-only reference resolves to the repository now holding that route, and every response
  returns the ID (`internal/mcpserver/tools_items.go::Server.resolveRepositoryKey`).
- Missing `platform_host` defaults to the provider's public host, except a Bitbucket integer key,
  which names Data Center and requires a host. Incomplete identities fail as `invalid_request`
  naming the field, never `internal_error`.
- Single-item tools accept a listing's nested `repository` object; reads follow the ID to the
  current route across renames (`internal/server/mcp_backend.go::mcpBackend.resolveRepository`).
- Hub-backed workspace tools verify the descriptor still matches the supplied stable ID; route-only
  fallback must not redirect an existing selection (`internal/server/mcp_backend.go::mcpBackend.resolveProviderRepository`).
- Workflow state is hub-owned; workspace state stays local. Workflow reads and writes must not
  expose provider mutations, arbitrary commands, terminal bytes, lifecycle cleanup, or
  `removed_upstream` items (`internal/server/mcp_backend.go::mcpBackend.SetWorkflowState`).
- Worklist ownership and authorship rules live in `worklists.md`. Generated apps may read worklists
  but never put, import, dismiss, or clear them (`internal/mcpserver/tools_worklists.go::Server.registerWorklistTools`).

## Errors and diffs

- Tool failures set `isError` and preserve kind, code, retryability, ambiguity, and details as JSON
  plus `io.kenn.forge/error` metadata; never reduce typed failures to message-only errors (`internal/mcpserver/tools_read.go::wrapTool`).
- Failed calls carry no `structuredContent`, which clients read as a real empty result; partial
  evidence belongs in error details (`internal/mcpserver/server.go::omitErrorStructuredContent`).
- Each full-diff handoff stays within 10 MiB; the temp store is LRU-bounded by
  `[mcp].diff_cache_mb`, and returned paths may be replaced or evicted by any later write.
- Stage diff replacements fully before touching published state: a failed write never removes the
  same-name diff or evicts others, and an entry whose eviction failed stays counted (`internal/mcpserver/difftmp.go::diffFileStore.write`).
- A failed eviction must not stop eviction of newer entries; one undeletable file cannot keep the
  store over budget.
- Files without text hunks keep minimal Git-style headers, including `/dev/null` and binary
  markers; one empty patch must not drop the rest of the diff (`internal/mcpserver/tools_diff.go::serializeDiffPatches`).

## Agent handoff

- MCP can create or reuse one PR, issue, or ad-hoc workspace and launch one new agent runtime with
  one initial message; it confirms submission before waiting for the runtime's hook session.
- Readiness waits and input-not-ready retries use the shared handoff package, the same one the
  workspace agent-handoff endpoint uses (`internal/workspace/agenthandoff/agenthandoff.go::Deliver`).
- A resume names the existing workspace and runtime, repeats the same target and prompt through the
  duplicate guard, and never launches another agent.
- Ambiguous workspace or runtime mutations are never retried or cleaned up. Only the exact
  `workspaceAlreadyExists` pull conflict reuses the concurrent winner, after one authoritative read
  (`internal/mcpserver/tools_agent_spawn.go::Server.resolveOrCreatePRWorkspace`).
- Initial-message status recovery gets one bounded read that is independent of request cancellation
  (`internal/mcpserver/tools_agent_spawn.go::Server.recoverInitialMessageStatus`).
- MCP-created PR and issue workspaces suppress optional automatic assignment; ordinary UI creation
  keeps configured self-assignment (`internal/server/workspaceapi/routes_handlers.go::Handler.CreatePullWorkspace`).
- Initial-message attempts are process-local; same-daemon retries must match the live runtime target
  and prompt, and a restart permits a fresh attempt (`internal/server/workspaceapi/initial_message.go::initialMessageAttempt`).
- Agent messages must be non-blank valid UTF-8; only terminal agents also limit them to LF or
  printable Unicode within 64 KiB. Validate against the target's protocol before creating anything
  (`internal/server/workspaceapi/initial_message.go::normalizeAgentMessage`).
- Terminal initial input needs an exact live agent runtime and tracked bracketed paste for
  multiline text; hook observation is not a precondition (`internal/workspace/localruntime/manager.go::Manager.SubmitInitialMessage`).
- Retry only the typed safe-paste-not-observed condition, on the same runtime, until the handoff
  deadline. Writes must not hold the session lock while waiting; a timed-out write stays uncertain.
- Configured agent target keys are not hook agent identities; custom targets must stay launchable,
  and handoffs match the live runtime and target key (`internal/mcpserver/tools_agent_spawn.go::Server.waitForCodingSession`).
- Agent-session inspection returns live runtimes separately from reported sessions;
  `hook_observed=false` marks a launched runtime awaiting its first report (`internal/mcpserver/tools_agent.go::Server.listWorkspaceAgentSessions`).
- MCP agent tools cover terminal and ACP agents; ACP sessions come only from `agent=acp` owner
  reports on ACP runtimes, never hooks (`internal/server/workspaceapi/agent_sessions.go::reportedAgent`).
- Follow-up messages address one existing live runtime and return without launching, persisting,
  or waiting for activity; ACP follow-ups queue behind a running turn (`internal/mcpserver/tools_agent.go::Server.sendAgentMessage`).
- An omitted agent target picks the most-used available workspace agent of the prior 14 days; ties
  prefer recent use, then key, and empty history uses configured order (`internal/mcpserver/tools_agent_spawn.go::Server.defaultAgentTarget`).
- Handoff evidence uses `stage` plus `initial_message.state`; never add a separate
  `message_delivered` output or error detail (`internal/mcpserver/tools_agent_spawn.go::spawnWorkspaceWithAgentOutput`).

## Workspace targets

- ACP target tools bind the workspace through its owner proxy; terminal MCP clients supply the
  launch-context ID. Register every worked-on stack layer explicitly (`internal/mcpserver/tools_workspace_targets.go::workspaceScope`).
- Workspace target writes go through MCP's in-process backend; HTTP exposes only the list the UI
  needs. Add HTTP writes only when a concrete client needs them (`internal/server/workspaceapi/handler.go::Handler.RegisterExecution`).
- Workspace-target JSON responses must accept additional properties, so schema inference cannot
  turn a successful write into a tool failure (`internal/mcpserver/tools_workspace_targets.go::Server.registerWorkspaceTargetTools`).

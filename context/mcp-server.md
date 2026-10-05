# MCP Companion

- Interactive components are agent-authored; Forge supplies generic rendering and
  cached data primitives, not built-in dashboard layouts (`internal/mcpserver/apps.go`).
- Keep the ACP app kind marker in text and structured output: real agent adapters
  flatten resources and may discard text in favor of structured data.
  (`internal/mcpserver/apps.go::AppDescriptorKind`)
- The first-party ACP app descriptor selects a renderer, not an authority. It
  does not establish third-party MCP server identity (`frontend/src/lib/components/acp/mcp-app.ts`).
- Generated anchors must cross the same host-confirmed link path as `openLink`;
  confirmation links explicitly open a new tab and retain native browser navigation
  (`internal/mcpserver/app.html`, `frontend/src/lib/components/acp/McpApp.svelte`).
- Generated apps reread cached evidence and cannot perform mutations; freshness
  and cached mergeability never certify permission to merge (`internal/mcpserver/apps.go::CallAppTool`).

- Event excerpts must disclose whether more cached events exist so agents can
  decide whether to request more context; never silently truncate the list
  (`internal/mcpserver/tools_items.go::getItemContextOutput`).
- Agent guidance must prefer cached Forge reads over provider CLI/API reads to
  avoid redundant latency; explain missing or stale evidence before falling back
  (`internal/mcpserver/guidance.go::serverInstructions`).
- Repository-wide PR scans use bulk cached list data by default, never one detail
  read per PR; review-event enrichment is opt-in. Cached readiness evidence must
  not certify permission to merge (`internal/mcpserver/tools_pulls.go::Server.listPullContexts`).
- PR contexts preserve cached label names. Exact label filters run in the
  provider-owned list query before pagination, including across hub transport
  (`internal/server/mcp_backend.go::mcpBackend.ListPulls`).
- ACP launches and connection tests use a dedicated authenticated loopback HTTP listener
  on the execution host, independent of browser Host/proxy policy and companion settings;
  native harness servers and skills stay agent-owned (`cmd/kenn-forge/agent_mcp.go::newAgentMCPHTTP`).
- Forge MCP tools are optional for ACP agents: one without HTTP MCP support starts without
  them and gets a session notice, never an error or a failed test or launch (`internal/workspace/localruntime/acp.go::startACPSession`).
- Durable ACP owners retain their injected MCP address across daemon restarts;
  reattachment updates the daemon URL and credential behind that address
  (`internal/workspace/localruntime/acp_mcp_proxy.go::acpMCPProxy.Bind`).
- The companion uses an optional daemon-owned listener enabled by
  `[mcp].enabled`; an omitted or zero port uses the backend port plus one, while
  a nonzero port overrides it (`internal/config/config.go::Config.MCPPort`).
  When that port is the backend port and the main listener already accepts the
  companion's loopback address, the main listener serves `/mcp` under the
  companion policy for requests without a bearer or Serve identity, at the base
  path, instead of binding a colliding socket
  (`internal/config/config.go::Config.MCPSharesMainListener`,
  `internal/server/mcp_tailnet.go::Server.serveTailnetMCP`).
- The listener is startup-bound and loopback-only: it binds `127.0.0.1` when
  the main `host` is not loopback (`internal/config/config.go::Config.MCPListenAddr`).
  Discovery publishes `mcp_listen_addr` and `/api/ping` publishes `mcp_url`;
  changing listener settings requires a restart
  (`cmd/kenn-forge/main.go::bindDaemonListeners`,
  `internal/server/daemon_ping.go::Server.daemonPing`).
- Settings exposes the saved MCP listener/cache configuration separately from
  the boot-active endpoint and authentication policy. Saving MCP settings never
  hot-restarts the companion: report restart drift, keep showing an endpoint
  that remains active until restart, and provide only token-file guidance rather
  than returning the daemon bearer (`internal/server/settings_handlers.go::Server.buildLocalSettingsResponse`).
- `kenn-forge mcp quickstart` is the canonical agent discovery path for the
  active connector and saved restart drift; expose token paths and environment
  placeholders there, never bearer contents (`cmd/kenn-forge/mcp_cli.go::newMCPCommand`).
- The companion listener serves only `/mcp` over stateless Streamable HTTP. Authentication follows
  `[api].require_auth`; direct loopback peer, exact loopback authority, absent
  forwarding headers, and optional same-origin HTTP Origin are required
  (`internal/mcpserver/server.go::Server.HTTPHandler`,
  `internal/server/mcpapi/mcp_http.go::NewMCPHTTPGuard`).
- While MCP is enabled, the main listener also serves `/mcp` to requests with
  the daemon bearer, regardless of `[api].require_auth`, and, with
  `[api.tailscale_serve]` enabled, to allowlisted Serve users without a bearer
  (`internal/server/mcp_tailnet.go::Server.serveTailnetMCP`). Both pass the
  main listener's host check first. The identity header is ambient, so a
  request whose Origin names another authority is rejected. Serve reaches the
  loopback listener with the public Host, so this path uses the SDK handler
  without its localhost rebinding guard after Forge's host check
  (`internal/mcpserver/server.go::Server.TailnetHTTPHandler`).
- Tool implementations call the typed in-process Forge backend and never the
  daemon's public HTTP API (`internal/mcpserver/backend.go::Backend`,
  `internal/server/mcp_backend.go::Server.MCPBackend`).
- The backend composes provider and spoke-local interfaces. Provider reads and
  workflow state use the hub; clone, workspace, runtime, and
  agent-session tools stay local, and hub outages remain typed
  (`internal/mcpserver/backend.go::NewFederatedBackend`).
- Every MCP repository and item reference carries provider-verified `platform_repo_id`;
  local tools resolve it to the active repository's current route, including after renames
  (`internal/server/mcp_backend.go::mcpBackend.resolveRepository`).
- Hub-backed workspace tools verify that the repository descriptor still matches the supplied
  stable ID; route-only fallback must not redirect an existing selection
  (`internal/server/mcp_backend.go::mcpBackend.resolveProviderRepository`).
- Target MCP `2026-07-28`; do not advertise deprecated logging or catalog
  notifications on stateless HTTP; Kata tool availability refreshes on each request
  (`internal/mcpserver/server.go::Server.httpHandler`).
- Use only canonical `kenn-forge` command/resource/prompt names and
  `kenn_forge_*` tools; do not add aliases (`internal/mcpserver/server.go::Server.registerTools`).
- Workflow reads and writes are hub-owned provider-adjacent state;
  workspace state stays local. The surface must not expose provider mutations,
  arbitrary commands, terminal bytes, lifecycle cleanup, or `removed_upstream`
  items through workflow reads or writes
  (`internal/server/mcp_backend.go::mcpBackend.ListWorkflowStates`,
  `internal/server/mcp_backend.go::mcpBackend.SetWorkflowState`).
- Candidate output defaults to 25 and caps at 100. Apply candidate `item_types`
  to Activity before its 5,000-row internal safety window
  (`internal/mcpserver/tools_candidates.go::Server.findReviewCandidates`,
  `internal/server/mcp_backend.go::mcpBackend.ListActivity`).
- Treat only typed not-stacked responses as absence; surface other evidence
  failures with structured retry and ambiguity state
  (`internal/mcpserver/tools_stack.go::isStackAbsentError`).
- Tool failures set MCP `isError` and preserve kind, code, retryability,
  ambiguity, and details as JSON content plus `io.kenn.forge/error` metadata;
  never reduce typed backend failures to message-only errors
  (`internal/mcpserver/tools_read.go::wrapTool`).
- Each full-diff handoff stays within 10 MiB. The request-based LRU temp store
  defaults to 128 MiB through `[mcp].diff_cache_mb`. Stage replacements fully
  before mutating published state: a failed write never removes the same-name
  diff or evicts others; replacement is an atomic rename reusing the replaced
  entry's budget. An entry whose eviction fails stays counted for a later
  retry and must not stop eviction of newer entries. Returned diff paths may
  be replaced or evicted by any later write. Represent files without text hunks using minimal Git-style headers,
  including `/dev/null` markers for empty added/deleted files and binary
  difference evidence for binary renames and copies; one empty text patch must
  not discard the rest of the diff. Temporary diff identity case-folds
  repository names only when the provider requires it
  (`internal/config/config.go::Config.MCPDiffCacheBytes`,
  `internal/mcpserver/difftmp.go::diffFileStore.write`,
  `internal/mcpserver/tools_diff.go::serializeDiffPatches`,
  `internal/mcpserver/tools_diff.go::canonicalDiffFileRef`).
- Initial-message attempts are process-local and retain the exact normalized
  prompt only in daemon memory. Same-daemon retries must match the live runtime
  target and prompt; daemon restart permits a fresh attempt
  (`internal/server/workspaceapi/initial_message.go::initialMessageAttempt`).
- Agent messages must be non-blank valid UTF-8; only terminal agents also limit them to LF or
  printable Unicode within 64 KiB. ACP prompts have no further limits, so validate against
  the target's protocol before creating anything
  (`internal/server/workspaceapi/initial_message.go::normalizeAgentMessage`).
- Terminal initial input requires an exact live agent runtime and matching target
  and tracked bracketed paste for multiline text. Hook
  observation is not a submission precondition. If safe paste mode is not
  observed yet, release the no-write reservation and retry only that typed
  condition on the same runtime until the handoff deadline. Terminal writes
  honor the handoff context and do not hold the session lock while waiting.
  Other proven no-write rejection releases its reservation; a timed-out write
  that may have started remains uncertain
  (`internal/workspace/localruntime/manager.go::Manager.SubmitInitialMessage`,
  `internal/server/workspaceapi/initial_message.go::Handler.SubmitInitialMessageService`).
- Shutdown contract: stop MCP admission, wait the bounded grace period, cancel
  in-flight handler contexts and force-close connections, and only then close
  the MCP temp store and database; handlers must honor request-context
  cancellation (`cmd/kenn-forge/main.go::runMainShutdown`).
- MCP-created pull-request and issue workspaces suppress optional automatic
  assignment; ordinary UI omission preserves configured self-assignment
  (`internal/server/workspaceapi/routes_handlers.go::Handler.CreatePullWorkspace`,
  `internal/server/workspaceapi/routes_handlers.go::Handler.CreateIssueWorkspaceService`).
- MCP can create or reuse a pull-request, issue, or ad-hoc workspace and launch
  one new agent runtime with one initial message. It passes that message at launch
  for native CLI delivery when supported, then confirms submission before
  waiting for the runtime's matching hook session. Its readiness wait and
  input-not-ready retry loop are the shared handoff package's, the same code the
  workspace agent-handoff endpoint uses; only the transport calls and error
  mapping stay in the MCP tool (`internal/workspace/agenthandoff/agenthandoff.go::Deliver`). A resume names the existing
  workspace and runtime, repeats the same target and prompt through the
  runtime-scoped duplicate guard, and never launches another agent. Ambiguous
  workspace or runtime mutations are never retried or cleaned up. The exact
  `workspaceAlreadyExists` pull-workspace conflict receives one authoritative
  pull read and reuses the concurrent winner; only initial-message status
  receives a bounded, cancellation-independent read
  (`internal/mcpserver/tools_agent_spawn.go::Server.resolveOrCreatePRWorkspace`,
  `internal/mcpserver/tools_agent_spawn.go::Server.recoverInitialMessageStatus`).
- Configured agent target keys are not hook agent identities; custom targets
  must remain launchable and handoffs match the live runtime and target key
  (`internal/mcpserver/tools_agent_spawn.go::Server.waitForCodingSession`).
- Agent-session inspection returns live agent runtimes separately from
  reported sessions. `hook_observed=false` distinguishes a launched
  runtime awaiting its first report from a workspace with no agent runtime
  (`internal/mcpserver/tools_agent.go::Server.listWorkspaceAgentSessions`).
- MCP agent tools cover terminal and ACP agents; ACP sessions come only from `agent=acp`
  owner reports on ACP runtimes, never hooks (`internal/server/workspaceapi/agent_sessions.go::reportedAgent`).
- Follow-up MCP messages address one existing live agent runtime by workspace ID
  and runtime session key, then return without launching, persisting, or
  waiting for activity. Terminal follow-ups reuse the serialized bracketed-paste
  and Enter path; ACP follow-ups queue behind a running turn
  (`internal/mcpserver/tools_agent.go::Server.sendAgentMessage`,
  `internal/workspace/localruntime/manager.go::Manager.SubmitAgentMessage`).
- An omitted MCP agent target selects the most-used available workspace agent
  from the prior 14 days; ties prefer recent use, then key, and empty history
  falls back to configured order (`internal/mcpserver/tools_agent_spawn.go::Server.defaultAgentTarget`).
- Handoff success and failure evidence uses `stage` plus
  `initial_message.state`; never add a separate `message_delivered` output or
  error detail (`internal/mcpserver/tools_agent_spawn.go::spawnWorkspaceWithAgentOutput`).

- ACP target tools bind the workspace through its owner proxy; terminal MCP clients supply the launch-context ID. Register every worked-on stack layer explicitly
  (`internal/mcpserver/tools_workspace_targets.go::workspaceScope`).
- Workspace target writes use MCP's in-process backend; HTTP exposes only the
  list needed by the UI. Add HTTP writes only when a concrete client needs them
  (`internal/server/workspaceapi/handler.go::Handler.RegisterExecution`).

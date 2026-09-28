# External PR Context

- External integrations are node-local CLI commands configured only through
  `[[external_context]]` in TOML. Settings writes reject that section; ordinary
  saves preserve it (`internal/config/config.go::Config.Save`).
- Require an absolute executable path. Credentials and service-specific rules
  belong to the external adapter, never the browser or Forge provider code
  (`internal/config/config.go::ExternalContextSource`).
- The connected node supplies its own sources regardless of workspace host;
  spokes obtain PR metadata from the hub's synced copy. These routes have no
  federation peer scope (`internal/server/provider_route_policy.go::providerRouteDeclarations`).
- Requests carry no viewer identity; the node's adapter credentials determine card output
  (`internal/externalcontext/runner.go::Runner.invoke`). Every user who can open that PR
  on that node can see the card. Configure private adapters only where all such users are authorized to see their output.
- Actions compare stable repository identity and the displayed SHA against
  the last synced PR, not a live provider read. Send that SHA unchanged; the
  adapter must pin commit-specific work or reject it (`internal/server/external_context.go::Server.externalContextPull`).
- Invoke an action once without a preceding read. The adapter validates action
  IDs and permissions. Never retry actions automatically; failures offer
  Refresh and disclose possible submission when execution started
  (`internal/externalcontext/runner.go::Runner.Action`).
- Share reads across tabs for the complete source/config and PR snapshot.
  Cache outcomes for five seconds, bounded to 64 entries; invalidate around
  actions and on config reload (`internal/externalcontext/runner.go::Runner`).
- Limit external reads and actions together to two processes before acquiring
  a global procutil slot. Each invocation owns its timeout; cancelling one
  reader only stops that reader's wait (`internal/externalcontext/runner.go::Runner`).
- Card payloads remain in memory. Do not add them to provider comments/checks,
  timelines, notifications, agent context, federation payloads, or Forge MCP
  responses. A CLI adapter may use any upstream transport; no second Forge
  transport is planned.

## Protocol

Run the configured argument vector unchanged, with one JSON request on stdin
and one response on stdout. Stderr is diagnostic only. Exit zero requires a
valid response. Bound stdout to 1 MiB and stderr to 64 KiB; errors returned to
the browser exclude raw command output (`internal/externalcontext/runner.go`).

Requests contain `version: 1`, `operation` (`read` or `action`), and
`pull_request`. Actions also contain `action_id`. Adapters reject unsupported
versions. The PR object contains `provider`, `platform_host`,
`platform_repo_id`, `repo_path`, `number`, `url`, `state` (`open`, `merged`,
`closed`), `head_sha`, and `base_sha`. All metadata is last-synced; comments,
body text, and diffs are excluded (`internal/externalcontext/types.go::PullRequest`).

For the same resolved repository, `pull_request.platform_repo_id` and the archive
snapshot's `repositories[].provider_id` carry the same `repo.PlatformRepoID` value
(`internal/server/external_context.go::Server.externalContextPull`, `internal/archive/snapshot.go::Service.snapshot`).
Match across contracts using provider, host, and this opaque ID. For GitHub it is
the node ID (`node_id`), not the numeric database ID (`id`)
(`platform/github/provider.go::GitHubPlatformRepository`).

Responses contain `card`, with null meaning not applicable. A card has
`status` (`neutral`, `pending`, `success`, `warning`, `error`) and `summary`.
Optional fields are `markdown`, `result_head_sha`, `actions`, and
`refresh_after_seconds`. Each action has `id`, `label`, and optional
`disabled_reason`. Action responses use the same envelope. Preserve the
nullable card in generated schemas and clients
(`internal/externalcontext/types.go::ExternalContextCard.TransformSchema`).

`summary` and `markdown` are display text; nothing parses them for list ranking or filtering.

Summaries allow 4096 bytes, Markdown 512 KiB, and up to 32 actions. Action IDs
must be unique and nonblank (128 bytes maximum); labels allow 256 bytes and
disabled reasons 4096 bytes (`internal/externalcontext/runner.go::decodeResult`).

Poll only mounted, visible cards; requested intervals are at least five
seconds. Manual Refresh bypasses completed cached results and shares an
in-flight read. A result SHA different from the displayed PR head is visibly
stale; an absent result SHA does not attest to current-head coverage
(`frontend/src/lib/components/detail/ExternalContextCard.svelte`). Omitting
the interval or setting it to zero disables polling.

Adapters enqueue long-running work and return a pending card promptly. They
own upstream authentication, repository applicability, business rules, and
authorization. They must match repository identity before using upstream APIs
that address PRs only by number.

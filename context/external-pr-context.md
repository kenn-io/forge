# External PR Context

## Boundaries

- External integrations are node-local CLI commands configured only through
  `[[external_context]]` in TOML. Settings writes reject that section; ordinary
  saves preserve it (`internal/config/config.go::Config.Save`).
- Require an absolute executable path. Credentials and service-specific rules
  belong to the external adapter, never the browser or Forge provider code
  (`internal/config/config.go::ExternalContextSource`).
- The connected node supplies its own sources regardless of workspace host;
  spokes obtain PR metadata from the hub's synced copy. These routes have no
  federation peer scope (`internal/server/routepolicy/provider_route_policy.go::ProviderRouteDeclarations`).
- Requests carry no viewer identity; the node's adapter credentials determine card output, so
  every user who can open that PR on that node sees it. Configure private adapters only where all
  such users are authorized to see their output (`internal/externalcontext/runner.go::Runner.invoke`).
- Actions compare stable repository identity and the displayed SHA against the last synced PR,
  not a live provider read. Send that SHA unchanged; the adapter must pin commit-specific work or
  reject it (`internal/server/external_context.go::Server.externalContextPull`).
- Invoke an action once without a preceding read; the adapter validates action IDs and
  permissions. Never retry actions automatically; failures offer Refresh and disclose possible
  submission when execution started (`internal/externalcontext/runner.go::Runner.Action`).
- External reads and actions together run at most two processes. Each invocation owns its
  timeout; cancelling one reader only stops that reader's wait (`internal/externalcontext/runner.go::Runner`).
- Card payloads remain in memory. Do not add them to provider comments/checks, timelines,
  notifications, agent context, federation payloads, or Forge MCP responses. A CLI adapter may
  use any upstream transport; no second Forge transport is planned.

## Protocol

Run the configured argument vector unchanged, with one JSON request on stdin
and one response on stdout. Stderr is diagnostic only. Exit zero requires a
valid response. Bound stdout to 1 MiB and stderr to 64 KiB; errors returned to
the browser exclude raw command output (`internal/externalcontext/runner.go`).

Requests contain `version: 1`, `operation` (`read` or `action`), and
`pull_request`. Actions also contain `action_id`, plus an `input` string only
when the user submits text for an action that declared `input`. Adapters reject
unsupported versions, input for actions that do not declare it, and missing
input for actions that do. The PR object contains `provider`, `platform_host`,
`platform_repo_id`, `repo_path`, `number`, `url`, `state` (`open`, `merged`,
`closed`), `head_sha`, and `base_sha`, plus `bitbucket_repository_uuid` for
Bitbucket Cloud, whose `platform_repo_id` is 0. All metadata is last-synced;
comments, body text, and diffs are excluded
(`internal/externalcontext/types.go::PullRequestJSON`).

For the same resolved repository, `pull_request` and the archive snapshot's
`repositories[]` carry the same repository key: `platform_repo_id` /
`provider_id` hold the provider's integer repository ID (GitHub's numeric `id`,
never `node_id`) as a JSON integer, and Bitbucket Cloud sets
`bitbucket_repository_uuid` instead (`platform/repository_key.go::RepositoryKey`).

Responses contain `card`, with null meaning not applicable. A card has
`status` (`neutral`, `pending`, `success`, `warning`, `error`) and `summary`.
Optional fields are `markdown`, `result_head_sha`, `actions`,
`refresh_after_seconds`, and `recommendation`. A `recommendation` has a
`verdict` of `Merge` or `Hold` and a nonblank `reason` of at most 512 bytes;
the card shows it as advice that changes no merge rule or action
(`internal/externalcontext/runner.go::decodeResult`). Each action has
`id`, `label`, and optional `disabled_reason` and `input`. An `input` object,
with optional `placeholder`, makes the card collect nonblank text before
submitting. Action responses use the same envelope. Preserve the nullable card
in generated schemas and clients
(`internal/externalcontext/types.go::ExternalContextCard.TransformSchema`).

`summary` and `markdown` are display text; nothing parses them for list ranking or filtering.

Summaries allow 4096 bytes, Markdown 512 KiB, and up to 32 actions. Action IDs
must be unique and nonblank (128 bytes maximum); labels allow 256 bytes and
disabled reasons 4096 bytes, input placeholders 256 bytes, and an input's
optional `max_length` up to 16384 characters, where omitted or zero means 16384
(`internal/externalcontext/runner.go::decodeResult`). Submitted input allows
16384 characters. The card applies `max_length` to its text box only; the
adapter still enforces its own limit. Browsers count `max_length` in UTF-16
code units and may cut a paste to fit, so adapters should accept at least that
many characters.

Requested refresh intervals are at least five seconds. Omitting the interval or
setting it to zero disables polling after successful reads. Failed reads
without an interval retry every 15 seconds while visible; failed actions still
require manual Refresh. A result SHA different from the displayed PR head is
visibly stale; an absent result SHA does not attest to current-head coverage.

Adapters enqueue long-running work and return a pending card promptly. They
own upstream authentication, repository applicability, business rules, and
authorization. They must match repository identity before using upstream APIs
that address PRs only by number.

## Card presentation

- Pending reads without a card stay out of the PR layout, and a source returning null must never
  flash a loading card. Keep existing cards visible during refresh
  (`frontend/src/lib/components/detail/ExternalContextCard.svelte`).
- Neutral cards show no status chip. With Markdown, the summary labels the Details disclosure and
  actions that declare `input` render inside it; the recommendation shows above Details.
- Input drafts survive PR navigation and head changes and clear only after the action succeeds.
  A card mounted with an unsent draft opens its details and stays open after the draft clears.
- Poll only mounted, visible cards. Manual Refresh bypasses completed cached results and shares
  an in-flight read.
- Reads run once per pull request identity and head, never for a detail still showing the
  previous pull request.
- A source-list config change missed during a stale reconnect needs a page reload, which the
  maintainer accepts (`frontend/src/lib/stores/external-context-workflow.svelte.ts::ExternalContextWorkflowLive`).

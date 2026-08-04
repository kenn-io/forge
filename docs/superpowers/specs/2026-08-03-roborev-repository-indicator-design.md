# Roborev Repository Indicator Design

## Goal

Show a compact indicator at the bottom-right of each Repositories card when
Roborev is configured for that provider repository. A repository is configured
when at least one Roborev-tracked local checkout with the same remote identity
has a Roborev-managed `post-commit` hook installed.

The indicator is informational only. It does not install, repair, or remove
hooks and does not make the Repositories view depend on Roborev availability.

## User Experience

The existing card footer keeps its sync state and timestamp. A muted 16px Bot
icon appears as the final footer item, placing it at the card's bottom-right.
The icon has the accessible label and tooltip `Roborev hooks installed`. It is
not clickable and carries no count or additional badge.

Cards without a matching hooked checkout do not reserve space for the icon.
If Roborev is unavailable or the configuration lookup fails, repository cards
still render normally and omit the indicator.

## Configuration Endpoint

Kenn Forge exposes a dedicated read-only Roborev configuration endpoint rather
than adding optional daemon and filesystem work to `/repos/summary`. The
response contains deduplicated provider repository references for identities
with at least one hooked checkout. It never exposes Roborev's local checkout
paths.

The handler obtains Roborev's tracked checkout inventory from `/api/repos`.
Each non-local remote identity is parsed through Kenn Forge's existing project
remote parser with the configured provider-host catalog. Matching uses the full
provider route identity: provider, normalized host, and repository path. This
keeps GitHub, GitLab, Forgejo, and Gitea support provider-neutral, including
self-hosted providers and GitLab nested namespaces.

For each tracked checkout, the handler asks Git for the effective
`hooks/post-commit` path. This respects worktrees and `core.hooksPath`. The hook
counts only when it is a regular executable file containing a Roborev-generated
marker or a supported current or legacy Roborev post-commit invocation. A
tracked checkout without that hook is not configured. Multiple hooked
checkouts with the same remote identity produce one response entry.

## Caching and Cost

The probe is lazy, process-local, and single-flight. The first endpoint request
loads the Roborev checkout inventory; concurrent requests share the same work.
A successfully loaded inventory is retained for the Kenn Forge process
lifetime because repository registration and hook installation are not
expected to change during an ordinary server run.

Hook results are cached per tracked checkout for the process lifetime when the
probe reaches a definitive installed or absent result. Endpoint requests then
aggregate those cached booleans into an in-memory identity set. Duplicate
effective hook paths are read once, and hook-path resolution runs with bounded
concurrency so a large historical Roborev inventory does not create an
unbounded process burst.

Transient daemon, Git, or filesystem errors are not turned into permanent
negative results. They receive a short retry cooldown and are coalesced by the
same single-flight path. A successful empty Roborev inventory is definitive and
is cached like any other successful inventory. This keeps the normal card-load
cost to an API call backed by in-memory lookups while preserving recovery from
startup ordering and temporary local failures.

## Frontend Data Flow

The Repositories page loads summaries through the existing endpoint and loads
the Roborev configuration endpoint independently. Summary success controls the
page state; Roborev lookup failure does not become a page error.

The page converts configured repository references into the same canonical
provider/host/repository-path key used by repository summaries and passes a
boolean to each card. The card owns only presentation and does not call either
API itself. Reloading or refreshing the page reuses the server's cached probe.

## Error Handling

- An unavailable Roborev daemon returns an ordinary typed service error from
  the dedicated endpoint; the frontend treats it as an absent optional signal.
- Unparseable or local-only Roborev identities are ignored.
- Missing checkout paths and unreadable or non-Roborev hook files do not mark a
  repository configured.
- A failure for one checkout does not discard definitive positive matches from
  other checkouts and remains eligible for retry after the cooldown.

## Verification

Server tests cover remote identity normalization, self-hosted and nested paths,
multiple checkout deduplication, effective hook-path detection, executable and
Roborev-content requirements, process-lifetime caching, concurrent request
coalescing, bounded probing, and transient failure retry behavior. A real HTTP
and SQLite server test pins the endpoint response shape and verifies that an
unavailable Roborev daemon does not affect `/repos/summary`.

Frontend component tests cover an icon appearing only on a matching card, the
accessible label, no reserved icon space for unmatched cards, and graceful
behavior when the optional endpoint fails. Final verification runs the full
frontend unit suite and the affected repository Playwright suite after the last
frontend or fixture edit.

## Non-Goals

- Installing, repairing, or removing Roborev hooks from Kenn Forge.
- Showing review counts, daemon health, hook versions, or individual checkout
  paths on repository cards.
- Polling for hook changes during a Kenn Forge process lifetime.
- Changing the existing Reviews view or Roborev daemon API.

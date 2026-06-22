---
name: context-sync
description: Scan middleman's context docs for staleness against the current Go/Svelte code, verify anchored claims, surface knowledge gaps, and draft reviewed updates. Use on demand when context/ docs, the root CLAUDE.md, or docs/ specs may have drifted from the code, after large refactors, or when an agent hits a gotcha context should have prevented.
argument-hint: "[area] [--check | --audit-claims]"
---

# Context Sync (middleman)

Keep middleman's context system honest: scan the context docs, compare them against
the current code, detect drift and knowledge gaps, ask the maintainer to fill them,
and draft updates for review. Adapted for this repo's Go + Svelte stack and its
single-surface context layout: root `CLAUDE.md` routes to flat `context/*.md` topic docs.

**Arguments:**
- No args: sync every area in the area map below.
- `$1 = area`: sync one area (see Area Map, e.g. `platform`, `github-sync`, `db`,
  `server`, `frontend`, `errors`, `testing`, `mobile`, `kata`, `docs`, `messages`).
- `--check`: report staleness only, propose nothing.
- `--audit-claims`: run the four-tag claim verification (see `claim-verifier.md`) over
  every anchored claim in the scanned docs.

## Stop Hook Completion

This repo installs Claude Code and Codex `Stop` hooks that require context-sync to check
the current worktree state before a turn completes. When a Stop hook asks for context
sync, run `context-sync --check` first. If it reports drift, address the drift or report
the findings before marking the state as checked.

After any successful `context-sync --check` or full context-sync run, mark the current
worktree state:

```bash
scripts/hooks/context-sync-stop.sh mark
```

Do not mark first. The marker is only a loop guard proving the current git worktree
fingerprint has already been checked.

## Step 1: Load the Guide

Read `context-guide.md` in this skill directory. It carries the full philosophy: the
grep test, anchored-claim format, what belongs in the hub vs. a topic doc, size limits,
and how invariant claims map to Go guard tests/analyzers rather than Python probes.
Internalize it before proposing any change.

## Step 2: Validate the Manifest

middleman routes context from the root `CLAUDE.md` (Conventions, Provider Support, and
Non-Provider Modes sections reference `context/*.md` and `docs/superpowers/specs/*`).
Check that:

- every `context/*.md` reference in `CLAUDE.md` points to a file that exists;
- every `context/*.md` and `docs/**/*.md` that an agent would need is reachable from
  `CLAUDE.md` or from another reachable doc (orphaned context is invisible);
- `AGENTS.md` still resolves to `CLAUDE.md` (it is a symlink).

Report broken links and orphaned docs. Unreachable context is a high-priority gap.

## Step 3: Scan Areas

For each area (or just `$1`):

Dispatch a read-only subagent (Agent tool, `Explore` or `general-purpose`). Give it:

- the relevant `context/*.md` topic doc(s) for the area, plus any section of the root
  `CLAUDE.md` that governs it;
- the code paths that area covers (see Area Map);
- the anchored-claim and four-tag rules from `context-guide.md`.

Collect from each subagent:

- a proposed diff for the topic doc (and/or the relevant `CLAUDE.md` section);
- a one-paragraph summary of what drifted and why;
- knowledge-gap questions (things the code cannot answer on its own);
- anchor verification results — per anchor: resolves / moved / gone;
- any invariant claim that should be (but is not) backed by a Go guard test/analyzer.

If `--check` was passed, report the summaries and stop here.

If `--audit-claims` was passed, additionally dispatch a claim-verifier subagent per doc
using `claim-verifier.md` as its instructions, and run the four-tag verification over
every anchor.

### Area Map

| Area | Topic doc(s) | Code it must track |
|------|--------------|--------------------|
| `platform` | `context/provider-architecture.md`, `context/platform-sync-invariants.md` | `internal/platform/` (registry, types, metadata, persist), `internal/platform/<provider>/` |
| `github-sync` | `context/github-sync-invariants.md` | `internal/github/` (sync, graphql, client, transports) |
| `db` | `context/db-migrations.md`, `context/embeds.md` | `internal/db/`, `internal/db/migrations/` |
| `server` | `context/workspace-apis.md`, `context/workspace-runtime-lifecycle.md` | `internal/server/`, `internal/apiclient/generated/` |
| `errors` | `context/error-handling.md` | error envelopes across `internal/server/`, frontend error branching |
| `retries` | `context/retries-and-backoffs.md` | retry/backoff/single-flight paths against upstreams |
| `testing` | `context/testing.md` | `internal/server/apitest/`, `internal/server/e2etest/`, test helpers |
| `frontend` | `context/ui-design-system.md`, `context/ui-interaction-contracts.md`, `context/vscode-workflow-panel-interaction-spec.md` | `frontend/src/` |
| `mobile` | `context/mobile-ux.md` | `frontend/src/` `/m` routes and phone-first components |
| `kata` | `docs/superpowers/specs/2026-06-08-kata-docs-msgvault-modes-design.md` | `internal/kata/` |
| `docs` | same spec as `kata` | `internal/docs/` |
| `messages` | same spec as `kata` | `internal/messages/msgvault/` |

When `internal/docs/`, `internal/kata/`, or `internal/messages/msgvault/` graduate from
the shared modes design spec to their own `context/*.md` docs, add rows here and a
routing reference in `CLAUDE.md`.

## Step 4: Check Design Decisions

middleman records durable decisions in `docs/adr/` (for example
`docs/adr/0001-utc-datetime-policy.md`). Scan recent conversation history and recent
commits for design decisions or domain knowledge the maintainer stated that are not yet
captured in an ADR or topic doc. Propose where each belongs (ADR vs. topic doc).

## Step 5: Research, Suggest, Then Ask

For each knowledge gap, do not ask a bare question. Follow the research-then-suggest
pattern in `context-guide.md`:

- general technical patterns: search for current best practice first, then propose;
- middleman-specific domain knowledge: ask the maintainer directly.

Present confidence-tagged suggestions the maintainer can confirm or correct.

## Step 6: Check Invariant Guards

middleman encodes hard invariants as Go tests and custom analyzers under `tools/` and
`internal/server/{apitest,e2etest}/`, not as Python probes. For each invariant a context
doc asserts (provider identity tuple, capability gating, UTC datetimes, stable error
codes, no-`net/http`-mux, testify-only assertions), confirm a guard exists. If an
invariant is documented but unguarded, flag it as a gap and propose the smallest guard
(a Go test or an analyzer like `tools/nohttpmux` / `tools/migrationhistorycheck`).

When a guard and a doc disagree, that is a high-priority finding for Step 7.

## Step 7: Present Diffs

Show all proposed changes. Flag DELETIONS and MODIFICATIONS separately from additions —
deletions are higher risk (removing context that was actually needed is harder to
recover from than adding noise). Wait for maintainer approval before applying anything.

## Step 8: Apply, Route, Commit

Apply approved changes. If a new area/topic doc was created, add its routing reference
to the root `CLAUDE.md`, and add symlinks for any new skill under both `.claude/skills/`
and `.agents/skills/` (each pointing to `../../skills/<name>`). Then commit following the
repo's git workflow: a conventional-commit subject that states the user-visible reason
(for example `docs: realign platform-sync context with capability registry`), a body
explaining what drifted, and the project's standard hook-enforced commit path. Do not
amend; do not bypass hooks.

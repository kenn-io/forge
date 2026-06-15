# Context Engineering Guide — middleman

Loaded during a `context-sync` run. The complete philosophy and rules for keeping
middleman's context system aligned with its code. This guide is adapted from a generic
context-engineering system to middleman's actual stack (Go backend, Svelte 5 SPA, a
small Rust pty-manager) and its single-surface context layout.

## Core Principle: The Grep Test

Every line in a context file must fail this test: "Could an agent discover this by
reading the code or running `rg`?" If yes, delete it.

**INCLUDE:** domain knowledge, design rationale, cross-cutting invariants, gotchas,
non-obvious conventions, and routing between docs.
**EXCLUDE:** file listings, function signatures, struct fields, import graphs, and code
style that `golangci-lint` / `gofmt` / `oxfmt` already enforce.

### Good vs Bad Context

BAD (greppable): "`Registry` is defined in `internal/platform/registry.go`."
GOOD (not greppable): "Provider identity is always the tuple `(platform, platform_host,
owner, name)`; code that keys repos by `owner/name/number` alone will silently collide
across hosts once a self-hosted Forgejo and github.com share an owner."

BAD (greppable): "`internal/github/graphql.go` has a `BulkFetch` function."
GOOD (not greppable): "GitHub GraphQL bulk fetch is an optimization layered *around* the
neutral persistence path, not through it — keep it optional so other providers keep
working when the GraphQL path is skipped."

## Single-Surface Layout

middleman's durable agent context lives in two places:

- **Root `CLAUDE.md`** — the hub. Project overview, architecture, the single canonical
  provider list, non-provider modes, project structure, key files, dev/test commands,
  conventions, git/PR workflow, and routing references into `context/`.
- **`context/*.md`** — topic docs. One concern each (provider sync invariants, error
  handling, db migrations, retries, testing discipline, UI design system, mobile UX,
  etc.). Deep enough to hold real rationale; narrow enough to load only when relevant.

Specs and plans live under `docs/superpowers/specs/` and `docs/superpowers/plans/`;
durable, dated decisions live under `docs/adr/`.

### The Sorting Test

When a new fact arrives, ask: does it describe the *whole project's* shape or workflow
(→ root `CLAUDE.md`), one *concern in depth* (→ the matching `context/*.md`), a *dated
decision* you chose between alternatives (→ `docs/adr/`), or a *feature still being
designed* (→ `docs/superpowers/specs/`)? Put it in exactly one home and route to it.

## Anchored Claims

Every factual claim in `context/*.md` and in fact-bearing sections of `CLAUDE.md` should
cite a code location so it can be verified and so drift is detectable.

**Format:**
- `` `path/to/file.go::SymbolName` `` — preferred; symbol anchors survive reformatting
  and line shifts. Works for Go (`registry.go::Registry`), TypeScript, and Svelte.
- `` `path/to/file.go:123` `` — only when the target is genuinely nameless (a specific
  SQL string in a migration, an inline constant, a literal route string).
- A directory anchor (`` `internal/platform/` ``) is acceptable for claims about a
  package's role rather than a single symbol.

**What needs an anchor:** factual claims — behavior, invariants, the identity of a
function/type/route/migration/capability.
**What doesn't:** rationale, opinions, design motivation, "why we avoid X".

This repo already uses this anchor style informally throughout `context/`. The sync run
verifies anchors by resolving each with `rg` against the worktree. A future
`mise run check-anchors` task (a small Go tool under `tools/`, in the family of
`tools/nohttpmux` and `tools/migrationhistorycheck`) can make this a pre-commit gate;
until then, anchor resolution is part of the sync.

## Four-Tag Verification (`--audit-claims`)

Each anchored claim gets one tag:

- VERIFIED — found in code, still accurate; anchor refreshed if the symbol moved.
- OUTDATED — found, but the claim no longer matches behavior; write the delta.
- GONE — the symbol/type/route/migration no longer exists; flag for removal.
- UNVERIFIABLE — a behavioral or judgment claim ("fastest when…", "safe because…");
  flag for maintainer review.

Structural drift (renames, moves, deletions) is mechanical and the verifier resolves it.
Behavioral drift is where `--audit-claims` flags UNVERIFIABLE rows, and where a Go guard
test should exist if the invariant matters.

## Invariant Guards (the Go analogue of probes)

The most fragile invariants should be protected by runnable code, not prose. In
middleman that means Go tests and custom analyzers, not Python probes. Examples of
invariants that warrant a guard:

- Provider identity is the full `(platform, platform_host, owner, name)` tuple
  everywhere; routes carry `provider`/`platform_host`.
- Provider capability differences go through the capability model and return typed
  `unsupported_capability` errors — never a silent GitHub-only fallback.
- Datetimes are UTC at storage and API boundaries, RFC3339 on the wire
  (`docs/adr/0001-utc-datetime-policy.md`); local conversion only in the Svelte layer.
- Error envelopes branch on stable codes/details, not prose (`context/error-handling.md`).
- No `net/http` mux usage where the repo forbids it (`tools/nohttpmux`).
- Tests use testify assertions, not `t.Fatal`/`t.Error` (`tools/testifyhelpercheck`).

**When a guard fails, three deliberate choices:**

1. Fix the code — the guard was right.
2. Update the guard — the design changed on purpose; record it (ADR or topic-doc note)
   in the same change.
3. Delete the guard — the invariant no longer applies; document why.

Silencing a failing guard without recording the decision is forbidden.

## Where to Store Context

| Content type | Store in | Why |
|--------------|----------|-----|
| Project shape, canonical provider list, modes, key files, workflow | root `CLAUDE.md` | The always-loaded hub |
| Provider-neutral sync identity, tokens, freshness, route shape | `context/platform-sync-invariants.md` | Cross-provider invariants |
| New-provider checklist, package layout | `context/provider-architecture.md` | Onboarding a provider |
| GitHub-only sync behavior (GraphQL, ETag) | `context/github-sync-invariants.md` | Isolated optimization |
| Schema evolution rules | `context/db-migrations.md` | Migrations are the source of truth |
| API error envelopes + frontend branching | `context/error-handling.md` | Stable contract |
| Retry/backoff/single-flight | `context/retries-and-backoffs.md` | Upstream flakiness |
| HTTP test discipline (apitest vs e2etest) | `context/testing.md` | Wire-level testing |
| UI/TS/Svelte conventions, interaction contracts | `context/ui-design-system.md`, `context/ui-interaction-contracts.md` | Frontend consistency |
| Phone-first mobile workflow | `context/mobile-ux.md` | `/m` is its own UX |
| Kata / Docs / Messages mode integration | `docs/superpowers/specs/2026-06-08-kata-docs-msgvault-modes-design.md` | Until dedicated docs exist |
| A decision chosen over an alternative | `docs/adr/NNNN-title.md` | Dated, durable rationale |
| A feature still being designed | `docs/superpowers/specs/YYYY-MM-DD-topic-design.md` | Loaded only when needed |

Every new topic doc MUST get a routing reference from root `CLAUDE.md` (or from another
reachable doc). Unreachable context is invisible to agents.

## Size Guidance

These are soft ceilings; when a doc outgrows one, split by sub-concern rather than
deleting substance. They are not a mandate to shrink today's docs.

| File | Soft max lines | Rationale |
|------|----------------|-----------|
| Each `context/*.md` topic doc | ~250 | One concern, deep but scannable |
| `docs/adr/*` entry | ~80 | One decision |
| `docs/superpowers/specs/*` | as needed | Design surface, archived after landing |

The root `CLAUDE.md` is the hub and is allowed to be long, but it should stay a router
and a conventions list — push any concern that grows rationale into a `context/` doc and
reference it.

## When to Update Context

- A maintainer explains a design decision → capture it promptly (ADR or topic doc).
- A new provider, mode, or cross-cutting invariant lands → add/extend the topic doc and
  route to it from `CLAUDE.md`.
- An agent makes a mistake context would have prevented → add the gotcha.
- A convention changes → update the relevant doc and, if it is an invariant, its guard.
- Code is deleted or restructured → remove the now-GONE context.

## When NOT to Update Context

- Code was reformatted (the formatters own this).
- A function/type was added or renamed (greppable).
- Tests were added (greppable).
- Dependencies changed (`go.mod` / `bun.lock` are authoritative).

## Staleness Signals

- A doc references a file, type, route, or migration that no longer exists.
- A doc describes a pattern the code no longer follows.
- Two docs (or a doc and `CLAUDE.md`) contradict each other.
- An anchor fails to resolve.
- An invariant guard fails.
- The canonical provider list in `CLAUDE.md` is restated (and thus can drift) elsewhere.

## Context Poisoning Safeguard

When proposing updates, present DELETIONS and MODIFICATIONS separately from additions and
justify each removal. Removing context that turns out to be load-bearing is more costly
than leaving a redundant line.

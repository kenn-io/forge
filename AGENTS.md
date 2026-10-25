# kenn-forge Agent Instructions

## Project Model

kenn-forge is a local-first maintainer console. A Go/Huma server syncs
provider-backed pull requests, issues, and activity into SQLite and serves an
embedded Svelte 5 SPA. Kata and Docs are separate first-class modes whose data
remains owned by their external or filesystem domains. The project builds
without CGO; use the README and Makefile for discoverable setup, build, and
development commands.

## Working Rules

These rules win over any skill, hook, context document, or review finding.

1. **Check the code that runs.** Before relying on how existing code or a
   provider behaves, read the call that actually executes and the primary docs
   for that endpoint and version. Mark anything unchecked as unverified, in chat
   and in commit messages; reviewers read commit messages as context.
2. **Make the smallest change.** Reuse existing helpers, components, and types
   before writing new ones. Ask before adding anything the request did not ask
   for: new state, caches, caps, retries, guards, options, compatibility shims,
   or UI. When a bug recurs in another place, fix its cause once.
3. **Answer findings; do not obey them.** Before changing code for a review
   finding, confirm it against the running code. Decline a wrong, unreachable,
   or out-of-scope finding with evidence where the reviewer will see it
   (`roborev comment <job>`, or a PR reply), never only in chat. A finding that
   returns after you addressed it means the design is wrong: stop and rethink.
4. **Done means shown.** Do not say tests pass, CI is green, a bug is fixed, or
   a PR is ready without output from this turn that proves it. A failure is
   flaky only if it also fails on `main` or passes on an unchanged rerun.

## Provider Support

kenn-forge supports GitHub, GitLab, Forgejo, Gitea, and Bitbucket Cloud/Data Center. This is the single
canonical provider list; `gitealike` is the shared Forgejo/Gitea adapter.
Provider-backed features must work across every supported provider within its
declared capabilities and preserve provider-verified stable repository identity;
owner/name remains a mutable route. The routing table below owns the detailed rules.

Before using provider state to gate an action, establish which credential queried
it, which resource revision it describes, and whether it is fresh. Test differing
read/write identities, including an allowed user who needs no bypass and a denied
user. Verify provider assumptions before encoding them in mocks.

## Non-Provider Modes

Kata and Docs are first-class modes, not platform providers. Do not force them
through `platform`: their data remains owned respectively by Kata
daemons and configured filesystem folders.

## Context Routing

Read the smallest relevant set of topic documents before changing or reviewing
the corresponding area. These documents own the detailed invariants; this file
only routes to them.

| When working on | Read |
| --- | --- |
| Provider interfaces or package boundaries | `context/provider-architecture.md` |
| Provider identity, sync, import, routes, or settings | `context/platform-sync-invariants.md` |
| GitHub-specific sync or notifications | `context/github-sync-invariants.md`, `context/notifications-in-activity.md` |
| Config fields that persist to TOML | `context/config-persistence.md` |
| External PR context commands, cards, or actions | `context/external-pr-context.md` |
| Database schema migrations or the SQLite connection layer | `context/db-migrations.md` |
| Deferred merge behavior | `context/deferred-merge.md` |
| Daemon startup, discovery, host/origin validation, or SSE replay | `context/server-runtime.md` |
| Activity relay protocol, consumer, or status | `context/server-runtime.md`, `context/github-sync-invariants.md` |
| Fleet settings, snapshots, host routing, or peer transports | `context/fleet-architecture.md` |
| API failures or frontend error branching | `context/error-handling.md` |
| Retries, rate limits, scheduling, or single-flight work | `context/retries-and-backoffs.md` |
| Go test commands, assertions, fixtures, or shell tests | `context/testing-basics.md` |
| Test lanes, provider tests, API contracts, or HTTP tests | `context/testing.md` |
| Repository-controlled session bootstrap or dependency installation | `context/agent-bootstrap.md` |
| User documentation, screenshots, or the Zensical site | `context/docs-authoring.md` |
| CI runner policy, pushing, opening a pull request, or changing PR metadata, comments, or review threads | `context/pull-request-workflow.md` |
| Frontend visual design or component conventions | `context/ui-design-system.md` |
| Frontend Effect workflows, services, layers, errors, or async ownership | `context/frontend-effect.md` |
| Frontend interaction, route state, persistence, or input semantics | `context/ui-interaction-contracts.md` |
| Phone routes, narrow layouts, or touch UX | `context/mobile-ux.md` |
| Workflow or terminal panel interaction models | `context/vscode-workflow-panel-interaction-spec.md` |
| MCP companion tools, transports, safety boundary, or agent handoff | `context/mcp-server.md` |
| Worklists, their MCP tools, freshness, or worklist UI | `context/worklists.md`, `context/mcp-server.md` |
| Embeddings config, the semantic index, or semantic search API and MCP tool | `context/semantic-search.md` |
| Workspace APIs, creation, item identity, lifecycle hooks, or generated launch context | `context/workspace-apis.md` |
| Workspace deletion, runtime sessions, tmux, or terminal UI | `context/workspace-runtime-lifecycle.md` |
| Repository source-browser routes, clones, refs, or previews | `context/repository-source-browser.md` |
| Inline diff review drafts, comments, or threads | `context/inline-review-comments.md` |
| Kata task authority, daemon integration, task UI, or Kata workspaces | `context/kata-mode.md`, `context/workspace-apis.md` |
| Markdown folders, Docs APIs, or git publishing | `context/docs-mode.md` |

## Conventions

- Prefer stdlib over external dependencies
- Before hand-rolling file-safety or filesystem helpers, reuse `go.kenn.io/kit` (for example `fslink.ReadFile` for regular-file-only reads, `safefileio` for private files); do not reimplement what Kit already owns
- Never make backward-incompatible changes to the activity relay wire format. Relays and Forge clients upgrade independently; preserve existing field types, meanings, and events, and verify changes against fixed wire fixtures from deployed versions.
- The `kenn-forge` binary has one Cobra root command. Register every public command on that tree; do not add a second parser, manual dispatcher, or command-facing `flag.FlagSet`. (`cmd/kenn-forge/cli.go::newRootCommand`)
- CLI flags must affect execution or fail validation; reject shared persistent flags outside the commands that consume them instead of silently ignoring user input. (`internal/cli/ctl/ctl.go::installControlFlagValidation`)
- Use `huma` for the web framework and OpenAPI generation
- Regenerate API artifacts with `make api-generate`; the Go client also supports `go generate ./internal/apiclient/generated`
- The maintainer's local backend and frontend run under process-compose; never launch unsupervised replacements. Never run `kenn-forge daemon start|restart|stop` against its config.
  `daemon restart` swaps it for an unsupervised detached copy of the invoking binary. (`process-compose.yml`)
- Tests, docs, fixtures, commit messages, and PR text should use generic synthetic examples unless the user explicitly asks to preserve exact private project names, paths, prose, or domain details.
- **Use Bun only for dependency installation** — run `bun install`, and launch Vite+, Vitest, Svelte tooling, and Playwright with Node via `./node_modules/.bin/vp ...` (or `../node_modules/.bin/vp ...` from `frontend/`). Never use npm; it creates `package-lock.json`, which conflicts with the Bun lockfile
- No emojis in code or output
- Sub-second race windows that self-heal on the next event or pass are accepted eventual consistency. Do not fix them, add guards for them, or raise them as review findings; the maintainer has decided this.
- Datetimes are UTC across storage and API boundaries. Store timestamps in UTC, emit API timestamps as UTC RFC3339, and only convert to local time in the Svelte UI presentation layer.

## Roborev

- Never invoke the `roborev review` CLI command in any form unless the user
  explicitly asks for it. Use all other `roborev` CLI commands normally when
  they are appropriate for interacting with roborev. Never invoke a roborev
  skill (including `roborev-fix` or `roborev-design-review-branch`) unless the
  user explicitly asks for that skill.

## Git Workflow

- **Commit and push every turn** — always commit and push your work at the end of each turn, after required checks
- Superpowers design specs and implementation plans are temporary working artifacts; never commit
  or retain them. Before committing, distill current contracts into the matching `context/` topic
  docs and delete the artifacts. Do not convert a transient spec into an ADR to preserve it.
- **Record what the code cannot tell you** — run the `context-sync` skill with `--commit`
  only when the maintainer stated a durable decision or corrected you during this work.
  Context documents hold decisions, invariants, and structure no single file shows; never
  restate what reading the code shows.
- **Never amend commits** — always create new commits for fixes, never use `--amend`
- **Never change branches** — don't create, switch, or delete branches without explicit permission
- **Never bypass pre-commit hooks** — all commits must go through a hook-enforced Git commit path. Do not use `jj` or any other workflow to create, rewrite, or finalize commits in a way that skips the repository's Git hooks
- Kata task identifiers are internal execution metadata; never include them in branch names, commit messages, PR titles or descriptions, or GitHub comments.
- Pull requests must have a user-facing benefit or improve the developer experience, and the body must say which one.
- Write issues and pull requests in English.
- Use conventional commit messages whose subject explains the reason or user-visible outcome, not just the mechanical change. Good subjects answer "why does this commit exist?" (for example, `fix: restore workspace activity for launched agents`), while vague mechanics such as `fix: run agents under tmux` are not acceptable on their own
- Commit bodies must add any important context about the bug, regression, constraint, or tradeoff that motivated the change; do not rely on the diff to explain intent
- Never push unless the repository's non-mutating lint check passes after the final relevant edit; keep local and CI linter versions aligned. (`Makefile`, `prek.toml`, `.github/workflows/ci.yml`)
- Before pushing any frontend change, you must have run the tests covering the changed files locally after the final frontend/test edit (`../node_modules/.bin/vp test run <files>` from `frontend/`), plus the affected Playwright specs whenever the change touches them or the shared mock fixtures they rely on; type checks and CI-only verification are not enough. CI runs the full suites.

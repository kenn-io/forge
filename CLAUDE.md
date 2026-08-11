# kenn-forge Agent Instructions

## Project Model

kenn-forge is a local-first maintainer console. A Go/Huma server syncs
provider-backed pull requests, issues, and activity into SQLite and serves an
embedded Svelte 5 SPA. Kata and Docs are separate first-class modes whose data
remains owned by their external or filesystem domains. The project builds
without CGO; use the README and Makefile for discoverable setup, build, and
development commands.

## Provider Support

kenn-forge supports GitHub, GitLab, Forgejo, and Gitea. This is the single
canonical provider list; `gitealike` is the shared Forgejo/Gitea adapter.
Provider-backed features must work across every supported provider within its
declared capabilities and preserve provider-verified stable repository identity;
owner/name remains a mutable route. The routing table below owns the detailed rules.

## Non-Provider Modes

Kata and Docs are first-class modes, not platform providers. Do not force them
through `internal/platform`: their data remains owned respectively by Kata
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
| Database schema migrations | `context/db-migrations.md` |
| Deferred merge behavior | `context/deferred-merge.md` |
| Embed routes or host bridges | `context/embeds.md` |
| Daemon startup, discovery, host/origin validation, or SSE replay | `context/server-runtime.md` |
| Fleet settings, snapshots, host routing, or peer transports | `context/fleet-architecture.md` |
| API failures or frontend error branching | `context/error-handling.md` |
| Retries, rate limits, scheduling, or single-flight work | `context/retries-and-backoffs.md` |
| Go test commands, assertions, fixtures, or shell tests | `context/testing-basics.md` |
| Test lanes, provider tests, API contracts, or HTTP tests | `context/testing.md` |
| Repository-controlled session bootstrap or dependency installation | `context/agent-bootstrap.md` |
| User documentation, screenshots, or the Zensical site | `context/docs-authoring.md` |
| Pushing, opening a pull request, or changing PR metadata, comments, or review threads | `context/pull-request-workflow.md` |
| Frontend visual design or component conventions | `context/ui-design-system.md` |
| Frontend Effect workflows, services, layers, errors, or async ownership | `context/frontend-effect.md` |
| Frontend interaction, route state, persistence, or input semantics | `context/ui-interaction-contracts.md` |
| Phone routes, narrow layouts, or touch UX | `context/mobile-ux.md` |
| Workflow or terminal panel interaction models | `context/vscode-workflow-panel-interaction-spec.md` |
| Workspace APIs, creation, item identity, lifecycle hooks, or generated launch context | `context/workspace-apis.md` |
| Workspace deletion, runtime sessions, tmux, or terminal UI | `context/workspace-runtime-lifecycle.md` |
| Repository source-browser routes, clones, refs, or previews | `context/repository-source-browser.md` |
| Inline diff review drafts, comments, or threads | `context/inline-review-comments.md` |
| Kata task authority, daemon integration, task UI, or Kata workspaces | `context/kata-mode.md`, `context/workspace-apis.md` |
| Markdown folders, Docs APIs, or git publishing | `context/docs-mode.md` |

## Conventions

- Prefer stdlib over external dependencies
- The `kenn-forge` binary has one Cobra root command. Register every public command on that tree; do not add a second parser, manual dispatcher, or command-facing `flag.FlagSet`. (`cmd/kenn-forge/cli.go::newRootCommand`)
- CLI flags must affect execution or fail validation; reject shared persistent flags outside the commands that consume them instead of silently ignoring user input. (`internal/cli/ctl/ctl.go::installControlFlagValidation`)
- Do the task requested, not the task imagined. Do not widen scope without explicitly confirming with the user first
- When a backwards compatibility adapter, shim, alias, fallback wrapper, or legacy translation layer seems useful, ask the user for EXPRESS permission before introducing it. These shims carry very high maintenance cost because they preserve old paths, multiply edge cases, and make future changes harder to reason about; explain the compatibility benefit and why direct migration or removal is not the better choice.
- Use `huma` for the web framework and OpenAPI generation
- Regenerate API artifacts with `make api-generate`; the Go client also supports `go generate ./internal/apiclient/generated`
- User-facing docs should be concise and workflow-oriented: state the UI capabilities and the maintainer workflows kenn-forge enables, avoid overexplaining internals, and treat the HTTP API as an internal/thin-client concern rather than regular user guidance.
- Local thin clients must not infer startup-bound daemon middleware policy from
  reloadable config; derive required request metadata from the runtime record or
  send it safely when the middleware ignores it (`cmd/kenn-forge/daemon_client.go::discoverDaemonHTTP`).
- User-facing workflow screenshots are generated into a staged docs tree by the docs build and must not be tracked in Git; the Playwright captures in `docs/screenshots/` use the real seeded e2e backend, not mocked API fixtures or a developer daemon.
- Stage Zensical input from an explicit public allowlist; internal plans, specs, ADRs, reports, and screenshot tooling must never enter the staged tree or rendered `site/` output.
- Verify Zensical screenshot asset-path findings against rendered `site/` output; raw HTML source paths can be rewritten when `use_directory_urls` is enabled.
- Zensical resolves `docs_dir`/`site_dir` relative to the config file's directory, so `uvx zensical build` cannot run in place against the checked-in `docs/zensical.toml`; stage a scratch project root containing a copy of the config beside a copy of `docs/`, then build there.
- Tests, docs, fixtures, commit messages, and PR text should use generic synthetic examples unless the user explicitly asks to preserve exact private project names, paths, prose, or domain details.
- **Never use npm** — use `bun install` for frontend dependencies and invoke Vite+ directly via `./node_modules/.bin/vp ...` (or `../node_modules/.bin/vp ...` from `frontend/`). Never run `npm install` or `npm run` — this creates `package-lock.json` which conflicts with the bun lockfile
- No emojis in code or output
- Datetimes are UTC across storage and API boundaries. Store timestamps in UTC, emit API timestamps as UTC RFC3339, and only convert to local time in the Svelte UI presentation layer.

## Roborev

- Never invoke the `roborev review` CLI command in any form unless the user
  explicitly asks for it. Use all other `roborev` CLI commands normally when
  they are appropriate for interacting with roborev. Never invoke a roborev
  skill (including `roborev-fix` or `roborev-design-review-branch`) unless the
  user explicitly asks for that skill.

## Git Workflow

- **Commit every turn** — always commit your work at the end of each turn, no exceptions
- Superpowers design specs and implementation plans are temporary working artifacts; never commit
  or retain them. Before committing, distill current contracts into the matching `context/` topic
  docs and delete the artifacts. Do not convert a transient spec into an ADR to preserve it.
- **Capture context before committing** — before every agent-created Git commit, invoke
  the repository-local `context-sync` skill with `--commit`. Apply clear scoped context
  changes before invoking the normal external commit skill. Block only when an unclear
  durable decision requires maintainer input.
- **Never amend commits** — always create new commits for fixes, never use `--amend`
- **Never change branches** — don't create, switch, or delete branches without explicit permission
- **Never bypass pre-commit hooks** — all commits must go through a hook-enforced Git commit path. Do not use `jj` or any other workflow to create, rewrite, or finalize commits in a way that skips the repository's Git hooks
- Kata task identifiers are internal execution metadata; never include them in branch names, commit messages, PR titles or descriptions, or GitHub comments.
- Use conventional commit messages whose subject explains the reason or user-visible outcome, not just the mechanical change. Good subjects answer "why does this commit exist?" (for example, `fix: restore workspace activity for launched agents`), while vague mechanics such as `fix: run agents under tmux` are not acceptable on their own
- Commit bodies must add any important context about the bug, regression, constraint, or tradeoff that motivated the change; do not rely on the diff to explain intent
- Run tests before committing when applicable
- Never push unless the repository's non-mutating lint check passes after the final relevant edit; keep local and CI linter versions aligned. (`Makefile`, `prek.toml`, `.github/workflows/ci.yml`)
- Before pushing any frontend change, you must have run the full affected suite locally after the final frontend/test edit — the full `vp test` Vitest run, plus the full affected Playwright e2e suite whenever the change touches Playwright specs or the shared mock fixtures they rely on; type checks and CI-only verification are not enough.
- Never push new workstreams unless explicitly asked. When addressing review feedback or CI failures on an existing PR, an agent may push after the fix is implemented and relevant local validation has run.

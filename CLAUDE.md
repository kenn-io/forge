# Kenn Forge Agent Instructions

## Project Model

Kenn Forge is a local-first maintainer console. A Go/Huma server syncs
provider-backed pull requests, issues, and activity into SQLite and serves an
embedded Svelte 5 SPA. Kata and Docs are separate first-class modes whose data
remains owned by their external or filesystem domains. The project builds
without CGO; use the README and Makefile for discoverable setup, build, and
development commands.

## Provider Support

kenn-forge supports GitHub, GitLab, Forgejo, and Gitea. The `gitealike` package is the shared Forgejo/Gitea adapter.

This paragraph is the single place CLAUDE.md enumerates supported providers. Do not duplicate the list elsewhere in this file: not in the architecture diagram, env-var lists, project structure, key files, or test guidance. Adding or removing a provider updates this paragraph only. Mentioning a specific provider in context (for example, GitHub-only optimizations in `internal/github/`) is fine when it describes real artifacts, not when it restates the supported set.

New features must work across every supported provider to the extent each provider's API allows. Concrete rules:

- Provider-specific capability differences go behind the capability model in `internal/platform`. Declare capabilities in `Capabilities()`, check them before mutations, and return typed `unsupported_capability` errors when a provider can't satisfy an operation. Do not silently fall back to GitHub-only behavior for other providers.
- A provider-verified repository is canonically identified by `(provider, platform_host, provider_repo_id)`; owner/repo is a mutable route. Route-only references still require provider and host, and must not be promoted or combined without a verified stable ID.
- Never identify, route, cache, dedupe, query, persist, or compare repositories, pull requests, merge requests, issues, comments, checks, releases, workspaces, activity, or events by owner/repo/number alone. Every repo-scoped path and data structure must carry provider and host as well as owner and repo.
- Repo-scoped routes use provider-aware paths like `/pulls/{provider}/{owner}/{name}/{number}`, with `/host/{platform_host}/...` for non-default or self-hosted instances.
- GitHub-only optimizations (GraphQL bulk fetch, ETag recovery, detailed diff behavior) stay in `internal/github/` and remain optional around the neutral persistence path.
- Frontend stores and components must thread the full provider ref (`provider`, `platformHost`, `owner`, `name`, `repoPath`) through the shared route helpers in `packages/ui/src/api/provider-routes.ts`. Do not hand-build `/api/v1` URLs or assume GitHub defaults inside components.

For package layout and the new-provider checklist, see `context/provider-architecture.md`. For identity, tokens, freshness, and route shape, see `context/platform-sync-invariants.md`. For GitHub-only sync behavior, see `context/github-sync-invariants.md`.

## Non-Provider Modes

Kata and Docs are first-class kenn-forge modes, but they are not platform providers and do not use provider-neutral repository identity. Do not force them through `internal/platform` or provider capability abstractions.

- Kata mode talks to external Kata daemons. Kenn Forge reads the Kata daemon catalog from `$KATA_HOME/config.toml` (default `~/.kata/config.toml`) and resolves `local = true` daemon entries from Kata runtime records. Kenn Forge config must not become the source of truth for Kata daemon definitions.
- Docs mode operates on explicitly configured local markdown folders. Treat folder reads, writes, deletes, browse, and git publish as local filesystem surfaces requiring explicit path safety, CSRF, and loopback-access decisions.
- These modes may link to each other, but their data ownership remains separate: provider PR/MR data lives in kenn-forge's SQLite DB, Kata task data stays in external Kata daemons, and docs files stay on disk.

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
| API failures or frontend error branching | `context/error-handling.md` |
| Retries, rate limits, scheduling, or single-flight work | `context/retries-and-backoffs.md` |
| Test design, test placement, or test helpers | `context/testing.md` |
| Agent hooks, session bootstrap, or dependency installation | `context/agent-bootstrap.md` |
| User documentation, screenshots, or the Zensical site | `context/docs-authoring.md` |
| Pushing, opening a pull request, or changing PR metadata | `context/pull-request-workflow.md` |
| Frontend visual design or component conventions | `context/ui-design-system.md` |
| Frontend interaction, route state, persistence, or input semantics | `context/ui-interaction-contracts.md` |
| Phone routes, narrow layouts, or touch UX | `context/mobile-ux.md` |
| Workflow or terminal panel interaction models | `context/vscode-workflow-panel-interaction-spec.md` |
| Workspace APIs, creation, or item identity | `context/workspace-apis.md` |
| Workspace deletion, runtime sessions, tmux, or terminal UI | `context/workspace-runtime-lifecycle.md` |
| Inline diff review comments | `context/inline-diff-review-comments-plan.md` |
| Kata daemon integration, task UI, or Kata workspaces | `context/kata-mode.md`, `context/workspace-apis.md` |
| Markdown folders, Docs APIs, or git publishing | `context/docs-mode.md` |

## Testing

```bash
make test       # All Go tests
make test-short # Fast tests only
make lint       # golangci-lint
make vet        # go vet
```

### End-to-End Tests

Coverage of real behavior is non-negotiable; the lane is chosen by the behavior under test, not by a blanket "must have e2e" rule. Avoid the expensive lanes unless they add distinct confidence. Four independent axes:

- **Component or app harness first.** UI-owned behavior such as filtering, sorting, hidden/disabled states, menu contents, route-derived view state, and store/component data flow should usually be covered in Vitest. Use **Vitest + jsdom** (`vp test`) when layout/browser primitives are not material; mount the real `App.svelte` via `frontend/src/test/appHarness.ts` when routing matters.
- **Vitest browser before Playwright for real DOM needs.** Use `*.browser.svelte.ts` / `vitest-browser-svelte` (`vp test --project browser`) when the behavior needs a real browser DOM, native focus/keyboard semantics, localStorage/matchMedia, computed styles, or layout, but does not need an external HTTP server or multi-page Playwright workflow.
- **Playwright/full-stack only for boundaries they uniquely prove.** Reserve Playwright for screenshots/video, `getBoundingClientRect`, scroll/sticky/overflow geometry, container queries, pointer drag, viewport emulation, canvas/xterm, computed CSS pixels, or workflows that must exercise browser navigation against a running app. Use `frontend/tests/e2e-full/` or `internal/server/` Go tests when the behavior depends on backend persistence, sync, capabilities, normalization, wire shape, or middleware. If a real-backend API/server test already proves the runtime path and a component/browser test proves the UI presentation, do not require duplicate full-stack e2e just to click through the same data.
- **Mocking is the exception.** Do not assert backend-computed values through a hand-written fixture. Mock the API (`frontend/src/test/mockApiFetch.ts`, never fork the Playwright copy) only when the behavior is owned by the frontend or the seeded server cannot produce the state.

### Test Guidelines

- Always pass `-shuffle=on` when invoking `go test` directly (e.g. `go test ./internal/db -run TestFoo -shuffle=on`). The `make test` and `make test-short` targets already set it. Shuffled ordering catches hidden test-to-test coupling
- Do not pass `-count=1` to `go test`. `-count=1` is the default and specifying it wastes tokens and disables the build cache unnecessarily. Omit the flag. If a genuine need to bypass cache arises, confirm with the user first
- Only pass `-count=N` when `N > 1` (e.g. `-count=10` for flake hunting)
- Table-driven tests for Go code
- Use `testify` consistently in Go tests; prefer `require` for setup/preconditions and `assert` for non-blocking checks
- When a test function has more than 3 assertions, create a local helper with `assert := assert.New(t)` and use the helper methods for the rest of the checks. Import `github.com/stretchr/testify/assert` without an alias; aliased assert imports are rejected by golangci-lint.
- Do not use `t.Fatal`, `t.Fatalf`, `t.Error`, `t.Errorf`, `t.Fail`, or `t.FailNow` in tests; use testify assertions instead
- Prefer the generated Go API client in `internal/apiclient` for integration-style API tests
- For HTTP tests of user-visible behavior, follow the wire-level discipline in `context/testing.md`: route through `srv.ServeHTTP`, assert on what a client observes, and pick `internal/server/apitest/` or `internal/server/e2etest/` per the rules there.
- Use `openTestDB(t)` helper for database tests
- All tests use `t.TempDir()` for temp directories
- Tests should be fast and isolated
- Shell script tests must exercise observable behavior by running the script
  against controlled inputs and asserting outputs, side effects, or exit
  codes. Do not add bash tests that grep shell scripts, workflows, config
  files, or docs for expected implementation text; those checks are usually
  tautological and should be replaced with real execution, parser/tool-native
  validation, or a documented manual release check.
- Do not run tests with `-v` (especially `go test`) — default output has enough signal to debug failures, and verbose output wastes tokens. Only use `-v` if the user asks for it or a failure genuinely needs the extra detail
- For provider-specific live or container test fixtures used when fake transports can't catch endpoint or auth drift, follow `context/testing.md` and `context/platform-sync-invariants.md`. The GitHub GraphQL gate is `MIDDLEMAN_LIVE_GITHUB_TESTS=1`.

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
- Verify Zensical screenshot asset-path findings against rendered `site/` output; raw HTML source paths can be rewritten when `use_directory_urls` is enabled.
- Zensical resolves `docs_dir`/`site_dir` relative to the config file's directory, so `uvx zensical build` cannot run in place against the checked-in `docs/zensical.toml`; stage a scratch project root containing a copy of the config beside a copy of `docs/`, then build there.
- Tests, docs, fixtures, commit messages, and PR text should use generic synthetic examples unless the user explicitly asks to preserve exact private project names, paths, prose, or domain details.
- **Never use npm** — use `bun install` for frontend dependencies and invoke Vite+ directly via `./node_modules/.bin/vp ...` (or `../node_modules/.bin/vp ...` from `frontend/`). Never run `npm install` or `npm run` — this creates `package-lock.json` which conflicts with the bun lockfile
- Tests should be fast and isolated
- No emojis in code or output
- For database schema changes, follow `context/db-migrations.md`; `internal/db/migrations/` is the source of truth for schema evolution.
- For HTTP API error envelopes and frontend error branching, follow `context/error-handling.md`; branch on stable codes/details rather than prose.
- For retries, backoff, and single-flight dedup against flaky upstreams, follow `context/retries-and-backoffs.md`.
- For frontend UI and TypeScript/Svelte conventions, follow `context/ui-design-system.md`; prefer extending shared UI primitives over adding one-off local badge/chip/button styling, and name reused domain object shapes instead of repeating anonymous inline types.
- For mobile, phone, narrow-viewport, touch, or `/m` route work, follow `context/mobile-ux.md`; mobile UX is a phone-first workflow, not desktop UI resized under mobile routes.
- For Kata task authority, daemon integration, and workspace behavior, follow `context/workspace-apis.md`.
- For Docs mode integration, follow `docs/superpowers/specs/2026-06-08-kata-docs-msgvault-modes-design.md` until dedicated context docs exist; its Messages/msgvault sections are historical (that mode was removed).
- Datetimes are UTC across storage and API boundaries. Store timestamps in UTC, emit API timestamps as UTC RFC3339, and only convert to local time in the Svelte UI presentation layer.

## Roborev

- Never invoke the `roborev review` CLI command in any form unless the user
  explicitly asks for it. Use all other `roborev` CLI commands normally when
  they are appropriate for interacting with roborev. Never invoke a roborev
  skill (including `roborev-fix` or `roborev-design-review-branch`) unless the
  user explicitly asks for that skill.

## Git Workflow

- **Commit every turn** — always commit your work at the end of each turn, no exceptions
- **Capture context before committing** — before every agent-created Git commit, invoke
  the repository-local `context-sync` skill with `--commit`. Apply clear scoped context
  changes before invoking the normal external commit skill. Block only when an unclear
  durable decision requires maintainer input.
- **Never amend commits** — always create new commits for fixes, never use `--amend`
- **Never change branches** — don't create, switch, or delete branches without explicit permission
- **Never bypass pre-commit hooks** — all commits must go through a hook-enforced Git commit path. Do not use `jj` or any other workflow to create, rewrite, or finalize commits in a way that skips the repository's Git hooks
- Use conventional commit messages whose subject explains the reason or user-visible outcome, not just the mechanical change. Good subjects answer "why does this commit exist?" (for example, `fix: restore workspace activity for launched agents`), while vague mechanics such as `fix: run agents under tmux` are not acceptable on their own
- Commit bodies must add any important context about the bug, regression, constraint, or tradeoff that motivated the change; do not rely on the diff to explain intent
- Run tests before committing when applicable
- Before pushing any frontend change, you must have run the full affected suite locally after the final frontend/test edit — the full `vp test` Vitest run, plus the full affected Playwright e2e suite whenever the change touches Playwright specs or the shared mock fixtures they rely on; type checks and CI-only verification are not enough.
- Never push new workstreams unless explicitly asked. When addressing review feedback or CI failures on an existing PR, an agent may push after the fix is implemented and relevant local validation has run.

# Single-Migration PR Enforcement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse the archive schema into migration `000039` and enforce at most one new migration pair per pull request without breaking stacked PRs.

**Architecture:** The migration-history checker keeps staged-only behavior for local hooks and gains an optional immediate-PR-base comparison for CI. Migration identities are numbered basenames, so matching up/down files count once. The archive migration creates its final schema directly and the guide retains only concise policy plus non-automatable judgment.

**Tech Stack:** Go, Git, SQLite migrations, GitHub Actions, Markdown.

## Global Constraints

- Never edit a migration present on `origin/main`, a tag, or a release branch.
- CI compares the current PR against `github.event.pull_request.base.sha` so each stacked PR may own one migration.
- Local hooks do not query GitHub stack metadata.
- Do not add compatibility handling for the discarded `000040`-`000043` intermediate schemas.

---

### Task 1: Enforce one migration identity per PR

**Files:**
- Modify: `tools/migrationhistorycheck/main_test.go`
- Modify: `tools/migrationhistorycheck/main.go`

**Interfaces:**
- Consumes: `MIDDLEMAN_MIGRATION_PR_BASE_REF`, optional Git commit/ref.
- Produces: rejection when a staged change or complete PR delta contains more than one new migration basename.

- [x] Add `TestBlocksMultipleNewMigrations` with two distinct staged migration basenames and assert `run` returns `1` with the one-migration-per-PR error.
- [x] Add `TestBlocksSecondMigrationAcrossPRCommits` that commits the first branch migration, stages the second, sets `MIDDLEMAN_MIGRATION_PR_BASE_REF=main`, and asserts the same rejection.
- [x] Run `go test ./tools/migrationhistorycheck -run 'TestBlocksMultipleNewMigrations|TestBlocksSecondMigrationAcrossPRCommits' -shuffle=on` and confirm both tests fail because the checker currently accepts the migrations.
- [x] Read the optional PR base ref. When set, obtain the candidate diff with `git diff --cached --name-status <pr-base> -- <migration-dir>` and use that ref for base-tree comparisons; otherwise retain the staged-only diff.
- [x] Collect distinct migration basenames that appear in the candidate diff but not in the comparison base. Reject when the sorted set contains more than one name; matching `.up.sql` and `.down.sql` files count once.
- [x] Re-run `go test ./tools/migrationhistorycheck -shuffle=on` and confirm the package passes.

### Task 2: Collapse the archive schema into migration 000039

**Files:**
- Modify: `internal/db/migrations/000039_historical_activity_archive.up.sql`
- Modify: `internal/db/migrations/000039_historical_activity_archive.down.sql`
- Delete: `internal/db/migrations/000040_archive_dataset_pages.{up,down}.sql`
- Delete: `internal/db/migrations/000041_version_archive_dataset_pages.{up,down}.sql`
- Delete: `internal/db/migrations/000042_snapshot_revisions.{up,down}.sql`
- Delete: `internal/db/migrations/000043_bind_archive_dataset_statuses.{up,down}.sql`

**Interfaces:**
- Consumes: schema at migration `000038`.
- Produces: the final archive tables, `snapshot_revision` columns, dataset-page revision binding, and hydration snapshot state at version `000039`.

- [x] Add `snapshot_revision INTEGER NOT NULL DEFAULT 0 CHECK (snapshot_revision >= 0)` to issues and merge requests, then initialize existing rows to `1`.
- [x] Add `hydration_snapshot_updated_at DATETIME` directly to `middleman_archive_items`.
- [x] Create `middleman_archive_dataset_pages` directly with `snapshot_updated_at`, `domain_revision`, and the final composite primary key.
- [x] Update the down migration to drop dataset pages before archive items and remove both snapshot-revision columns.
- [x] Delete migrations `000040` through `000043` with `apply_patch`; do not add tests asserting the files stay deleted.
- [x] Run the migration-focused database tests with `go test ./internal/db -run 'Migration|Archive' -shuffle=on`.

### Task 3: Wire CI enforcement and shrink the guide

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `Makefile`
- Modify: `prek.toml`
- Modify: `context/db-migrations.md`

**Interfaces:**
- Consumes: `github.event.pull_request.base.sha` in pull-request CI.
- Produces: CI guardrail failure for more than one migration identity relative to the immediate PR base.

- [x] Add a `migration-history-check` Make target running `go run ./tools/migrationhistorycheck` and include it in `guardrail-check`.
- [x] Fetch full history in the lint checkout and set `MIDDLEMAN_MIGRATION_PR_BASE_REF` to the PR base SHA for the guardrail step.
- [x] Rename the prek hook to describe immutable-history and single-PR-migration enforcement while keeping staged-only local behavior.
- [x] Compress `context/db-migrations.md`: retain short summaries of immutable shipped migrations and one migration per PR; state what the checker enforces; remove duplicated procedural bullets; retain only history/environment/schema judgment.
- [x] Run `go test ./tools/migrationhistorycheck ./internal/db -shuffle=on`, `make guardrail-check`, and `git diff --check`.
- [x] Commit all implementation changes through repository hooks without `--no-verify`.

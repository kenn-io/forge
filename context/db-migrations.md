# Database Migrations

Use this document before creating, editing, reviewing, or validating database
schema migrations.

- **Shipped migrations are immutable.** A migration has shipped when it exists on `origin/main`, a tag or release branch, or a production database; correct it with a new forward migration.
- **Each PR introduces at most one migration.** Amend a PR-local migration in place instead of stacking fix-ups.

## History Checks

- `go run ./tools/migrationhistorycheck` enforces immutability, unique numbers, and one
  new migration per PR, but cannot see production use or refs it was not given.
- In GitHub's synthetic PR merge checkout it compares against the merge's first parent, so
  target-branch migrations added after the PR opened count as inherited, not new
  (`tools/migrationhistorycheck/main.go::pullRequestComparisonRef`).
- Refresh tags and release branches before deciding a migration is PR-local; when
  uncertain, treat it as immutable.
- Two PRs can each pass and still merge the same migration number, which stops
  golang-migrate from loading any migration.
- Repair a merged duplicate number by renaming the later-merged migration to the next
  number with content unchanged; pure renumbering is the only allowed change to a base
  migration (`tools/migrationhistorycheck/main.go::isPureRenumbering`).

## Rules

- Applying a PR-local migration to a resettable local or preview database does not make it immutable; reset that database after rewriting the migration.
- Do not add compatibility columns, dual-read/write paths, repair gates, or backfills for schema states that have never shipped. Amend the PR-local migration and current code paths directly.
- Keep `.down.sql` honest: if data cleanup is one-way, say so and undo only reversible schema artifacts.
- Validate migrations through `db.Open()` and application-level tests; do not test `golang-migrate` internals.
- Do not add fill, repair, or validation triggers as a shortcut around fixing write paths or rebuilding a SQLite table when a real schema invariant is required.
- Do not add SQL `CHECK` constraints to enforce application invariants;
  express them in Go types and write-path validation (maintainer decision,
  `internal/db/types.go::ActiveRepo`).
- SQLite `CHECK` accepts a `NULL` result; assert `IS NOT NULL` before validating a
  conditionally required nullable value
  (`internal/db/migrations/000048_kata_issue_links.up.sql:16`).
- Kit `sqlcheck` is disabled because existing `CHECK` constraints are intentional
  (`.golangci.overlay.yml`).
- A SQLite parent-table rebuild must drop and recreate dependent triggers, and
  foreign keys must be disabled before golang-migrate starts its transaction
  (`internal/db/migrations.go::runRepositoryCatalogMigration`).
- A recorded migration version and the physical schema must match; there is no
  supported partially upgraded schema state.
- Never use a no-op SQL migration as a version marker for schema work done later in
  Go; new schema artifacts belong in the SQL migration that introduces them.
- Never make migrations tolerate duplicate pre-existing columns with conditional
  `ADD COLUMN` or Go-side repair; an impossible version/schema combination must fail.
- Do not add migration triggers, defaults, or repair hooks to compensate for insert
  paths that omit newly required columns; update every current-schema insert.
- Do not "downgrade" a current test database by editing `schema_migrations` or dropping
  schema artifacts. Historical shapes belong only in migration tests that build them
  directly, never in query, API, or UI tests.
- When changing persisted data, test with real SQLite tables and representative child
  rows, including records lost through foreign keys, uniqueness conflicts, or `INSERT OR IGNORE`.

## Connection Layer

- Package queries go through the `*DB` helpers or the pool-specific `stmtCache`, never
  `d.ro`/`d.rw` directly outside `BeginTx`; direct calls recompile every time
  (`internal/db/stmt_cache.go::stmtCache`).
- Per-connection pragmas live only in `connectionDSN`; idle limits equal open limits
  so compiled statements persist (`internal/db/db.go::openPool`).
- Migrations must never drop or rebuild `sqlite_stat1`; a query-plan assertion needs
  seeded rows plus `DB.Optimize` first (`internal/db/db.go::Optimize`).
- Never expand an unbounded ID list into `IN (?, ...)`: SQLite caps a statement at
  32,766 bound variables; bind a JSON array through `json_each(?)` or batch
  (`internal/db/queries.go::GetWorktreeLinksForMRs`).
- In a `UNION ALL` feeding `*time.Time`, the first arm must use a bare column reference
  there, not `NULL` or an expression; modernc.org/sqlite caches the first arm's decltype
  and a blank one breaks `Scan` for every arm
  (`internal/db/queries_archive_snapshot.go::LoadArchiveSnapshotItems`).

## Federation Spoke Preparation

Migration `000054_federated_spoke_preparation` is the atomic standalone-to-spoke
preparation boundary.

- Backfill launch specifications only when existing rows contain every required fact;
  never synthesize readiness, and keep missing facts discoverable
  (`internal/db/queries_workspace_launch_specs.go::DB.ListUnpreparedProviderWorkspaces`).
- Backfilled launch specifications start with an expired visibility lease, so spoke
  preparation must refresh them from the hub.
- Ad-hoc and Kata workspaces do not require provider launch specifications.
- The down migration removes only migration-54 tables and triggers and never rewrites
  provider, workspace, review, workflow, or notification source rows.
- Review drafts and workflow rows stay on the source spoke as an audit trail after
  handoff; the active spoke runtime stops reading them.

## Migration Review Checklist

- [ ] The migration runs from the previous schema version to the new version.
- [ ] Existing rows are transformed before new constraints or triggers are installed.
- [ ] Foreign-key child rows are moved or merged before parent rows are deleted, unless the product decision deletes the parent and its owned state as one unit.
- [ ] Unique-index conflicts are handled intentionally: true duplicates are deleted, non-duplicate children are preserved.
- [ ] `PRAGMA integrity_check` and `PRAGMA foreign_key_check` are clean on migrated test data.
- [ ] Any real-data validation uses a copy or SQLite backup, never the live database.

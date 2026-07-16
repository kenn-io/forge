# Database Migrations

Migration handling depends on whether the migration has shipped:

1. **Never modify a shipped migration.** A migration is immutable when it exists on `origin/main`, in a tag or release branch, or has been applied to a production database. Correct it with a new forward migration.
2. **Use one migration per PR, amended in place.** A migration that exists only on the current PR branch is rewriteable. If the schema changes during the PR, update that migration instead of stacking fix-up migrations.

Before editing a migration, refresh remote refs and check the boundary:

```bash
git fetch --tags origin
git log --oneline origin/main -- internal/db/migrations/<file>
git log --oneline --tags -- internal/db/migrations/<file>
git branch -r --list 'origin/release/*'
```

If release branches exist, check them explicitly with `git log` as well. Empty results from every applicable history check mean the migration is PR-local unless it has been applied to production or another non-resettable environment. When the boundary cannot be established, treat the migration as immutable and ask before editing it.

The pre-commit hook `go run ./tools/migrationhistorycheck` rejects staged edits to migrations on `origin/main` and duplicate migration numbers. It is a backstop, not the full policy: a passing hook does not permit multiple PR-local migrations or prove that tag, release-branch, production, or shared-environment history is safe. If a checkout uses a different main-branch ref, set `MIDDLEMAN_MIGRATION_BASE_REF` before running the hook.

## Rules

- Before adding a migration, check whether the current PR already has one. If it does, amend its matching `.up.sql` and `.down.sql` files instead of creating another migration number.
- When the PR has no migration yet, create the next sequential `NNNNNN_description.up.sql` and matching `NNNNNN_description.down.sql`.
- Never modify `.up.sql` or `.down.sql` files that have shipped. Historical migrations must keep describing the schema at the time they were introduced.
- Applying a PR-local migration to a resettable local or preview database does not make it immutable. Reset that database to the schema baseline after rewriting the migration so its state matches the revised history.
- If a PR-local migration has reached a shared mutable environment, get explicit approval for the rollback or reseed procedure before rewriting it. If that environment cannot be reset, treat the migration as immutable.
- Do not add compatibility columns, dual-read/write paths, repair gates, or backfills for schema states that have never shipped. Amend the PR-local migration and current code paths directly.
- Keep `.down.sql` honest. If the data cleanup is one-way, say that in the down migration and only undo reversible schema artifacts such as triggers or indexes.
- Validate migrations through `db.Open()` and application-level tests. Do not test `golang-migrate` internals.
- Test a migration against both a fresh database and one at the previous schema version.
- For SQLite, remember that adding constraints to existing columns usually requires a table rebuild. Do not add fill, repair, or validation triggers as a shortcut around fixing current write paths or rebuilding a table when a real schema invariant is required.
- A recorded migration version and the physical schema must match. There is no
  supported "partially upgraded" schema state for new migrations.
- Never use a no-op SQL migration as a version marker for schema work that is
  actually performed later in Go. New schema artifacts belong in the numbered
  SQL migration that introduces them.
- Never make new schema migrations tolerate duplicate pre-existing columns with
  conditional `ADD COLUMN` or Go-side `ensureColumn` repair. If applying the
  migration would hit a duplicate column, the database is already claiming an
  impossible version/schema combination and should fail instead of being
  papered over.
- Do not add migration triggers, defaults, or repair hooks to compensate for
  application or test insert paths that omit newly required columns. Update
  every current-schema insert to write the new column explicitly.
- Do not write tests that "downgrade" or rewind a latest/current test database
  by editing `schema_migrations` or dropping post-target schema artifacts.
  Historical schema or data fixtures are acceptable only in migration/upgrade
  tests that build the older shape directly and then verify the forward
  migration behavior. They are not acceptable in ordinary query, API, or UI
  tests.
- When changing persisted data, test with real SQLite tables and representative child rows. Include dependent records that can be lost through foreign keys, uniqueness conflicts, or `INSERT OR IGNORE`.

## Migration Review Checklist

- [ ] The migration runs from the previous schema version to the new version.
- [ ] Existing rows are transformed before new constraints or triggers are installed.
- [ ] Foreign-key child rows are moved or merged before parent rows are deleted.
- [ ] Unique-index conflicts are handled intentionally: true duplicates are deleted, non-duplicate children are preserved.
- [ ] `PRAGMA integrity_check` and `PRAGMA foreign_key_check` are clean on migrated test data.
- [ ] Any real-data validation uses a copy or SQLite backup, never the live database.

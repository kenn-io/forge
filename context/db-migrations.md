# Database Migrations

`internal/db/migrations/` is append-only history. Once a migration exists on `main`, do not edit it to add new behavior. Add a new numbered migration instead.

The pre-commit hook `go run ./tools/migrationhistorycheck` enforces this by rejecting staged edits to migration files that already exist on `origin/main`. If a checkout uses a different main-branch ref, set `MIDDLEMAN_MIGRATION_BASE_REF` before running the hook.

## Rules

- Create the next sequential `NNNNNN_description.up.sql` and matching `NNNNNN_description.down.sql`.
- Do not modify `.up.sql` or `.down.sql` files that already exist on `main` to account for new requirements. Historical migrations must keep describing the schema at the time they were introduced.
- If a previous migration in the same branch is wrong, prefer a follow-up migration that repairs or completes the state. Only rewrite an existing migration when explicitly instructed by the user.
- Keep `.down.sql` honest. If the data cleanup is one-way, say that in the down migration and only undo reversible schema artifacts such as triggers or indexes.
- Validate migrations through `db.Open()` and application-level tests. Do not test `golang-migrate` internals.
- For SQLite, remember that adding constraints to existing columns usually requires a table rebuild. Prefer a new migration with triggers when the goal is to enforce an invariant without rebuilding tables.
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
- Tests that rewind a latest test database by editing `schema_migrations` must
  first remove every schema artifact introduced after the target version. Do
  not compensate for impossible rewind fixtures in production migration code.
- When changing persisted data, test with real SQLite tables and representative child rows. Include dependent records that can be lost through foreign keys, uniqueness conflicts, or `INSERT OR IGNORE`.

## Migration Review Checklist

- [ ] The migration runs from the previous schema version to the new version.
- [ ] Existing rows are transformed before new constraints or triggers are installed.
- [ ] Foreign-key child rows are moved or merged before parent rows are deleted.
- [ ] Unique-index conflicts are handled intentionally: true duplicates are deleted, non-duplicate children are preserved.
- [ ] `PRAGMA integrity_check` and `PRAGMA foreign_key_check` are clean on migrated test data.
- [ ] Any real-data validation uses a copy or SQLite backup, never the live database.

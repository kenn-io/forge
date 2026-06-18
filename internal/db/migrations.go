package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

const (
	firstLegacySchemaVersion       = 1
	latestLegacySchemaVersion      = 3
	migrationTableName             = "schema_migrations"
	recreateDatabaseInstruction    = "delete the database file and let middleman recreate it"
	timestampRepairGateVersion     = 10
	workspaceSetupMigrationVersion = 11
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func runMigrations(rw *sql.DB) (int, error) {
	sourceDriver, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return migratedb.NilVersion, fmt.Errorf("load embedded migrations: %w", err)
	}

	databaseDriver, err := migratesqlite.WithInstance(rw, &migratesqlite.Config{
		MigrationsTable: migrationTableName,
	})
	if err != nil {
		return migratedb.NilVersion, wrapMigrationError(fmt.Errorf("open migration driver: %w", err))
	}

	version, dirty, err := databaseDriver.Version()
	if err != nil {
		return migratedb.NilVersion, wrapMigrationError(fmt.Errorf("read migration version: %w", err))
	}
	startVersion := version

	latest, err := latestMigrationVersion()
	if err != nil {
		return migratedb.NilVersion, fmt.Errorf("read embedded migration versions: %w", err)
	}

	if version > latest {
		return migratedb.NilVersion, fmt.Errorf(
			"middleman schema version %d is newer than this binary "+
				"(expects %d); upgrade middleman",
			version, latest,
		)
	}

	if dirty {
		return migratedb.NilVersion, wrapMigrationError(fmt.Errorf("database is in a dirty migration state"))
	}

	if version == migratedb.NilVersion {
		legacyVersion, hasLegacyVersion, err := readLegacySchemaVersion(rw)
		if err != nil {
			return migratedb.NilVersion, wrapMigrationError(fmt.Errorf("read legacy schema version: %w", err))
		}

		switch {
		case hasLegacyVersion:
			if !hasMiddlemanTables(rw) {
				return migratedb.NilVersion, wrapMigrationError(
					fmt.Errorf("legacy database schema version metadata exists without middleman tables"),
				)
			}
			if legacyVersion > latestLegacySchemaVersion {
				return migratedb.NilVersion, fmt.Errorf(
					"middleman schema version %d is newer than this binary "+
						"(expects %d); upgrade middleman",
					legacyVersion, latestLegacySchemaVersion,
				)
			}
			if legacyVersion < firstLegacySchemaVersion {
				return migratedb.NilVersion, wrapMigrationError(
					fmt.Errorf("legacy database schema version %d is invalid", legacyVersion),
				)
			}
			if err := databaseDriver.SetVersion(legacyVersion, false); err != nil {
				return migratedb.NilVersion, wrapMigrationError(fmt.Errorf("seed legacy migration version: %w", err))
			}
			startVersion = legacyVersion

		case hasMiddlemanTables(rw):
			return migratedb.NilVersion, wrapMigrationError(
				fmt.Errorf("legacy database is missing schema version metadata"),
			)
		}
	}

	if version == timestampRepairGateVersion {
		_, err := reconcileWorkspaceSetupMigrationVersion10(
			rw, databaseDriver,
		)
		if err != nil {
			return migratedb.NilVersion, wrapMigrationError(
				fmt.Errorf("repair workspace migration state: %w", err),
			)
		}
	}

	m, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", databaseDriver)
	if err != nil {
		return migratedb.NilVersion, wrapMigrationError(fmt.Errorf("create migrator: %w", err))
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return migratedb.NilVersion, wrapMigrationError(fmt.Errorf("apply migrations: %w", err))
	}

	version, dirty, err = databaseDriver.Version()
	if err != nil {
		return migratedb.NilVersion, wrapMigrationError(fmt.Errorf("read migration version after update: %w", err))
	}
	if dirty {
		return migratedb.NilVersion, wrapMigrationError(fmt.Errorf("database is in a dirty migration state"))
	}
	if version != latest {
		return migratedb.NilVersion, wrapMigrationError(
			fmt.Errorf("database ended at migration version %d, expected %d", version, latest),
		)
	}
	if err := reconcileWorkspaceTerminalBackendColumn(rw); err != nil {
		return migratedb.NilVersion, wrapMigrationError(
			fmt.Errorf("repair workspace terminal backend column: %w", err),
		)
	}
	if err := reconcileFleetIntegrationSchema(rw); err != nil {
		return migratedb.NilVersion, wrapMigrationError(
			fmt.Errorf("repair fleet integration schema: %w", err),
		)
	}

	return startVersion, nil
}

func latestMigrationVersion() (int, error) {
	files, err := fs.Glob(migrationFiles, "migrations/*.up.sql")
	if err != nil {
		return 0, err
	}

	latest := migratedb.NilVersion
	for _, file := range files {
		name := path.Base(file)
		prefix := strings.TrimSuffix(name, ".up.sql")
		versionText, _, found := strings.Cut(prefix, "_")
		if !found {
			return 0, fmt.Errorf("parse migration version from %q", name)
		}

		version, err := strconv.Atoi(versionText)
		if err != nil {
			return 0, fmt.Errorf("parse migration version from %q: %w", name, err)
		}
		if version > latest {
			latest = version
		}
	}

	if latest == migratedb.NilVersion {
		return 0, fmt.Errorf("no embedded migrations found")
	}

	return latest, nil
}

func hasMiddlemanTables(db *sql.DB) bool {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master
		 WHERE type = 'table'
		   AND name GLOB 'middleman_*'`,
	).Scan(&count)
	return err == nil && count > 0
}

func hasTable(db *sql.DB, name string) bool {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name,
	).Scan(&count)
	return err == nil && count > 0
}

func hasIndex(db *sql.DB, name string) bool {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`,
		name,
	).Scan(&count)
	return err == nil && count > 0
}

func hasColumn(
	db *sql.DB, tableName, columnName string,
) (bool, error) {
	rows, err := db.Query(
		fmt.Sprintf(`PRAGMA table_info(%s)`, tableName),
	)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(
			&cid, &name, &columnType, &notNull, &defaultVal, &pk,
		); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}

	return false, rows.Err()
}

func reconcileWorkspaceTerminalBackendColumn(rw *sql.DB) error {
	if !hasTable(rw, "middleman_workspaces") {
		return nil
	}
	hasTerminalBackend, err := hasColumn(
		rw, "middleman_workspaces", "terminal_backend",
	)
	if err != nil {
		return err
	}
	if hasTerminalBackend {
		return nil
	}
	_, err = rw.Exec(`
		ALTER TABLE middleman_workspaces
		    ADD COLUMN terminal_backend TEXT NOT NULL DEFAULT ''
	`)
	return err
}

func reconcileFleetIntegrationSchema(rw *sql.DB) error {
	if !hasTable(rw, "middleman_projects") ||
		!hasTable(rw, "middleman_project_worktrees") ||
		!hasTable(rw, "middleman_workspace_runtime_sessions") {
		return nil
	}

	projectColumns := map[string]string{
		"is_stale":        "INTEGER NOT NULL DEFAULT 0",
		"repository_kind": "TEXT NOT NULL DEFAULT 'standard'",
	}
	for column, definition := range projectColumns {
		if err := ensureColumn(rw, "middleman_projects", column, definition); err != nil {
			return err
		}
	}

	worktreeColumns := map[string]string{
		"is_stale":             "INTEGER NOT NULL DEFAULT 0",
		"is_hidden":            "INTEGER NOT NULL DEFAULT 0",
		"session_backend":      "TEXT NOT NULL DEFAULT ''",
		"linked_issue_numbers": "TEXT NOT NULL DEFAULT '[]'",
	}
	for column, definition := range worktreeColumns {
		if err := ensureColumn(rw, "middleman_project_worktrees", column, definition); err != nil {
			return err
		}
	}

	if err := ensureColumn(rw, "middleman_workspace_runtime_sessions", "display_region", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := rw.Exec(`
		UPDATE middleman_workspace_runtime_sessions
		SET display_region = CASE
		    WHEN target_key = 'plain_shell' THEN 'terminal'
		    ELSE 'workflow'
		END
		WHERE display_region = ''
	`); err != nil {
		return err
	}

	if _, err := rw.Exec(`
		CREATE TABLE IF NOT EXISTS middleman_project_worktree_runtime_sessions (
		    worktree_id         TEXT NOT NULL REFERENCES middleman_project_worktrees(id) ON DELETE CASCADE,
		    session_key         TEXT NOT NULL,
		    target_key          TEXT NOT NULL,
		    runtime_backend     TEXT NOT NULL,
		    backend_session_key TEXT,
		    label               TEXT NOT NULL DEFAULT '',
		    created_at          DATETIME NOT NULL DEFAULT (datetime('now')),
		    PRIMARY KEY (worktree_id, session_key),
		    UNIQUE (session_key),
		    UNIQUE (runtime_backend, backend_session_key)
		)
	`); err != nil {
		return err
	}
	if _, err := rw.Exec(`
		CREATE INDEX IF NOT EXISTS middleman_project_worktree_runtime_sessions_worktree_id_idx
		    ON middleman_project_worktree_runtime_sessions(worktree_id)
	`); err != nil {
		return err
	}
	if _, err := rw.Exec(`
		CREATE TABLE IF NOT EXISTS middleman_worktree_stats (
		    path         TEXT PRIMARY KEY,
		    diff_added   INTEGER NOT NULL DEFAULT 0,
		    diff_removed INTEGER NOT NULL DEFAULT 0,
		    sync_ahead   INTEGER NOT NULL DEFAULT 0,
		    sync_behind  INTEGER NOT NULL DEFAULT 0,
		    sampled_at   DATETIME NOT NULL DEFAULT (datetime('now'))
		)
	`); err != nil {
		return err
	}
	if _, err := rw.Exec(`
		CREATE TABLE IF NOT EXISTS middleman_host_runtime_sessions (
		    session_key         TEXT PRIMARY KEY,
		    runtime_backend     TEXT NOT NULL,
		    backend_session_key TEXT,
		    label               TEXT NOT NULL DEFAULT '',
		    cwd                 TEXT NOT NULL DEFAULT '',
		    created_at          DATETIME NOT NULL DEFAULT (datetime('now')),
		    UNIQUE (runtime_backend, backend_session_key)
		)
	`); err != nil {
		return err
	}

	return nil
}

func ensureColumn(rw *sql.DB, tableName, columnName, definition string) error {
	hasExistingColumn, err := hasColumn(rw, tableName, columnName)
	if err != nil {
		return err
	}
	if hasExistingColumn {
		return nil
	}
	_, err = rw.Exec(fmt.Sprintf(
		"ALTER TABLE %s ADD COLUMN %s %s",
		tableName,
		columnName,
		definition,
	))
	return err
}

func reconcileWorkspaceSetupMigrationVersion10(
	rw *sql.DB, driver migratedb.Driver,
) (bool, error) {
	hasEventsTable := hasTable(rw, "middleman_workspace_setup_events")
	hasEventsIndex := hasIndex(
		rw, "middleman_workspace_setup_events_workspace_id_idx",
	)
	hasWorkspaceBranch, err := hasColumn(
		rw, "middleman_workspaces", "workspace_branch",
	)
	if err != nil {
		return false, err
	}
	if !hasEventsTable && !hasEventsIndex && !hasWorkspaceBranch {
		return false, nil
	}
	if err := ensureWorkspaceSetupMigrationArtifacts(
		rw, hasEventsTable, hasEventsIndex, hasWorkspaceBranch,
	); err != nil {
		return false, err
	}

	if err := driver.SetVersion(
		workspaceSetupMigrationVersion, false,
	); err != nil {
		return false, err
	}

	return true, nil
}

func ensureWorkspaceSetupMigrationArtifacts(
	rw *sql.DB,
	hasEventsTable, hasEventsIndex, hasWorkspaceBranch bool,
) error {
	if !hasEventsTable {
		if _, err := rw.Exec(`
			CREATE TABLE IF NOT EXISTS middleman_workspace_setup_events (
			    id          INTEGER PRIMARY KEY AUTOINCREMENT,
			    workspace_id TEXT NOT NULL REFERENCES middleman_workspaces(id) ON DELETE CASCADE,
			    stage       TEXT NOT NULL,
			    outcome     TEXT NOT NULL,
			    message     TEXT NOT NULL,
			    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
			)
		`); err != nil {
			return err
		}
	}

	if !hasEventsIndex {
		if _, err := rw.Exec(`
			CREATE INDEX IF NOT EXISTS middleman_workspace_setup_events_workspace_id_idx
			    ON middleman_workspace_setup_events (workspace_id, id)
		`); err != nil {
			return err
		}
	}

	if !hasWorkspaceBranch {
		if _, err := rw.Exec(`
			ALTER TABLE middleman_workspaces
			    ADD COLUMN workspace_branch TEXT NOT NULL DEFAULT '__middleman_unknown__'
		`); err != nil {
			return err
		}
	}
	return nil
}

func readLegacySchemaVersion(db *sql.DB) (int, bool, error) {
	var version int
	err := db.QueryRow(
		`SELECT version FROM middleman_schema_version LIMIT 1`,
	).Scan(&version)
	if err == nil {
		return version, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if hasTable(db, "middleman_schema_version") {
		return 0, false, err
	}
	return 0, false, nil
}

func wrapMigrationError(err error) error {
	return fmt.Errorf("database migration failed; %s: %w", recreateDatabaseInstruction, err)
}

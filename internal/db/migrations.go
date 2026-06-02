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
	runtimeTmuxMigrationVersion    = 30
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

	if version < runtimeTmuxMigrationVersion && latest >= runtimeTmuxMigrationVersion {
		if version < runtimeTmuxMigrationVersion-1 {
			if err := m.Migrate(uint(runtimeTmuxMigrationVersion - 1)); err != nil &&
				!errors.Is(err, migrate.ErrNoChange) {
				return migratedb.NilVersion, wrapMigrationError(
					fmt.Errorf("apply migrations before runtime tmux migration: %w", err),
				)
			}
		}
		if err := reconcileWorkspaceTmuxSessionsRuntimeMigration(rw); err != nil {
			return migratedb.NilVersion, wrapMigrationError(
				fmt.Errorf("repair workspace tmux session migration state: %w", err),
			)
		}
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

func reconcileWorkspaceTmuxSessionsRuntimeMigration(rw *sql.DB) error {
	hasRuntimeSessions := hasTable(rw, "middleman_workspace_runtime_sessions")
	hasTmuxSessions := hasTable(rw, "middleman_workspace_tmux_sessions")
	if !hasRuntimeSessions && !hasTmuxSessions {
		return nil
	}
	if _, err := rw.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	var rebuildErr error
	if hasRuntimeSessions {
		_, rebuildErr = rw.Exec(`
			DROP TABLE IF EXISTS middleman_workspace_runtime_sessions_runtime_migration;
			CREATE TABLE middleman_workspace_runtime_sessions_runtime_migration (
			    workspace_id  TEXT NOT NULL REFERENCES middleman_workspaces(id) ON DELETE CASCADE,
			    session_key   TEXT NOT NULL,
			    target_key    TEXT NOT NULL,
			    label         TEXT NOT NULL,
			    kind          TEXT NOT NULL,
			    scope         TEXT NOT NULL DEFAULT 'session',
			    tmux_session  TEXT NOT NULL DEFAULT '',
			    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
			    PRIMARY KEY (workspace_id, session_key),
			    UNIQUE (session_key)
			);
			INSERT OR IGNORE INTO middleman_workspace_runtime_sessions_runtime_migration
			    (workspace_id, session_key, target_key, label, kind, scope,
			     tmux_session, created_at)
			SELECT
			    session.workspace_id, session.session_key,
			    session.target_key, session.label, session.kind,
			    session.scope, session.tmux_session, session.created_at
			FROM middleman_workspace_runtime_sessions AS session
			WHERE EXISTS (
			    SELECT 1
			    FROM middleman_workspaces AS workspace
			    WHERE workspace.id = session.workspace_id
			);
			DROP INDEX IF EXISTS middleman_workspace_runtime_sessions_workspace_id_idx;
			DROP TABLE middleman_workspace_runtime_sessions;
			ALTER TABLE middleman_workspace_runtime_sessions_runtime_migration
			    RENAME TO middleman_workspace_runtime_sessions;
			CREATE INDEX middleman_workspace_runtime_sessions_workspace_id_idx
			    ON middleman_workspace_runtime_sessions(workspace_id);
		`)
	}
	if rebuildErr == nil && hasTmuxSessions {
		_, rebuildErr = rw.Exec(`
		DROP TABLE IF EXISTS middleman_workspace_tmux_sessions_runtime_migration;
		CREATE TABLE middleman_workspace_tmux_sessions_runtime_migration (
		    workspace_id TEXT NOT NULL,
		    session_name TEXT NOT NULL,
		    target_key   TEXT NOT NULL,
		    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
		    PRIMARY KEY (workspace_id, session_name),
		    UNIQUE (session_name)
		);
		INSERT OR IGNORE INTO middleman_workspace_tmux_sessions_runtime_migration
		    (workspace_id, session_name, target_key, created_at)
		SELECT workspace_id, session_name, target_key, created_at
		FROM middleman_workspace_tmux_sessions;
		DROP INDEX IF EXISTS middleman_workspace_tmux_sessions_workspace_id_idx;
		DROP TABLE middleman_workspace_tmux_sessions;
		ALTER TABLE middleman_workspace_tmux_sessions_runtime_migration
		    RENAME TO middleman_workspace_tmux_sessions;
		CREATE INDEX middleman_workspace_tmux_sessions_workspace_id_idx
		    ON middleman_workspace_tmux_sessions(workspace_id);
	`)
	}
	_, enableErr := rw.Exec(`PRAGMA foreign_keys = ON`)
	if rebuildErr != nil {
		return rebuildErr
	}
	return enableErr
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

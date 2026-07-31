package config_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/testutil/dbtest"
	_ "modernc.org/sqlite"
)

func TestLegacyDatabaseRelocationAppliesSchemaIdentityMigration(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	legacyPath := filepath.Join(home, ".config", "middleman", "middleman.db")
	require.NoError(os.MkdirAll(filepath.Dir(legacyPath), 0o700))

	database := dbtest.OpenAt(t, legacyPath)
	_, err := database.WriteDB().Exec(`
		INSERT INTO forge_repos (
			id, platform, platform_host, owner, name, repo_path,
			owner_key, name_key, repo_path_key, created_at
		) VALUES (
			1, 'github', 'github.com', 'acme', 'widget', 'acme/widget',
			'acme', 'widget', 'acme/widget', datetime('now')
		)`)
	require.NoError(err)
	require.NoError(database.Close())

	raw, err := sql.Open("sqlite", legacyPath+"?_pragma=foreign_keys(1)")
	require.NoError(err)
	sourceDriver, err := iofs.New(os.DirFS("../db"), "migrations")
	require.NoError(err)
	databaseDriver, err := migratesqlite.WithInstance(raw, &migratesqlite.Config{
		MigrationsTable: "schema_migrations",
	})
	require.NoError(err)
	migrator, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", databaseDriver)
	require.NoError(err)
	require.NoError(migrator.Migrate(43))
	sourceErr, databaseErr := migrator.Close()
	require.NoError(sourceErr)
	require.NoError(databaseErr)

	cfg, err := config.LoadOrCreate(config.DefaultConfigPath())
	require.NoError(err)
	forgePath := filepath.Join(cfg.DataDir, "forge.db")
	database = dbtest.OpenWithMigrationsAt(t, forgePath)

	var owner, name string
	require.NoError(database.ReadDB().QueryRow(
		`SELECT owner, name FROM forge_repos WHERE id = 1`,
	).Scan(&owner, &name))
	assert.Equal("acme", owner)
	assert.Equal("widget", name)
	assert.NoFileExists(legacyPath)
	assert.FileExists(forgePath)
}

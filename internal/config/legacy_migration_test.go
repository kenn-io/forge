package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateCopiesLegacyDefaultConfig(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	require.NoError(os.MkdirAll(oldHome, 0o700))
	legacy := "github_token_env = \"MIDDLEMAN_GITHUB_TOKEN\"\n" +
		"[[repos]]\nowner = \"acme\"\nname = \"widgets\"\n"
	source := filepath.Join(oldHome, "config.toml")
	require.NoError(os.WriteFile(source, []byte(legacy), 0o640))

	cfg, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)

	require.Len(cfg.Repos, 1)
	assert.Equal("acme", cfg.Repos[0].Owner)
	assert.Equal("widgets", cfg.Repos[0].Name)
	assert.Equal("KENN_FORGE_GITHUB_TOKEN", cfg.GitHubTokenEnv)
	assert.FileExists(source)
	assert.FileExists(filepath.Join(DefaultDataDir(), ".legacy-config-migrated"))
	info, err := os.Stat(DefaultConfigPath())
	require.NoError(err)
	assert.Equal(os.FileMode(0o640), info.Mode().Perm())
	dirInfo, err := os.Stat(DefaultDataDir())
	require.NoError(err)
	assert.Equal(os.FileMode(0o700), dirInfo.Mode().Perm())
}

func TestLoadOrCreateLegacyConfigDestinationStates(t *testing.T) {
	tests := []struct {
		name               string
		prepareDestination func(t *testing.T, destination, migrated string)
		wantError          bool
	}{
		{name: "missing destination"},
		{
			name: "generated default",
			prepareDestination: func(t *testing.T, destination, _ string) {
				require.NoError(t, EnsureDefault(destination))
			},
		},
		{
			name: "identical migration without marker",
			prepareDestination: func(t *testing.T, destination, migrated string) {
				require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o700))
				require.NoError(t, os.WriteFile(destination, []byte(migrated), 0o600))
			},
		},
		{
			name: "conflicting destination",
			prepareDestination: func(t *testing.T, destination, _ string) {
				require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o700))
				require.NoError(t, os.WriteFile(destination, []byte("[[repos]]\nowner = \"other\"\nname = \"repo\"\n"), 0o600))
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("KENN_FORGE_HOME", "")
			oldHome := filepath.Join(home, ".config", "middleman")
			newHome := filepath.Join(home, ".kenn", "forge")
			require.NoError(os.MkdirAll(oldHome, 0o700))
			legacy := "[[repos]]\nowner = \"acme\"\nname = \"widgets\"\n"
			source := filepath.Join(oldHome, "config.toml")
			destination := filepath.Join(newHome, "config.toml")
			require.NoError(os.WriteFile(source, []byte(legacy), 0o600))
			if tt.prepareDestination != nil {
				tt.prepareDestination(t, destination, legacy)
			}
			before, beforeErr := os.ReadFile(destination)

			cfg, err := LoadOrCreate(destination)
			if tt.wantError {
				require.Error(err)
				assert.Contains(err.Error(), source)
				assert.Contains(err.Error(), destination)
				after, readErr := os.ReadFile(destination)
				require.NoError(readErr)
				assert.Equal(before, after)
				assert.NoFileExists(filepath.Join(newHome, ".legacy-config-migrated"))
				return
			}

			require.NoError(err)
			require.Len(cfg.Repos, 1)
			assert.Equal("acme", cfg.Repos[0].Owner)
			assert.Equal("widgets", cfg.Repos[0].Name)
			assert.FileExists(filepath.Join(newHome, ".legacy-config-migrated"))
			if beforeErr != nil {
				assert.ErrorIs(beforeErr, os.ErrNotExist)
			}
		})
	}
}

func TestLoadOrCreateLegacyConfigMarkerStopsFutureLegacyReads(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	require.NoError(os.MkdirAll(oldHome, 0o700))
	source := filepath.Join(oldHome, "config.toml")
	require.NoError(os.WriteFile(source, []byte("[[repos]]\nowner = \"acme\"\nname = \"widgets\"\n"), 0o600))
	_, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)
	live, err := os.ReadFile(DefaultConfigPath())
	require.NoError(err)
	live = append(live, []byte("\n# live Kenn Forge edit\n")...)
	require.NoError(os.WriteFile(DefaultConfigPath(), live, 0o600))
	require.NoError(os.WriteFile(source, []byte("[[repos]]\nowner ="), 0o600))

	cfg, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)

	require.Len(cfg.Repos, 1)
	assert.Equal("acme", cfg.Repos[0].Owner)
	after, err := os.ReadFile(DefaultConfigPath())
	require.NoError(err)
	assert.Contains(string(after), "live Kenn Forge edit")
}

func TestLoadOrCreateTransformsOnlyProductOwnedLegacyValues(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	newHome := filepath.Join(home, ".kenn", "forge")
	require.NoError(os.MkdirAll(oldHome, 0o700))
	require.NoError(os.MkdirAll(filepath.Join(oldHome, "tokens"), 0o700))
	require.NoError(os.WriteFile(filepath.Join(oldHome, "tokens", "repo"), []byte("repo-token\n"), 0o600))
	legacy := strings.Join([]string{
		"github_token_env = 'MIDDLEMAN_GITHUB_TOKEN' # keep token comment",
		"data_dir = '" + oldHome + "'",
		"[[repos]]",
		"owner = 'acme'",
		"name = 'widgets'",
		"token_env = \"MIDDLEMAN_GITHUB_TOKEN_ORG_A\"",
		"token_file = '" + filepath.Join(oldHome, "tokens", "repo") + "'",
		"worktree_base_path = '/opt/worktrees'",
		"[[platforms]]",
		"type = 'gitlab'",
		"host = 'gitlab.example'",
		"token_env = 'MIDDLEMAN_GITLAB_TOKEN'",
		"",
	}, "\n")
	require.NoError(os.WriteFile(filepath.Join(oldHome, "config.toml"), []byte(legacy), 0o600))

	_, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)
	body, err := os.ReadFile(DefaultConfigPath())
	require.NoError(err)
	migrated := string(body)

	assert.Contains(migrated, "github_token_env = 'KENN_FORGE_GITHUB_TOKEN' # keep token comment")
	assert.Contains(migrated, "token_env = 'KENN_FORGE_GITLAB_TOKEN'")
	assert.Contains(migrated, "token_env = \"MIDDLEMAN_GITHUB_TOKEN_ORG_A\"")
	assert.Contains(migrated, "data_dir = '"+newHome+"'")
	assert.Contains(migrated, "token_file = '"+filepath.Join(newHome, "tokens", "repo")+"'")
	assert.Contains(migrated, "worktree_base_path = '/opt/worktrees'")
}

func TestLoadOrCreateRejectsInvalidLegacyConfigBeforePublishing(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	newHome := filepath.Join(home, ".kenn", "forge")
	require.NoError(os.MkdirAll(oldHome, 0o700))
	source := filepath.Join(oldHome, "config.toml")
	legacy := []byte("[[repos]]\nowner =")
	require.NoError(os.WriteFile(source, legacy, 0o600))

	_, err := LoadOrCreate(DefaultConfigPath())
	require.Error(err)

	assert.Contains(err.Error(), source)
	assert.Contains(err.Error(), DefaultConfigPath())
	assert.NoFileExists(DefaultConfigPath())
	assert.NoFileExists(filepath.Join(newHome, ".legacy-config-migrated"))
	after, err := os.ReadFile(source)
	require.NoError(err)
	assert.Equal(legacy, after)
}

func TestLoadOrCreateCopiesLegacyCredentialFiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	newHome := filepath.Join(home, ".kenn", "forge")
	oldToken := filepath.Join(oldHome, "tokens", "repo.token")
	oldKey := filepath.Join(oldHome, "github-app.pem")
	require.NoError(os.MkdirAll(filepath.Dir(oldToken), 0o700))
	tokenBody := []byte("repo-token\n")
	keyBody := []byte("private-key\n")
	require.NoError(os.WriteFile(oldToken, tokenBody, 0o640))
	require.NoError(os.WriteFile(oldKey, keyBody, 0o600))
	legacy := strings.Join([]string{
		"[[repos]]",
		"owner = 'acme'",
		"name = 'widgets'",
		"token_file = 'tokens/repo.token'",
		"[[github_apps]]",
		"host = 'github.com'",
		"app_id = 42",
		"private_key_path = '" + oldKey + "'",
		"",
	}, "\n")
	require.NoError(os.WriteFile(filepath.Join(oldHome, "config.toml"), []byte(legacy), 0o600))

	cfg, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)

	require.Len(cfg.Repos, 1)
	require.Len(cfg.GitHubApps, 1)
	newToken := filepath.Join(newHome, "tokens", "repo.token")
	newKey := filepath.Join(newHome, "github-app.pem")
	assert.Equal(newToken, cfg.Repos[0].TokenFile)
	assert.Equal(newKey, cfg.GitHubApps[0].PrivateKeyPath)
	copiedToken, err := os.ReadFile(newToken)
	require.NoError(err)
	copiedKey, err := os.ReadFile(newKey)
	require.NoError(err)
	assert.Equal(tokenBody, copiedToken)
	assert.Equal(keyBody, copiedKey)
	tokenInfo, err := os.Stat(newToken)
	require.NoError(err)
	keyInfo, err := os.Stat(newKey)
	require.NoError(err)
	assert.Equal(os.FileMode(0o640), tokenInfo.Mode().Perm())
	assert.Equal(os.FileMode(0o600), keyInfo.Mode().Perm())
	marker, err := os.ReadFile(filepath.Join(newHome, legacyConfigMigrationMarker))
	require.NoError(err)
	assert.Equal("v2\n", string(marker))
	oldTokenAfter, err := os.ReadFile(oldToken)
	require.NoError(err)
	oldKeyAfter, err := os.ReadFile(oldKey)
	require.NoError(err)
	assert.Equal(tokenBody, oldTokenAfter)
	assert.Equal(keyBody, oldKeyAfter)
}

func TestLoadOrCreateCompletesV1CredentialMigration(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	newHome := filepath.Join(home, ".kenn", "forge")
	oldKey := filepath.Join(oldHome, "github-app.pem")
	newKey := filepath.Join(newHome, "github-app.pem")
	require.NoError(os.MkdirAll(oldHome, 0o700))
	require.NoError(os.MkdirAll(newHome, 0o700))
	keyBody := []byte("private-key\n")
	require.NoError(os.WriteFile(oldKey, keyBody, 0o600))
	legacy := "[[github_apps]]\nhost = 'github.com'\napp_id = 42\nprivate_key_path = '" + oldKey + "'\n"
	require.NoError(os.WriteFile(filepath.Join(oldHome, "config.toml"), []byte(legacy), 0o600))
	migrated, err := transformLegacyConfig([]byte(legacy), oldHome, newHome)
	require.NoError(err)
	require.NoError(os.WriteFile(DefaultConfigPath(), migrated, 0o600))
	markerPath := filepath.Join(newHome, legacyConfigMigrationMarker)
	require.NoError(os.WriteFile(markerPath, []byte("v1\n"), 0o600))

	cfg, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)

	require.Len(cfg.GitHubApps, 1)
	assert.Equal(newKey, cfg.GitHubApps[0].PrivateKeyPath)
	copied, err := os.ReadFile(newKey)
	require.NoError(err)
	assert.Equal(keyBody, copied)
	marker, err := os.ReadFile(markerPath)
	require.NoError(err)
	assert.Equal("v2\n", string(marker))
}

func TestLoadOrCreateRejectsConflictingCredentialFile(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	newHome := filepath.Join(home, ".kenn", "forge")
	oldKey := filepath.Join(oldHome, "github-app.pem")
	newKey := filepath.Join(newHome, "github-app.pem")
	require.NoError(os.MkdirAll(oldHome, 0o700))
	require.NoError(os.MkdirAll(newHome, 0o700))
	require.NoError(os.WriteFile(oldKey, []byte("legacy-key\n"), 0o600))
	legacy := "[[github_apps]]\nhost = 'github.com'\napp_id = 42\nprivate_key_path = '" + oldKey + "'\n"
	require.NoError(os.WriteFile(filepath.Join(oldHome, "config.toml"), []byte(legacy), 0o600))
	migrated, err := transformLegacyConfig([]byte(legacy), oldHome, newHome)
	require.NoError(err)
	require.NoError(os.WriteFile(DefaultConfigPath(), migrated, 0o600))
	markerPath := filepath.Join(newHome, legacyConfigMigrationMarker)
	require.NoError(os.WriteFile(markerPath, []byte("v1\n"), 0o600))
	conflict := []byte("different-key\n")
	require.NoError(os.WriteFile(newKey, conflict, 0o600))

	_, err = LoadOrCreate(DefaultConfigPath())
	require.Error(err)

	assert.Contains(err.Error(), oldKey)
	assert.Contains(err.Error(), newKey)
	after, err := os.ReadFile(newKey)
	require.NoError(err)
	assert.Equal(conflict, after)
	marker, err := os.ReadFile(markerPath)
	require.NoError(err)
	assert.Equal("v1\n", string(marker))
}

func TestLoadOrCreateMovesLegacyDefaultDatabase(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	newHome := filepath.Join(home, ".kenn", "forge")
	require.NoError(os.MkdirAll(oldHome, 0o700))
	require.NoError(os.WriteFile(filepath.Join(oldHome, legacyDatabaseFile), []byte("database"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(oldHome, legacyDatabaseFile+"-wal"), []byte("wal"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(oldHome, legacyDatabaseFile+"-shm"), []byte("shm"), 0o600))

	_, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)

	assert.FileExists(filepath.Join(newHome, forgeDatabaseFile))
	assert.FileExists(filepath.Join(newHome, forgeDatabaseFile+"-wal"))
	assert.FileExists(filepath.Join(newHome, forgeDatabaseFile+"-shm"))
	assert.NoFileExists(filepath.Join(oldHome, legacyDatabaseFile))
	assert.FileExists(filepath.Join(oldHome, legacyLockFile))
}

func TestLoadOrCreateMovesLegacyDatabaseAfterCanonicalConfigExists(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", "")
	oldHome := filepath.Join(home, ".config", "middleman")
	newHome := filepath.Join(home, ".kenn", "forge")
	require.NoError(EnsureDefault(DefaultConfigPath()))
	require.NoError(os.MkdirAll(oldHome, 0o700))
	require.NoError(os.WriteFile(filepath.Join(oldHome, legacyDatabaseFile), []byte("database"), 0o600))

	_, err := LoadOrCreate(DefaultConfigPath())
	require.NoError(err)

	assert.FileExists(filepath.Join(newHome, forgeDatabaseFile))
	assert.NoFileExists(filepath.Join(oldHome, legacyDatabaseFile))
}

func TestLoadOrCreateRenamesDatabaseInConfiguredDataDirectory(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dataDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	require.NoError(os.WriteFile(configPath, []byte("data_dir = \""+dataDir+"\"\n"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(dataDir, legacyDatabaseFile), []byte("database"), 0o600))

	_, err := LoadOrCreate(configPath)
	require.NoError(err)

	assert.FileExists(filepath.Join(dataDir, forgeDatabaseFile))
	assert.NoFileExists(filepath.Join(dataDir, legacyDatabaseFile))
	assert.FileExists(filepath.Join(dataDir, legacyLockFile))
}

func TestLoadOrCreateRefusesDatabaseMoveWhileMiddlemanDaemonIsActive(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dataDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	require.NoError(os.WriteFile(configPath, []byte("data_dir = \""+dataDir+"\"\n"), 0o600))
	require.NoError(os.WriteFile(filepath.Join(dataDir, legacyDatabaseFile), []byte("database"), 0o600))
	legacyLock := flock.New(filepath.Join(dataDir, legacyLockFile))
	require.NoError(legacyLock.Lock())
	t.Cleanup(func() { require.NoError(legacyLock.Unlock()) })

	_, err := LoadOrCreate(configPath)

	require.Error(err)
	assert.Contains(err.Error(), "middleman daemon is still using")
	assert.FileExists(filepath.Join(dataDir, legacyDatabaseFile))
	assert.NoFileExists(filepath.Join(dataDir, forgeDatabaseFile))
}

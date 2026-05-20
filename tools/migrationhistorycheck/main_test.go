package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/procutil"
)

func TestAllowsNewMigration(t *testing.T) {
	isolateGitEnvironment(t)
	repo := initRepoWithMainMigration(t)
	t.Chdir(repo)
	t.Setenv("MIDDLEMAN_MIGRATION_BASE_REF", "main")

	writeFile(t, repo, "internal/db/migrations/000002_next.up.sql", "new\n")
	gitCommand(t, "add", "internal/db/migrations/000002_next.up.sql")

	var stderr bytes.Buffer
	assert.Zero(t, run(&stderr))
	assert.Empty(t, stderr.String())
}

func TestBlocksNewMigrationWhenNumberAlreadyExistsOnMain(t *testing.T) {
	assert := assert.New(t)
	isolateGitEnvironment(t)
	repo := initRepoWithMainMigration(t)
	t.Chdir(repo)
	t.Setenv("MIDDLEMAN_MIGRATION_BASE_REF", "main")

	gitCommand(t, "checkout", "main")
	writeFile(t, repo, "internal/db/migrations/000002_main_name.up.sql", "main up\n")
	writeFile(t, repo, "internal/db/migrations/000002_main_name.down.sql", "main down\n")
	gitCommand(t, "add", "internal/db/migrations/000002_main_name.up.sql", "internal/db/migrations/000002_main_name.down.sql")
	gitCommand(t, "commit", "-qm", "add main migration")
	gitCommand(t, "checkout", "feature")
	writeFile(t, repo, "internal/db/migrations/000002_branch_name.up.sql", "new up\n")
	writeFile(t, repo, "internal/db/migrations/000002_branch_name.down.sql", "new down\n")
	gitCommand(t, "add", "internal/db/migrations/000002_branch_name.up.sql", "internal/db/migrations/000002_branch_name.down.sql")

	var stderr bytes.Buffer
	assert.Equal(1, run(&stderr))
	assert.Contains(stderr.String(), "duplicate migration number")
	assert.Contains(stderr.String(), "000002")
	assert.Contains(stderr.String(), "000002_branch_name")
	assert.Contains(stderr.String(), "000002_main_name")
}

func TestBlocksMainBranchMigrationEdit(t *testing.T) {
	isolateGitEnvironment(t)
	repo := initRepoWithMainMigration(t)
	t.Chdir(repo)
	t.Setenv("MIDDLEMAN_MIGRATION_BASE_REF", "main")

	writeFile(t, repo, "internal/db/migrations/000001_init.up.sql", "changed\n")
	gitCommand(t, "add", "internal/db/migrations/000001_init.up.sql")

	var stderr bytes.Buffer
	assert.Equal(t, 1, run(&stderr))
	assert.Contains(t, stderr.String(), "Refusing to commit staged migration history changes")
	assert.Contains(t, stderr.String(), "internal/db/migrations/000001_init.up.sql")
}

func TestBlocksMainBranchMigrationRename(t *testing.T) {
	isolateGitEnvironment(t)
	repo := initRepoWithMainMigration(t)
	t.Chdir(repo)
	t.Setenv("MIDDLEMAN_MIGRATION_BASE_REF", "main")

	gitCommand(t, "mv", "internal/db/migrations/000001_init.up.sql", "internal/db/migrations/000001_renamed.up.sql")

	var stderr bytes.Buffer
	assert.Equal(t, 1, run(&stderr))
	assert.Contains(t, stderr.String(), "internal/db/migrations/000001_init.up.sql")
}

func initRepoWithMainMigration(t *testing.T) string {
	t.Helper()

	repo := t.TempDir()
	migrationPath := filepath.Join(repo, "internal/db/migrations/000001_init.up.sql")
	require.NoError(t, os.MkdirAll(filepath.Dir(migrationPath), 0o755))
	require.NoError(t, os.WriteFile(migrationPath, []byte("old\n"), 0o644))

	gitCommandIn(t, repo, "init", "-q", "-b", "main")
	gitCommandIn(t, repo, "config", "user.email", "test@example.com")
	gitCommandIn(t, repo, "config", "user.name", "Test")
	gitCommandIn(t, repo, "add", ".")
	gitCommandIn(t, repo, "commit", "-qm", "init")
	gitCommandIn(t, repo, "checkout", "-qb", "feature")

	return repo
}

func writeFile(t *testing.T, repo, path, content string) {
	t.Helper()

	fullPath := filepath.Join(repo, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
	require.NoError(t, os.WriteFile(fullPath, []byte(content), 0o644))
}

func gitCommand(t *testing.T, args ...string) {
	t.Helper()
	gitCommandIn(t, "", args...)
}

func gitCommandIn(t *testing.T, dir string, args ...string) {
	t.Helper()

	gitArgs := append([]string{"-c", "core.hooksPath=/dev/null"}, args...)
	cmd := procutil.Command("git", gitArgs...)
	cmd.Dir = dir
	cmd.Env = cleanGitEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}

func isolateGitEnvironment(t *testing.T) {
	t.Helper()

	originalGitEnv := gitEnv
	gitEnv = cleanGitEnv(os.Environ())
	t.Cleanup(func() {
		gitEnv = originalGitEnv
	})
}

func cleanGitEnv(env []string) []string {
	cleaned := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key == "GIT_DIR" || key == "GIT_WORK_TREE" || strings.HasPrefix(key, "GIT_") {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}

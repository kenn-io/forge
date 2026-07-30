package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRewriteAppliesCanonicalMappings(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		body     string
		wantPath string
		wantBody string
	}{
		{
			name:     "go module and command path",
			path:     "cmd/middleman/main.go",
			body:     "import \"go.kenn.io/middleman/internal/config\"\n",
			wantPath: "cmd/kenn-forge/main.go",
			wantBody: "import \"go.kenn.io/forge/internal/config\"\n",
		},
		{
			name:     "environment and product prose",
			path:     "README.md",
			body:     "Middleman reads MIDDLEMAN_GITHUB_TOKEN.\n",
			wantPath: "README.md",
			wantBody: "Kenn Forge reads KENN_FORGE_GITHUB_TOKEN.\n",
		},
		{
			name:     "workspace package",
			path:     "frontend/src/main.ts",
			body:     "import { client } from \"@middleman/ui\";\n",
			wantPath: "frontend/src/main.ts",
			wantBody: "import { client } from \"@kenn-forge/ui\";\n",
		},
		{
			name:     "current schema query",
			path:     "internal/db/queries.go",
			body:     "SELECT id FROM middleman_repos\n",
			wantPath: "internal/db/queries.go",
			wantBody: "SELECT id FROM forge_repos\n",
		},
		{
			name: "go identifiers and repository URL",
			path: "internal/config/url_test.go",
			body: "package config\n\nfunc use() {\n" +
				"\tmiddleman := struct{ URL string }{}\n" +
				"\t_ = middleman.URL\n" +
				"\tvar _ XMiddlemanKataDaemon\n" +
				"\tvar _ XForgeRuntimeSessionKey\n" +
				"\t_ = \"https://github.com/wesm/middleman.git\"\n" +
				"}\n",
			wantPath: "internal/config/url_test.go",
			wantBody: "package config\n\nfunc use() {\n" +
				"\tforge := struct{ URL string }{}\n" +
				"\t_ = forge.URL\n" +
				"\tvar _ XKennForgeKataDaemon\n" +
				"\tvar _ XKennForgeRuntimeSessionKey\n" +
				"\t_ = \"https://github.com/wesm/kenn-forge.git\"\n" +
				"}\n",
		},
		{
			name:     "http header token",
			path:     "internal/server/headers.go",
			body:     "const header = \"X-Middleman-Fleet-Host\"\n",
			wantPath: "internal/server/headers.go",
			wantBody: "const header = \"X-Kenn-Forge-Fleet-Host\"\n",
		},
		{
			name:     "repository URL damaged by earlier alias mapping",
			path:     "internal/config/config_test.go",
			body:     "name = \"https://github.com/wesm/forge.git\"\n",
			wantPath: "internal/config/config_test.go",
			wantBody: "name = \"https://github.com/wesm/kenn-forge.git\"\n",
		},
		{
			name:     "typescript identifiers preserve storage keys",
			path:     "frontend/src/server.ts",
			body:     "let middleman = \"middleman:state\";\nlet kenn-forge = \"kenn-forge:state\";\n",
			wantPath: "frontend/src/server.ts",
			wantBody: "let forge = \"kenn-forge:state\";\nlet forge = \"kenn-forge:state\";\n",
		},
		{
			name:     "derived socket hash fixture",
			path:     "rust/pty-manager/src/main.rs",
			body:     "sock-cb190ac507b2b0a4\n",
			wantPath: "rust/pty-manager/src/main.rs",
			wantBody: "sock-5e73279a62064378\n",
		},
		{
			name:     "repository prefix glob fixture",
			path:     "internal/server/kataapi/workspace_test.go",
			body:     "name = \"middle*\"\n",
			wantPath: "internal/server/kataapi/workspace_test.go",
			wantBody: "name = \"kenn-*\"\n",
		},
		{
			name:     "runtime filenames damaged by earlier alias mapping",
			path:     "internal/runtimelock/paths.go",
			body:     "const names = \"forge.lock forge.run.json .forge.run.json.tmp\"\n",
			wantPath: "internal/runtimelock/paths.go",
			wantBody: "const names = \"kenn-forge.lock kenn-forge.run.json .kenn-forge.run.json.tmp\"\n",
		},
		{
			name:     "canonical home",
			path:     "README.md",
			body:     "~/.config/kenn-forge/config.toml\n",
			wantPath: "README.md",
			wantBody: "~/.kenn/forge/config.toml\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, tt.path, tt.body, 0o644)

			report, err := Rewrite(root, []string{tt.path}, false)
			require.NoError(t, err)

			assert := assert.New(t)
			assert.Equal(1, report.Changed)
			if tt.path == tt.wantPath {
				assert.Zero(report.Moved)
			} else {
				assert.Equal(1, report.Moved)
			}
			body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(tt.wantPath)))
			require.NoError(t, err)
			assert.Equal(tt.wantBody, string(body))
		})
	}
}

func TestRewriteIsIdempotent(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	root := t.TempDir()
	const oldPath = "cmd/middleman/main.go"
	const newPath = "cmd/kenn-forge/main.go"
	writeFixture(t, root, oldPath, "package main // Middleman\n", 0o644)

	_, err := Rewrite(root, []string{oldPath}, false)
	require.NoError(err)
	report, err := Rewrite(root, []string{newPath}, false)
	require.NoError(err)

	assert.Zero(report.Changed)
	assert.Zero(report.Moved)
}

func TestRewriteRecognizesAlreadyMovedTrackedPath(t *testing.T) {
	root := t.TempDir()
	const oldPath = "cmd/middleman/main.go"
	const newPath = "cmd/kenn-forge/main.go"
	writeFixture(t, root, newPath, "package main\n", 0o644)

	report, err := Rewrite(root, []string{oldPath}, false)

	require.NoError(t, err)
	assert.Zero(t, report.Changed)
	assert.Zero(t, report.Moved)
}

func TestRewriteRejectsPathCollision(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "cmd/middleman/main.go", "old\n", 0o644)
	writeFixture(t, root, "cmd/kenn-forge/main.go", "new\n", 0o644)

	_, err := Rewrite(root, []string{"cmd/middleman/main.go", "cmd/kenn-forge/main.go"}, false)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPathCollision)
}

func TestRewritePreservesExecutableModeAndSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	require := require.New(t)
	assert := assert.New(t)
	root := t.TempDir()
	writeFixture(t, root, "skills/middleman-helper/SKILL.md", "Middleman helper\n", 0o755)
	require.NoError(os.MkdirAll(filepath.Join(root, ".agents", "skills"), 0o755))
	require.NoError(os.Symlink("../../skills/middleman-helper", filepath.Join(root, ".agents", "skills", "middleman-helper")))

	_, err := Rewrite(root, []string{"skills/middleman-helper/SKILL.md", ".agents/skills/middleman-helper"}, false)
	require.NoError(err)

	info, err := os.Stat(filepath.Join(root, "skills", "kenn-forge-helper", "SKILL.md"))
	require.NoError(err)
	assert.NotZero(info.Mode().Perm() & 0o111)
	target, err := os.Readlink(filepath.Join(root, ".agents", "skills", "kenn-forge-helper"))
	require.NoError(err)
	assert.Equal("../../skills/kenn-forge-helper", target)
}

func TestRewriteSkipsBinaryAndHistoricalArtifacts(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "asset.bin", "middleman\x00payload", 0o644)
	const historical = "docs/superpowers/specs/2026-07-01-middleman-mcp-server-design.md"
	writeFixture(t, root, historical, "Middleman historical record\n", 0o644)

	report, err := Rewrite(root, []string{"asset.bin", historical}, false)
	require.NoError(t, err)

	assert := assert.New(t)
	assert.Zero(report.Changed)
	assert.Equal(1, report.SkippedBinary)
	assert.Equal(1, report.Allowlisted)
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(historical)))
	require.NoError(t, err)
	assert.Equal("Middleman historical record\n", string(body))
}

func TestRewriteMovesBinaryPathWithoutEditingContent(t *testing.T) {
	root := t.TempDir()
	const oldPath = "assets/middleman-logo.bin"
	const newPath = "assets/kenn-forge-logo.bin"
	const payload = "middleman\x00payload"
	writeFixture(t, root, oldPath, payload, 0o644)

	report, err := Rewrite(root, []string{oldPath}, false)
	require.NoError(t, err)

	assert := assert.New(t)
	assert.Equal(1, report.Changed)
	assert.Equal(1, report.Moved)
	assert.Equal(1, report.SkippedBinary)
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(newPath)))
	require.NoError(t, err)
	assert.Equal(payload, string(body))
}

func TestRewriteUpdatesItsOwnModuleImportWithoutChangingLegacyMappings(t *testing.T) {
	root := t.TempDir()
	const toolPath = "tools/renameforge/main.go"
	legacyModule := "go.kenn.io/" + "middleman"
	body := "package main\n\n" +
		"import \"" + legacyModule + "/internal/procutil\"\n\n" +
		"var rule = Rule{Old: \"" + legacyModule + "\", New: \"go.kenn.io/forge\"}\n"
	writeFixture(t, root, toolPath, body, 0o644)

	report, err := Rewrite(root, []string{toolPath}, false)
	require.NoError(t, err)

	bodyBytes, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(toolPath)))
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Equal(1, report.Changed)
	assert.Contains(string(bodyBytes), `import "go.kenn.io/forge/internal/procutil"`)
	assert.Contains(string(bodyBytes), `Old: "`+legacyModule+`"`)
}

func TestRewriteCheckOnlyReportsRequiredChanges(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	root := t.TempDir()
	writeFixture(t, root, "README.md", "Middleman\n", 0o644)

	report, err := Rewrite(root, []string{"README.md"}, true)

	assert.Equal(1, report.Changed)
	require.ErrorIs(err, ErrChangesRequired)
	body, readErr := os.ReadFile(filepath.Join(root, "README.md"))
	require.NoError(readErr)
	assert.Equal("Middleman\n", string(body))
}

func TestRenderSchemaRenameIsReversible(t *testing.T) {
	objects := []SchemaObject{
		{
			Type: "table",
			Name: "middleman_repos",
			SQL:  "CREATE TABLE middleman_repos (id INTEGER PRIMARY KEY)",
		},
		{
			Type:  "trigger",
			Name:  "middleman_repos_casefold_insert",
			Table: "middleman_repos",
			SQL:   "CREATE TRIGGER middleman_repos_casefold_insert BEFORE INSERT ON middleman_repos BEGIN SELECT 1; END",
		},
	}

	up, down, err := RenderSchemaRename(objects)
	require.NoError(t, err)

	assert := assert.New(t)
	assert.Contains(string(up), "DROP TRIGGER middleman_repos_casefold_insert;")
	assert.Contains(string(up), "ALTER TABLE middleman_repos RENAME TO forge_repos;")
	assert.Contains(string(up), "CREATE TRIGGER forge_repos_casefold_insert BEFORE INSERT ON forge_repos")
	assert.Contains(string(down), "DROP TRIGGER forge_repos_casefold_insert;")
	assert.Contains(string(down), "ALTER TABLE forge_repos RENAME TO middleman_repos;")
	assert.Contains(string(down), "CREATE TRIGGER middleman_repos_casefold_insert BEFORE INSERT ON middleman_repos")
}

func TestRenderSchemaRenameRejectsUnsupportedObject(t *testing.T) {
	_, _, err := RenderSchemaRename([]SchemaObject{{Type: "view", Name: "middleman_view", SQL: "CREATE VIEW middleman_view AS SELECT 1"}})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedSchemaObject)
}

func TestValidateModesRejectsCheckAndWriteTogether(t *testing.T) {
	err := validateModes(true, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined")
}

func TestVerifyMigrationPairDetectsDrift(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "000044_rename_schema_to_forge.up.sql", "stale up\n", 0o644)
	writeFixture(t, dir, "000044_rename_schema_to_forge.down.sql", "fresh down\n", 0o644)

	err := verifyMigrationPair(dir, []byte("fresh up\n"), []byte("fresh down\n"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "000044_rename_schema_to_forge.up.sql")
}

func TestRenderSchemaRenameMigratesWorkspaceSentinels(t *testing.T) {
	objects := []SchemaObject{{
		Type: "table",
		Name: "middleman_workspaces",
		SQL:  "CREATE TABLE middleman_workspaces (workspace_branch TEXT NOT NULL DEFAULT '__middleman_unknown__')",
	}}

	up, down, err := RenderSchemaRename(objects)
	require.NoError(t, err)

	assert := assert.New(t)
	assert.Contains(string(up), "__middleman_unknown__")
	assert.Contains(string(up), "__kenn_forge_unknown__")
	assert.Contains(string(up), "__middleman_recovery_pending__..state")
	assert.Contains(string(up), "__kenn_forge_recovery_pending__..state")
	assert.Contains(string(down), "__middleman_unknown__")
	assert.Contains(string(down), "__middleman_recovery_pending__..state")
}

func writeFixture(t *testing.T, root, relativePath, body string, mode os.FileMode) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(relativePath))
	require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
	require.NoError(t, os.WriteFile(fullPath, []byte(body), mode))
}

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
)

func writeDocsCLIConfig(t *testing.T, dir string, folders []config.DocFolder) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	cfg := &config.Config{
		SyncInterval:   "5m",
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Host:           "127.0.0.1",
		Port:           8091,
		DocFolders:     folders,
		Activity: config.Activity{
			ViewMode:  "threaded",
			TimeRange: "7d",
		},
		Terminal: config.Terminal{Renderer: "xterm"},
	}
	require.NoError(t, cfg.Save(path))
	return path
}

func TestDocsCLIListFoldersEmpty(t *testing.T) {
	cfgPath := writeDocsCLIConfig(t, t.TempDir(), nil)
	var buf bytes.Buffer

	err := runDocsListFolders([]string{"-config", cfgPath}, &buf)

	require.NoError(t, err)
	assert := assert.New(t)
	assert.Contains(buf.String(), "config: "+cfgPath)
	assert.Contains(buf.String(), "(no folders configured)")
}

func TestDocsCLIListFoldersIncludesDaemonBinding(t *testing.T) {
	dir := t.TempDir()
	folderDir := filepath.Join(dir, "notes")
	require.NoError(t, os.Mkdir(folderDir, 0o755))
	cfgPath := writeDocsCLIConfig(t, dir, []config.DocFolder{
		{ID: "notes", Name: "Notes", Path: folderDir, Daemon: "work"},
	})
	var buf bytes.Buffer

	err := runDocsListFolders([]string{"-config", cfgPath}, &buf)

	require.NoError(t, err)
	assert := assert.New(t)
	assert.Contains(buf.String(), "notes")
	assert.Contains(buf.String(), "Notes")
	assert.Contains(buf.String(), "work")
	assert.Contains(buf.String(), folderDir)
}

func TestDocsCLIAddFolderCreatesEntry(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	folderDir := filepath.Join(dir, "research")
	require.NoError(os.Mkdir(folderDir, 0o755))
	cfgPath := writeDocsCLIConfig(t, dir, nil)
	var buf bytes.Buffer

	err := runDocsAddFolder([]string{"-config", cfgPath, folderDir}, &buf)

	require.NoError(err)
	cfg, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg.DocFolders, 1)
	got := cfg.DocFolders[0]
	assert := assert.New(t)
	assert.Equal("research", got.ID)
	assert.Equal("research", got.Name)
	assert.True(strings.HasSuffix(got.Path, "research"))
	assert.Contains(buf.String(), "config saved to "+cfgPath)
}

func TestDocsCLIAddFolderBootstrapsExplicitMissingConfig(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	folderDir := filepath.Join(dir, "research")
	require.NoError(os.Mkdir(folderDir, 0o755))
	cfgPath := filepath.Join(dir, "fresh.toml")
	require.NoFileExists(cfgPath)
	var buf bytes.Buffer

	err := runDocsAddFolder([]string{"-config", cfgPath, folderDir}, &buf)

	require.NoError(err)
	cfg, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg.DocFolders, 1)
	assert := assert.New(t)
	assert.Equal("research", cfg.DocFolders[0].ID)
	assert.Contains(buf.String(), cfgPath)
}

func TestDocsCLIAddFolderCustomIDNameAndDaemon(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	folderDir := filepath.Join(dir, "tmp")
	require.NoError(os.Mkdir(folderDir, 0o755))
	cfgPath := writeDocsCLIConfig(t, dir, nil)
	var buf bytes.Buffer

	err := runDocsAddFolder([]string{
		"-config", cfgPath,
		"-id", "my-folder",
		"-name", "My Folder",
		"-daemon", "work",
		folderDir,
	}, &buf)

	require.NoError(err)
	cfg, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg.DocFolders, 1)
	assert := assert.New(t)
	assert.Equal("my-folder", cfg.DocFolders[0].ID)
	assert.Equal("My Folder", cfg.DocFolders[0].Name)
	assert.Equal("work", cfg.DocFolders[0].Daemon)
}

func TestDocsCLIAddFolderRejectsDuplicateID(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	folderDir := filepath.Join(dir, "notes")
	other := filepath.Join(dir, "other")
	require.NoError(os.Mkdir(folderDir, 0o755))
	require.NoError(os.Mkdir(other, 0o755))
	cfgPath := writeDocsCLIConfig(t, dir, []config.DocFolder{{ID: "notes", Path: folderDir}})
	var buf bytes.Buffer

	err := runDocsAddFolder([]string{"-config", cfgPath, "-id", "notes", other}, &buf)

	require.Error(err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestDocsCLIAddFolderRejectsMissingPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeDocsCLIConfig(t, dir, nil)
	var buf bytes.Buffer

	err := runDocsAddFolder([]string{"-config", cfgPath, filepath.Join(dir, "does-not-exist")}, &buf)

	require.Error(t, err)
}

func TestDocsCLIAddFolderRejectsFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeDocsCLIConfig(t, dir, nil)
	filePath := filepath.Join(dir, "note.md")
	require.NoError(t, os.WriteFile(filePath, []byte("hello"), 0o644))
	var buf bytes.Buffer

	err := runDocsAddFolder([]string{"-config", cfgPath, filePath}, &buf)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestDocsCLIAddFolderAutoNumbersDuplicateBasename(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	first := filepath.Join(dir, "notes")
	second := filepath.Join(dir, "other", "notes")
	require.NoError(os.MkdirAll(first, 0o755))
	require.NoError(os.MkdirAll(second, 0o755))
	cfgPath := writeDocsCLIConfig(t, dir, []config.DocFolder{{ID: "notes", Path: first}})
	var buf bytes.Buffer

	err := runDocsAddFolder([]string{"-config", cfgPath, second}, &buf)

	require.NoError(err)
	cfg, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg.DocFolders, 2)
	assert.Equal(t, "notes-2", cfg.DocFolders[1].ID)
}

func TestDocsCLIRemoveFolderDeletesEntry(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	require.NoError(os.Mkdir(a, 0o755))
	require.NoError(os.Mkdir(b, 0o755))
	cfgPath := writeDocsCLIConfig(t, dir, []config.DocFolder{
		{ID: "a", Path: a},
		{ID: "b", Path: b},
	})
	var buf bytes.Buffer

	err := runDocsRemoveFolder([]string{"-config", cfgPath, "a"}, &buf)

	require.NoError(err)
	cfg, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfg.DocFolders, 1)
	assert := assert.New(t)
	assert.Equal("b", cfg.DocFolders[0].ID)
	assert.Contains(buf.String(), "removed folder \"a\"")
}

func TestDocsCLIRemoveFolderUnknownErrors(t *testing.T) {
	cfgPath := writeDocsCLIConfig(t, t.TempDir(), nil)
	var buf bytes.Buffer

	err := runDocsRemoveFolder([]string{"-config", cfgPath, "missing"}, &buf)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestDocsCLIRemoveFolderWithoutConfigErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.toml")
	var buf bytes.Buffer

	err := runDocsRemoveFolder([]string{"-config", missing, "anything"}, &buf)

	require.Error(t, err)
}

func TestRunCLIDocsCommandsManageFolders(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	folderDir := filepath.Join(dir, "research")
	require.NoError(os.Mkdir(folderDir, 0o755))
	cfgPath := writeDocsCLIConfig(t, dir, nil)

	var add bytes.Buffer
	err := runCLI([]string{"docs", "add-folder", "-config", cfgPath, "-id", "research", "-daemon", "work", folderDir}, &add)
	require.NoError(err)
	assert.Contains(t, add.String(), "added folder \"research\"")

	var list bytes.Buffer
	err = runCLI([]string{"docs", "list-folders", "-config", cfgPath}, &list)
	require.NoError(err)
	assert := assert.New(t)
	assert.Contains(list.String(), "research")
	assert.Contains(list.String(), "work")
	assert.Contains(list.String(), folderDir)

	var remove bytes.Buffer
	err = runCLI([]string{"docs", "remove-folder", "-config", cfgPath, "research"}, &remove)
	require.NoError(err)
	assert.Contains(remove.String(), "removed folder \"research\"")

	cfg, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Empty(cfg.DocFolders)
}

func TestRunDocsCLIRejectsUnknownSubcommand(t *testing.T) {
	err := runDocsCLI([]string{"bogus"}, io.Discard)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown docs subcommand")
}

func TestRunDocsCLIRejectsMissingSubcommand(t *testing.T) {
	err := runDocsCLI(nil, io.Discard)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing docs subcommand")
}

func TestDocsCLIExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	cases := []struct {
		in   string
		want string
	}{
		{"~", home},
		{"~/notes", filepath.Join(home, "notes")},
		{"/abs/path", "/abs/path"},
		{"relative", "relative"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := expandDocsHome(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

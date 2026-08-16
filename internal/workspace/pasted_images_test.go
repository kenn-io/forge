package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestStorePastedImageDetectsSupportedFormats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []byte
		ext  string
	}{
		{name: "png", data: []byte("\x89PNG\r\n\x1a\nfixture"), ext: "png"},
		{name: "jpeg", data: []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01fixture"), ext: "jpg"},
		{name: "gif", data: []byte("GIF89a\x01\x00\x01\x00fixture"), ext: "gif"},
		{name: "webp", data: []byte("RIFF\x10\x00\x00\x00WEBPVP8 fixture"), ext: "webp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			require := require.New(t)
			manager, workspaceRow := setupPastedImageWorkspace(t, "ready")

			relPath, err := manager.StorePastedImage(t.Context(), workspaceRow.ID, tt.data)
			require.NoError(err)
			assert.Regexp(`^\.kenn-forge/pasted-images/paste-[0-9a-f]{32}\.`+tt.ext+`$`, relPath)

			absolutePath := filepath.Join(workspaceRow.WorktreePath, filepath.FromSlash(relPath))
			stored, err := os.ReadFile(absolutePath)
			require.NoError(err)
			assert.Equal(tt.data, stored)
			info, err := os.Stat(absolutePath)
			require.NoError(err)
			assert.Equal(fs.FileMode(0o600), info.Mode().Perm())
		})
	}
}

func TestStorePastedImageRejectsInvalidInputAndWorkspaceState(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	readyManager, readyWorkspace := setupPastedImageWorkspace(t, "ready")
	startingManager, startingWorkspace := setupPastedImageWorkspace(t, "starting")

	_, err := readyManager.StorePastedImage(
		t.Context(), readyWorkspace.ID, make([]byte, MaxPastedImageBytes+1),
	)
	require.ErrorIs(err, ErrPastedImageTooLarge)

	_, err = readyManager.StorePastedImage(t.Context(), readyWorkspace.ID, []byte("not an image"))
	require.ErrorIs(err, ErrUnsupportedPastedImage)

	_, err = readyManager.StorePastedImage(t.Context(), "missing", pastedImagePNGFixture())
	require.ErrorIs(err, ErrWorkspaceNotFound)

	_, err = startingManager.StorePastedImage(t.Context(), startingWorkspace.ID, pastedImagePNGFixture())
	require.ErrorIs(err, ErrWorkspaceInvalidState)
}

func TestStorePastedImageRejectsConflictingStoragePaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(*testing.T, string, string)
	}{
		{
			name: "root symlink",
			setup: func(t *testing.T, worktree, outside string) {
				require := require.New(t)
				require.NoError(os.Symlink(outside, filepath.Join(worktree, ".kenn-forge")))
			},
		},
		{
			name: "root regular file",
			setup: func(t *testing.T, worktree, _ string) {
				require := require.New(t)
				require.NoError(os.WriteFile(filepath.Join(worktree, ".kenn-forge"), []byte("conflict"), 0o600))
			},
		},
		{
			name: "image directory symlink",
			setup: func(t *testing.T, worktree, outside string) {
				require := require.New(t)
				require.NoError(os.Mkdir(filepath.Join(worktree, ".kenn-forge"), 0o755))
				require.NoError(os.Symlink(outside, filepath.Join(worktree, PastedImageDirectory)))
			},
		},
		{
			name: "image directory regular file",
			setup: func(t *testing.T, worktree, _ string) {
				require := require.New(t)
				require.NoError(os.Mkdir(filepath.Join(worktree, ".kenn-forge"), 0o755))
				require.NoError(os.WriteFile(filepath.Join(worktree, PastedImageDirectory), []byte("conflict"), 0o600))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			manager, workspaceRow := setupPastedImageWorkspace(t, "ready")
			outside := t.TempDir()
			tt.setup(t, workspaceRow.WorktreePath, outside)

			_, err := manager.StorePastedImage(t.Context(), workspaceRow.ID, pastedImagePNGFixture())
			require.ErrorIs(t, err, ErrPastedImagePathConflict)
			entries, readErr := os.ReadDir(outside)
			require.NoError(t, readErr)
			assert.Empty(t, entries)
		})
	}
}

func TestStorePastedImageDoesNotDirtyGitStatus(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	manager, workspaceRow := setupPastedImageWorkspace(t, "ready")

	_, err := manager.StorePastedImage(t.Context(), workspaceRow.ID, pastedImagePNGFixture())
	require.NoError(err)

	status := strings.TrimSpace(string(runWorkspaceTestGit(
		t, workspaceRow.WorktreePath, "status", "--porcelain",
	)))
	assert.Empty(status)
	entries, err := os.ReadDir(filepath.Join(workspaceRow.WorktreePath, PastedImageDirectory))
	require.NoError(err)
	for _, entry := range entries {
		assert.False(strings.HasPrefix(entry.Name(), ".tmp-paste-"), entry.Name())
	}
}

func TestStorePastedImageConcurrentFirstWrites(t *testing.T) {
	t.Parallel()
	manager, workspaceRow := setupPastedImageWorkspace(t, "ready")
	const writes = 8
	start := make(chan struct{})
	errs := make(chan error, writes)
	var wait sync.WaitGroup
	for range writes {
		wait.Go(func() {
			<-start
			_, err := manager.StorePastedImage(
				t.Context(), workspaceRow.ID, pastedImagePNGFixture(),
			)
			errs <- err
		})
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	entries, err := os.ReadDir(filepath.Join(workspaceRow.WorktreePath, PastedImageDirectory))
	require.NoError(t, err)
	assert.Len(t, entries, writes)
}

func setupPastedImageWorkspace(t *testing.T, status string) (*Manager, *db.Workspace) {
	t.Helper()
	database := dbtest.Open(t)
	worktree := initWorkspaceGitRepo(t)
	workspaceRow := &db.Workspace{
		ID:           "ws-pasted-image-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		WorktreePath: worktree,
		Status:       status,
	}
	require.NoError(t, database.InsertWorkspace(t.Context(), workspaceRow))
	return NewManager(database, t.TempDir()), workspaceRow
}

func pastedImagePNGFixture() []byte {
	return []byte("\x89PNG\r\n\x1a\nfixture")
}

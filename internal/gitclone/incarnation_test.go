package gitclone

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
)

func TestRepositoryIncarnationPartitionsCloneStorage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	baseDir := t.TempDir()
	manager := New(baseDir, nil)

	oldClone := manager.RepositoryClone(
		41, "github", "github.com", "acme", "widget",
	)
	oldPath, err := oldClone.ClonePath()
	require.NoError(err)

	newClone := manager.RepositoryClone(
		42, "github", "github.com", "acme", "widget",
	)
	newPath, err := newClone.ClonePath()
	require.NoError(err)

	assert.NotEqual(oldPath, newPath)
	assert.Equal(
		filepath.Join(
			baseDir, "repository-incarnations", "local", "repositories",
			"repo-41.git",
		),
		oldPath,
	)
	assert.Equal(
		filepath.Join(
			baseDir, "repository-incarnations", "local", "repositories",
			"repo-42.git",
		),
		newPath,
	)
}

func TestRepositoryCloneHandleKeepsImmutableIncarnation(t *testing.T) {
	require := require.New(t)
	manager := New(t.TempDir(), nil)

	retired := manager.RepositoryClone(
		41,
		"github",
		"github.com",
		"acme",
		"widget",
	)
	replacement := manager.RepositoryClone(
		42,
		"github",
		"github.com",
		"acme",
		"widget",
	)

	retiredPath, err := retired.ClonePath()
	require.NoError(err)
	replacementPath, err := replacement.ClonePath()
	require.NoError(err)
	retiredPathAgain, err := retired.ClonePath()
	require.NoError(err)

	require.Equal(retiredPath, retiredPathAgain)
	require.NotEqual(retiredPath, replacementPath)
	require.Contains(retiredPath, "repo-41.git")
	require.Contains(replacementPath, "repo-42.git")
}

func TestRepositoryRenameKeepsIncarnationCloneStorage(t *testing.T) {
	require := require.New(t)
	manager := New(t.TempDir(), nil)

	beforeClone := manager.RepositoryClone(
		42, "github", "github.com", "acme", "legacy-widget",
	)
	before, err := beforeClone.ClonePath()
	require.NoError(err)

	afterClone := manager.RepositoryClone(
		42, "github", "github.com", "acme", "widget",
	)
	after, err := afterClone.ClonePath()
	require.NoError(err)
	require.Equal(before, after)
}

func TestRepositoryCloneDoesNotAdoptRouteMatchedLegacyClone(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	manager := New(filepath.Join(root, "clones"), nil)
	legacyPath, _ := seedLegacyManagedClone(
		t, root, manager, "acme", "widget",
	)

	repository := manager.RepositoryClone(
		42, "github", "github.com", "acme", "widget",
	)
	_, err := repository.RevParse(t.Context(), "refs/heads/main")
	require.Error(err)

	incarnationPath, err := repository.ClonePath()
	require.NoError(err)
	require.NoDirExists(incarnationPath)
	require.DirExists(legacyPath)
}

func TestRepositoryCloneDoesNotAdoptAliasedLegacyClone(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	manager := New(filepath.Join(root, "clones"), nil)
	legacyPath, _ := seedLegacyManagedClone(
		t, root, manager, "acme", "legacy-widget",
	)
	manager.ReplaceCredentialAliases([]CredentialAlias{{
		Platform:        "github",
		Host:            "github.com",
		Owner:           "acme-tools",
		Name:            "current-widget",
		CredentialOwner: "acme",
		CredentialName:  "legacy-widget",
	}})

	repository := manager.RepositoryClone(
		42, "github", "github.com", "acme-tools", "current-widget",
	)
	_, err := repository.RevParse(t.Context(), "refs/heads/main")
	require.Error(err)

	incarnationPath, err := repository.ClonePath()
	require.NoError(err)
	require.NoDirExists(incarnationPath)
	require.DirExists(legacyPath)
}

func TestRepositoryCloneLeavesMismatchedLegacyCloneUntouched(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	manager := New(filepath.Join(root, "clones"), nil)
	legacyPath, err := manager.ClonePath(
		"github", "github.com", "acme", "widget",
	)
	require.NoError(err)
	runIncarnationGit(t, root, "init", "--bare", "--initial-branch=main", legacyPath)
	runIncarnationGit(
		t,
		legacyPath,
		"remote",
		"add",
		"origin",
		"https://github.com/other/repository.git",
	)

	repository := manager.RepositoryClone(
		42, "github", "github.com", "acme", "widget",
	)
	_, err = repository.RevParse(t.Context(), "refs/heads/main")
	require.Error(err)
	require.DirExists(legacyPath)
	incarnationPath, err := repository.ClonePath()
	require.NoError(err)
	require.NoDirExists(incarnationPath)
}

func TestRepositoryRenameRepointsTrustedIncarnationOrigin(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runIncarnationGit(t, root, "init", "--bare", "--initial-branch=main", remote)

	manager := New(filepath.Join(root, "clones"), nil)
	legacyClone := manager.RepositoryClone(
		42, "github", "github.com", "acme", "legacy-widget",
	)
	clonePath, err := legacyClone.ClonePath()
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(clonePath), 0o755))
	runIncarnationGit(t, root, "clone", "--bare", remote, clonePath)

	oldURL := "https://github.com/acme/legacy-widget.git"
	newURL := "https://github.com/acme/widget.git"
	runIncarnationGit(t, clonePath, "remote", "set-url", "origin", oldURL)
	runIncarnationGit(
		t,
		clonePath,
		"config",
		"url."+remote+".insteadOf",
		newURL,
	)

	renamedClone := manager.RepositoryClone(
		42, "github", "github.com", "acme", "widget",
	)
	require.NoError(renamedClone.EnsureClone(
		t.Context(),
		newURL,
	))

	out, err := gitcmd.New().Output(
		t.Context(),
		clonePath,
		"config",
		"--get",
		"remote.origin.url",
	)
	require.NoError(err)
	require.Equal(newURL, strings.TrimSpace(string(out)))
}

func TestRepositoryRenameRepointsIncarnationOriginAfterRestart(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runIncarnationGit(t, root, "init", "--bare", "--initial-branch=main", remote)

	baseDir := filepath.Join(root, "clones")
	beforeRestart := New(baseDir, nil)
	beforeClone := beforeRestart.RepositoryClone(
		42, "github", "github.com", "acme", "legacy-widget",
	)
	clonePath, err := beforeClone.ClonePath()
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(clonePath), 0o755))
	runIncarnationGit(t, root, "clone", "--bare", remote, clonePath)

	oldURL := "https://github.com/acme/legacy-widget.git"
	newURL := "https://github.com/acme/widget.git"
	runIncarnationGit(t, clonePath, "remote", "set-url", "origin", oldURL)
	runIncarnationGit(
		t,
		clonePath,
		"config",
		"url."+remote+".insteadOf",
		newURL,
	)

	afterRestart := New(baseDir, nil)
	afterClone := afterRestart.RepositoryClone(
		42, "github", "github.com", "acme", "widget",
	)
	require.NoError(afterClone.EnsureClone(
		t.Context(),
		newURL,
	))

	out, err := gitcmd.New().Output(
		t.Context(),
		clonePath,
		"config",
		"--get",
		"remote.origin.url",
	)
	require.NoError(err)
	require.Equal(newURL, strings.TrimSpace(string(out)))
}

func runIncarnationGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
}

func seedLegacyManagedClone(
	t *testing.T,
	root string,
	manager *Manager,
	owner, name string,
) (string, string) {
	t.Helper()
	require := require.New(t)
	legacyPath, err := manager.ClonePath(
		"github", "github.com", owner, name,
	)
	require.NoError(err)
	work := filepath.Join(root, "work-"+name)
	runIncarnationGit(t, root, "init", "--bare", "--initial-branch=main", legacyPath)
	runIncarnationGit(t, root, "init", "--initial-branch=main", work)
	runIncarnationGit(t, work, "config", "user.email", "test@example.com")
	runIncarnationGit(t, work, "config", "user.name", "Test User")
	require.NoError(os.WriteFile(filepath.Join(work, "README.md"), []byte("cached\n"), 0o644))
	runIncarnationGit(t, work, "add", "README.md")
	runIncarnationGit(t, work, "commit", "-m", "seed cache")
	runIncarnationGit(t, work, "push", legacyPath, "main")
	shaOut, err := gitcmd.New().Output(t.Context(), work, "rev-parse", "HEAD")
	require.NoError(err)
	runIncarnationGit(
		t,
		legacyPath,
		"remote",
		"add",
		"origin",
		"https://github.com/"+owner+"/"+name+".git",
	)
	return legacyPath, strings.TrimSpace(string(shaOut))
}

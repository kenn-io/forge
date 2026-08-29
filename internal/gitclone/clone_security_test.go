package gitclone

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRemoteURLHostRejectsMismatchedHTTPSHost(t *testing.T) {
	err := validateRemoteURLHost("github.com", "https://gitlab.com/acme/widget.git")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "gitlab.com")
	assert.Contains(t, err.Error(), "github.com")
}

func TestValidateRemoteURLHostAcceptsMatchingHTTPSHost(t *testing.T) {
	err := validateRemoteURLHost("github.com", "https://github.com/acme/widget.git")

	require.NoError(t, err)
}

func TestValidateRemoteURLIdentityAcceptsSCPStyleRemoteWithoutUser(t *testing.T) {
	err := validateRemoteURLIdentity(
		"github.com", "acme", "widget",
		"github.com:acme/widget.git",
	)

	require.NoError(t, err)
}

func TestValidateRemoteURLIdentityRejectsSCPStyleRemoteHostMismatch(t *testing.T) {
	err := validateRemoteURLIdentity(
		"github.com", "acme", "widget",
		"evil.example.com:acme/widget.git",
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "evil.example.com")
	assert.Contains(t, err.Error(), "github.com")
}

func TestValidateRemoteURLIdentityRejectsSchemeOnlyRemoteHostMismatch(t *testing.T) {
	err := validateRemoteURLIdentity(
		"github.com", "acme", "widget",
		"ssh:evil.example.com/acme/widget.git",
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssh")
	assert.Contains(t, err.Error(), "github.com")
}

func TestValidateRemoteURLHostAcceptsLocalPath(t *testing.T) {
	err := validateRemoteURLHost("github.com", "/tmp/acme/widget.git")

	require.NoError(t, err)
}

func TestValidateRemoteURLHostAcceptsFileURL(t *testing.T) {
	err := validateRemoteURLHost("github.com", "file:///C:/tmp/acme/widget.git")

	require.NoError(t, err)
}

func TestValidateRemoteURLIdentityAcceptsFileURL(t *testing.T) {
	err := validateRemoteURLIdentity(
		"github.com", "acme", "widget",
		"file:///C:/Users/RUNNER~1/AppData/Local/Temp/Test/remote/widget",
	)

	require.NoError(t, err)
}

func TestValidateRemoteURLHostAcceptsWindowsLocalPath(t *testing.T) {
	err := validateRemoteURLHost("github.com", `C:\tmp\acme\widget.git`)

	require.NoError(t, err)
}

func TestClonePathIncludesHost(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr := New("/tmp/clones", nil)

	path, err := mgr.ClonePath("github", "github.com", "owner", "repo")
	require.NoError(err)
	assert.Equal(
		filepath.Join("/tmp/clones", "github.com", "owner", "repo.git"),
		path)

	ghePath, err := mgr.ClonePath("github", "github.example.com", "owner", "repo")
	require.NoError(err)
	assert.Equal(
		filepath.Join("/tmp/clones", "github.example.com", "owner", "repo.git"),
		ghePath)
	assert.NotEqual(path, ghePath)
}

func TestClonePathPartitionsProvidersOnSharedHost(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr := New(t.TempDir(), nil)
	githubPath, err := mgr.ClonePath(
		"github", "code.example.com", "acme", "widgets",
	)
	require.NoError(err)
	forgejoPath, err := mgr.ClonePath(
		"forgejo", "code.example.com", "acme", "widgets",
	)
	require.NoError(err)

	assert.NotEqual(githubPath, forgejoPath)
	assert.Contains(forgejoPath, filepath.Join("forgejo", "code.example.com"))
}

func TestClonePathRejectsUnsafeSegments(t *testing.T) {
	tests := []struct {
		name  string
		host  string
		owner string
		repo  string
	}{
		{name: "traversal", host: "gitlab.example.com", owner: "group/../..", repo: "project"},
		{name: "dot owner", host: "gitlab.example.com", owner: "group/.", repo: "project"},
		{name: "empty owner segment", host: "gitlab.example.com", owner: "group//subgroup", repo: "project"},
		{name: "absolute owner", host: "gitlab.example.com", owner: "/group", repo: "project"},
		{name: "backslash", host: "gitlab.example.com", owner: `group\project`, repo: "project"},
		{name: "separator in repo", host: "gitlab.example.com", owner: "group", repo: "nested/project"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			mgr := New(t.TempDir(), nil)

			_, err := mgr.ClonePath("github", tt.host, tt.owner, tt.repo)

			require.Error(err)
			assert.Contains(err.Error(), "unsafe clone path")
		})
	}
}

func TestEnsureCloneRejectsTraversal(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr := New(t.TempDir(), nil)

	err := mgr.EnsureClone(t.Context(), "gitlab", "gitlab.example.com", "group/../..", "project", "/tmp/repo.git")

	require.Error(err)
	assert.Contains(err.Error(), "unsafe clone path")
}

// TestEnsureCloneValidatesRemoteURLPerCaller pins that the remoteURL
// is validated before the singleflight slot is taken. Without this,
// a caller with a mismatched-host URL could share the slot with a
// valid caller and inherit the leader's result, bypassing the host
// check entirely. We exercise this by passing a single invalid URL
// against an empty Manager — the validation must fire synchronously,
// before any git work, and surface to this caller specifically.
func TestEnsureCloneValidatesRemoteURLPerCaller(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr := New(t.TempDir(), nil)

	err := mgr.EnsureClone(
		t.Context(), "github", "github.com", "acme", "widget",
		"https://evil.example.com/acme/widget.git",
	)

	require.Error(err)
	assert.Contains(err.Error(), "does not match")
	assert.Contains(err.Error(), "evil.example.com")
}

func TestEnsureCloneValidatesRemoteURLRepoPerCaller(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mgr := New(t.TempDir(), nil)

	err := mgr.EnsureClone(
		t.Context(), "github", "github.com", "acme", "widget",
		"https://github.com/other/widget.git",
	)

	require.Error(err)
	assert.Contains(err.Error(), "does not match")
	assert.Contains(err.Error(), "other/widget")
	assert.Contains(err.Error(), "acme/widget")
}

func TestValidateRemoteTransportRequiresHostScopedHTTPAcknowledgement(t *testing.T) {
	require := require.New(t)
	mgr := New(t.TempDir(), nil)
	remote := "http://gitea.example.test:3000/acme/widget.git"

	err := mgr.validateRemoteTransport(
		"gitea", "gitea.example.test:3000", remote,
	)
	require.Error(err)
	assert.Contains(t, err.Error(), "allow_insecure = true")

	mgr.SetAllowInsecureHTTP("gitea", "gitea.example.test:3000", true)
	require.NoError(mgr.validateRemoteTransport(
		"gitea", "gitea.example.test:3000", remote,
	))
	require.Error(mgr.validateRemoteTransport(
		"forgejo", "gitea.example.test:3000", remote,
	))
}

func TestValidateRemoteTransportAllowsLoopbackHTTPWithoutPlatformPolicy(t *testing.T) {
	mgr := New(t.TempDir(), nil)

	require.NoError(t, mgr.validateRemoteTransport(
		"github", "127.0.0.1:3000",
		"http://127.0.0.1:3000/acme/widget.git",
	))
}

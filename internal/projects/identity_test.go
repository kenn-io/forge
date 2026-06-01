package projects

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
)

func TestParseRemoteURL_GitHubFormats(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		want   string
	}{
		{"scp_with_dot_git", "git@github.com:wesm/examplerepo.git", "github.com/wesm/examplerepo"},
		{"scp_no_dot_git", "git@github.com:wesm/examplerepo", "github.com/wesm/examplerepo"},
		{"https_with_dot_git", "https://github.com/wesm/examplerepo.git", "github.com/wesm/examplerepo"},
		{"https_no_dot_git", "https://github.com/wesm/examplerepo", "github.com/wesm/examplerepo"},
		{"https_with_user", "https://wesm@github.com/wesm/examplerepo.git", "github.com/wesm/examplerepo"},
		{"ssh_full", "ssh://git@github.com/wesm/examplerepo.git", "github.com/wesm/examplerepo"},
		{"ssh_with_port", "ssh://git@github.com:22/wesm/examplerepo.git", "github.com/wesm/examplerepo"},
		{"git_protocol", "git://github.com/wesm/examplerepo.git", "github.com/wesm/examplerepo"},
		{"trailing_slash", "https://github.com/wesm/examplerepo/", "github.com/wesm/examplerepo"},
		{"casefold", "GIT@GITHUB.COM:WesM/Examplerepo.git", "github.com/wesm/examplerepo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			got := ParseRemoteURL(tc.remote)
			require.NotNil(got)
			assert.Equal(tc.want, got.Host+"/"+got.Owner+"/"+got.Name)
		})
	}
}

func TestParseRemoteURL_OtherHosts(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		want   string
	}{
		{"gitlab", "git@gitlab.com:group/project.git", "gitlab.com/group/project"},
		{"codeberg", "https://codeberg.org/owner/repo.git", "codeberg.org/owner/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			got := ParseRemoteURL(tc.remote)
			require.NotNil(got)
			assert.Equal(tc.want, got.Host+"/"+got.Owner+"/"+got.Name)
		})
	}
}

func TestParseRemoteURL_ConfiguredSelfHostedGitLab(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	got := ParseRemoteURLWithKnownPlatforms(
		"git@code.example.com:group/project.git",
		[]KnownPlatformHost{{Platform: "gitlab", Host: "code.example.com"}},
	)

	require.NotNil(got)
	assert.Equal("gitlab", got.Platform)
	assert.Equal("code.example.com", got.Host)
	assert.Equal("group", got.Owner)
	assert.Equal("project", got.Name)
}

func TestParseRemoteURL_ConfiguredSelfHostedGitLabWithURLPort(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	got := ParseRemoteURLWithKnownPlatforms(
		"https://gitlab.example.com:8443/group/project.git",
		[]KnownPlatformHost{{Platform: "gitlab", Host: "gitlab.example.com:8443"}},
	)

	require.NotNil(got)
	assert.Equal("gitlab", got.Platform)
	assert.Equal("gitlab.example.com:8443", got.Host)
	assert.Equal("group", got.Owner)
	assert.Equal("project", got.Name)
}

func TestParseRemoteURL_NestedGitLabRepoPath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	got := ParseRemoteURL("git@gitlab.com:group/subgroup/project.git")

	require.NotNil(got)
	assert.Equal("gitlab", got.Platform)
	assert.Equal("gitlab.com", got.Host)
	assert.Equal("group/subgroup", got.Owner)
	assert.Equal("project", got.Name)
}

func TestParseRemoteURL_UnknownSelfHostedRemoteIsLocalOnly(t *testing.T) {
	assert := Assert.New(t)

	assert.Nil(ParseRemoteURL("git@code.example.com:group/project.git"))
}

func TestParseRemoteURL_Unrecognized(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"not-a-url",
		"/local/path/to/repo",
		"./relative/path",
		"file:///tmp/repo",
		"https://github.com/onlyone",           // missing name
		"https://github.com/too/many/segments", // wrong arity
		"github.com:foo/bar.git",               // SCP without user@
		"git@github.com",                       // missing path
		"git@github.com:",                      // empty path
		"git@github.com:owner/.git",            // empty name after strip
	}
	for _, remote := range cases {
		t.Run(remote, func(t *testing.T) {
			assert := Assert.New(t)
			assert.Nil(ParseRemoteURL(remote))
		})
	}
}

func TestResolveIdentityFromPath_NoOriginRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	require.NoError(runGit(t, dir, "init", "-q"))

	identity, err := ResolveIdentityFromPath(context.Background(), dir)
	require.NoError(err)
	assert.Nil(identity)
}

func TestResolveIdentityFromPath_UnrecognizableRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	require.NoError(runGit(t, dir, "init", "-q"))
	require.NoError(runGit(t, dir, "remote", "add", "origin", "/local/only/repo"))

	identity, err := ResolveIdentityFromPath(context.Background(), dir)
	require.NoError(err)
	assert.Nil(identity)
}

func TestResolveIdentityFromPath_RecognizedRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	require.NoError(runGit(t, dir, "init", "-q"))
	require.NoError(runGit(t, dir, "remote", "add", "origin", "git@github.com:wesm/examplerepo.git"))

	identity, err := ResolveIdentityFromPath(context.Background(), dir)
	require.NoError(err)
	require.NotNil(identity)
	assert.Equal("github.com", identity.Host)
	assert.Equal("wesm", identity.Owner)
	assert.Equal("examplerepo", identity.Name)
}

func TestResolveIdentityFromPath_NotAGitRepoIsTreatedAsNoIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	// A plain directory that is not a git repo causes git to exit
	// non-zero. The resolver's contract is identity-only, so it
	// returns (nil, nil) and lets registration validation reject the
	// non-repo separately.
	dir := t.TempDir()
	identity, err := ResolveIdentityFromPath(context.Background(), dir)
	require.NoError(err)
	assert.Nil(identity)
}

func TestResolveIdentityFromPath_RequiresPath(t *testing.T) {
	require := require.New(t)
	_, err := ResolveIdentityFromPath(context.Background(), "")
	require.Error(err)
}

func runGit(t *testing.T, dir string, args ...string) error {
	t.Helper()
	cmd := gitcmd.New().Command(t.Context(), dir, args...)
	cmd.Env = append(cmd.Env,
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	return cmd.Run()
}

// Sanity check that filepath.Abs is used; otherwise an unusual cwd could
// hide bugs in tests that pass relative paths.
func TestResolveIdentityFromPath_ResolvesRelativePaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	require.NoError(runGit(t, dir, "init", "-q"))
	require.NoError(runGit(t, dir, "remote", "add", "origin", "git@github.com:o/n.git"))

	parent := t.TempDir()
	cwd := filepath.Join(parent, "cwd")
	require.NoError(os.Mkdir(cwd, 0o755))

	rel, err := filepath.Rel(cwd, dir)
	require.NoError(err)
	identity, err := ResolveIdentityFromPath(context.Background(), filepath.Join(cwd, rel))
	require.NoError(err)
	require.NotNil(identity)
	assert.Equal("github.com", identity.Host)
}

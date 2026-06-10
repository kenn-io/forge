//go:build integration

package docs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/git/env"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/procutil"
)

type gitRepo struct {
	dir      string
	remote   string
	registry *Registry
	folderID string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	useIsolatedGitEnv(t)
	if err := procutil.Command("git", "--version").Run(); err != nil {
		t.Skip("git binary unavailable")
	}
	dir := t.TempDir()
	remote := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "middleman-fixture@example.invalid")
	runGit(t, dir, "config", "user.name", "Middleman Fixture")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "seed.md"), []byte("seed\n"), 0o644))
	runGit(t, dir, "add", "seed.md")
	runGit(t, dir, "commit", "-m", "seed")
	runGit(t, remote, "init", "--bare")
	runGit(t, dir, "remote", "add", "origin", remote)
	runGit(t, dir, "push", "-u", "origin", "main")
	reg := NewRegistry([]config.DocFolder{
		{ID: "f", Name: "F", Path: dir},
	})
	return &gitRepo{dir: dir, remote: remote, registry: reg, folderID: "f"}
}

func newGitRepoNoUpstream(t *testing.T) *gitRepo {
	t.Helper()
	useIsolatedGitEnv(t)
	if err := procutil.Command("git", "--version").Run(); err != nil {
		t.Skip("git binary unavailable")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "middleman-fixture@example.invalid")
	runGit(t, dir, "config", "user.name", "Middleman Fixture")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "seed.md"), []byte("seed\n"), 0o644))
	runGit(t, dir, "add", "seed.md")
	runGit(t, dir, "commit", "-m", "seed")
	reg := NewRegistry([]config.DocFolder{
		{ID: "f", Name: "F", Path: dir},
	})
	return &gitRepo{dir: dir, registry: reg, folderID: "f"}
}

func (g *gitRepo) writeFile(t *testing.T, rel, body string) {
	t.Helper()
	full := filepath.Join(g.dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
}

func (g *gitRepo) stage(t *testing.T, paths ...string) {
	t.Helper()
	args := append([]string{"add", "--"}, paths...)
	runGit(t, g.dir, args...)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := procutil.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = isolatedGitEnv
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), string(out))
	return string(out)
}

func useIsolatedGitEnv(t *testing.T) {
	t.Helper()
	old := isolatedGitEnv
	home := t.TempDir()
	xdgConfig := t.TempDir()
	isolatedGitEnv = append(gitenv.StripAll(os.Environ()),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"),
		"HOME="+home,
		"XDG_CONFIG_HOME="+xdgConfig,
	)
	t.Cleanup(func() {
		isolatedGitEnv = old
	})
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, args...))
}

func TestGitChangesNotARepo(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	reg := NewRegistry([]config.DocFolder{{ID: "f", Name: "F", Path: dir}})

	res, err := reg.GitChanges(context.Background(), "f")

	require.NoError(err)
	assert.False(res.IsRepo)
	assert.Empty(res.Changes)
}

func TestGitChangesEmptyRepo(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)

	res, err := g.registry.GitChanges(context.Background(), g.folderID)

	require.NoError(err)
	assert.True(res.IsRepo)
	assert.Empty(res.Changes)
	assert.Equal("main", res.Branch)
	assert.Equal("origin/main", res.Upstream)
}

func TestGitChangesIncludesUntrackedAndModifiedMarkdown(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	g.writeFile(t, "seed.md", "seed updated\n")
	g.writeFile(t, "code.go", "package x\n")

	res, err := g.registry.GitChanges(context.Background(), g.folderID)

	require.NoError(err)
	assert.True(res.IsRepo)
	gotPaths := make([]string, 0, len(res.Changes))
	for _, c := range res.Changes {
		gotPaths = append(gotPaths, c.Path)
	}
	assert.ElementsMatch([]string{"new.md", "seed.md"}, gotPaths)
	assert.Equal(1, res.IgnoredNonMarkdownCount)
	assert.Equal(suggestedCommitMessage(res.Changes), res.SuggestedMessage)
}

func TestGitChangesNoUpstream(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepoNoUpstream(t)

	res, err := g.registry.GitChanges(context.Background(), g.folderID)

	require.NoError(err)
	assert.Empty(res.Upstream)
	assert.Equal("main", res.Branch)
}

func TestGitPublishHappyPath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	g.writeFile(t, "seed.md", "seed updated\n")

	res, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: update 2 files\n\n- new.md\n- seed.md\n")

	require.NoError(err)
	assert.NotEmpty(res.Commit)
	assert.GreaterOrEqual(len(res.Commit), 40)
	assert.NotEmpty(res.ShortCommit)
	assert.Equal("main", res.Branch)
	assert.Equal("origin/main", res.Upstream)
	assert.True(res.Pushed)
	assert.Len(res.Files, 2)
	head := gitOutput(t, g.remote, "rev-parse", "main")
	assert.Equal(res.Commit, head)
}

func TestGitPublishPushesConfiguredUpstreamDespitePushDefaults(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	backup := t.TempDir()
	runGit(t, backup, "init", "--bare")
	runGit(t, g.dir, "remote", "add", "backup", backup)
	runGit(t, g.dir, "push", "backup", "main:main")
	backupInitial := gitOutput(t, backup, "rev-parse", "main")
	runGit(t, g.dir, "config", "remote.pushDefault", "backup")
	runGit(t, g.dir, "config", "push.default", "current")
	g.writeFile(t, "new.md", "# new\n")

	res, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: explicit upstream")

	require.NoError(err)
	originHead := gitOutput(t, g.remote, "rev-parse", "main")
	assert.Equal(res.Commit, originHead)
	backupHead := gitOutput(t, backup, "rev-parse", "main")
	assert.Equal(backupInitial, backupHead)
}

func TestGitPublishRefusesEmptyMessage(t *testing.T) {
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "   \n\t")

	require.ErrorIs(t, err, ErrEmptyMessage)
}

func TestGitPublishRefusesNoMarkdownChanges(t *testing.T) {
	g := newGitRepo(t)
	g.writeFile(t, "code.go", "package x\n")

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: nothing")

	require.ErrorIs(t, err, ErrNoMarkdownChanges)
}

func TestGitPublishRefusesNotARepo(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry([]config.DocFolder{{ID: "f", Name: "F", Path: dir}})

	_, err := reg.GitPublish(context.Background(), "f", "docs: x")

	require.ErrorIs(t, err, ErrNotAGitRepo)
}

func TestGitPublishRefusesIndexNotCleanUnrelatedStaged(t *testing.T) {
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	g.writeFile(t, "code.go", "package x\n")
	g.stage(t, "code.go")

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	require.ErrorIs(t, err, ErrIndexNotClean)
}

func TestGitPublishRefusesIndexNotCleanPartiallyStaged(t *testing.T) {
	g := newGitRepo(t)
	g.writeFile(t, "partial.md", "v1\n")
	g.stage(t, "partial.md")
	g.writeFile(t, "partial.md", "v2\n")

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	require.ErrorIs(t, err, ErrIndexNotClean)
}

func TestGitPublishRefusesConflict(t *testing.T) {
	g := newGitRepo(t)
	runGit(t, g.dir, "checkout", "-b", "side")
	g.writeFile(t, "seed.md", "side version\n")
	runGit(t, g.dir, "commit", "-am", "side")
	runGit(t, g.dir, "checkout", "main")
	g.writeFile(t, "seed.md", "main version\n")
	runGit(t, g.dir, "commit", "-am", "main")
	cmd := procutil.Command("git", "merge", "side")
	cmd.Dir = g.dir
	cmd.Env = isolatedGitEnv
	out, mergeErr := cmd.CombinedOutput()
	require.Error(t, mergeErr, "expected merge conflict, got clean merge: %s", out)

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	require.ErrorIs(t, err, ErrConflict)
}

func TestGitPublishRefusesNoUpstream(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepoNoUpstream(t)
	g.writeFile(t, "new.md", "# new\n")

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	var noUpstream *NoUpstreamError
	require.ErrorAs(err, &noUpstream)
	assert.Equal("main", noUpstream.Branch)
	assert.Equal("git push -u origin main", noUpstream.SuggestedCommand)
}

func TestGitPublishStagesRenamePair(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	runGit(t, g.dir, "mv", "seed.md", "renamed.md")

	res, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: rename")

	require.NoError(err)
	out := gitOutput(t, g.dir, "ls-tree", "--name-only", res.Commit)
	assert.NotContains(out, "seed.md")
	assert.Contains(out, "renamed.md")
}

func TestGitPublishStagesWorktreeRename(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	require.NoError(os.Rename(filepath.Join(g.dir, "seed.md"), filepath.Join(g.dir, "moved.md")))

	res, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: rename in worktree")

	require.NoError(err)
	out := gitOutput(t, g.dir, "ls-tree", "--name-only", res.Commit)
	assert.NotContains(out, "seed.md")
	assert.Contains(out, "moved.md")
	renames := gitOutput(t, g.dir, "show", "--name-status", "-M", "--format=", res.Commit)
	assert.Contains(renames, "R")
}

func TestGitPublishPushFailedAfterCommit(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	runGit(t, g.dir, "remote", "set-url", "origin", "/does/not/exist")

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	var pushFailed *PushFailedAfterCommitError
	require.ErrorAs(err, &pushFailed)
	assert.NotEmpty(pushFailed.Commit)
	assert.NotEmpty(pushFailed.Stderr)
	assert.NotContains(pushFailed.Stderr, "exit status")
	head := gitOutput(t, g.dir, "rev-parse", "HEAD")
	assert.Equal(head, pushFailed.Commit)
}

func TestGitPublishDoesNotRunDocsRepoHooks(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hookBody := "#!/bin/sh\necho hooked > \"" + marker + "\"\nexit 1\n"
	hookDir := filepath.Join(g.dir, ".git", "hooks")
	require.NoError(os.MkdirAll(hookDir, 0o755))
	for _, name := range []string{"pre-commit", "commit-msg", "post-commit", "pre-push"} {
		require.NoError(os.WriteFile(filepath.Join(hookDir, name), []byte(hookBody), 0o755))
	}

	res, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	require.NoError(err)
	assert.True(res.Pushed)
	assert.NoFileExists(marker, "docs repo hook executed during publish")
}

func TestGitPublishIgnoresRepoHooksPathOverride(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hooksPath := t.TempDir()
	hookBody := "#!/bin/sh\necho hooked > \"" + marker + "\"\nexit 1\n"
	require.NoError(os.WriteFile(filepath.Join(hooksPath, "pre-commit"), []byte(hookBody), 0o755))
	runGit(t, g.dir, "config", "core.hooksPath", hooksPath)

	res, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	require.NoError(err)
	assert.True(res.Pushed)
	assert.NoFileExists(marker, "core.hooksPath hook executed during publish")
}

func TestGitPublishRejectsCommandBearingLocalConfig(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"clean filter", "filter.evil.clean", "/bin/sh -c evil"},
		{"smudge filter", "filter.evil.smudge", "/bin/sh -c evil"},
		{"process filter", "filter.lfs.process", "git-lfs filter-process"},
		{"gpg program", "gpg.program", "/tmp/evil"},
		{"ssh command", "core.sshCommand", "/tmp/evil"},
		{"credential helper", "credential.helper", "/tmp/evil"},
		{"external diff", "diff.evil.command", "/tmp/evil"},
		{"textconv", "diff.evil.textconv", "/tmp/evil"},
		{"commit signing on", "commit.gpgsign", "true"},
		{"remote receive-pack", "remote.origin.receivepack", "/tmp/evil"},
		{"remote upload-pack", "remote.origin.uploadpack", "/tmp/evil"},
		{"remote vcs helper", "remote.origin.vcs", "evil"},
		{"core gitProxy", "core.gitProxy", "/tmp/evil"},
		{"core askPass", "core.askPass", "/tmp/evil"},
		{"include path", "include.path", "../evil.cfg"},
		{"includeIf path", "includeIf.gitdir:/.path", "../evil.cfg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			g := newGitRepo(t)
			g.writeFile(t, "new.md", "# new\n")
			runGit(t, g.dir, "config", tc.key, tc.value)

			_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

			var unsafe *UnsafeGitConfigError
			require.ErrorAs(err, &unsafe)
			assert.NotEmpty(unsafe.Entries)
		})
	}
}

func TestGitPublishRejectsIncludedCommandBearingConfig(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	// The attack the include gate exists for: the directive itself looks
	// harmless, but the included file enables a signing program that a
	// later `git commit` would execute.
	included := filepath.Join(t.TempDir(), "evil.cfg")
	require.NoError(os.WriteFile(included, []byte("[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = /tmp/evil\n"), 0o644))
	runGit(t, g.dir, "config", "include.path", included)

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(err, &unsafe)
	assert.NotEmpty(unsafe.Entries)
}

func TestGitPublishRejectsPushTargetInsideDocsFolder(t *testing.T) {
	cases := []struct {
		name string
		url  func(g *gitRepo) string
	}{
		{"relative path", func(g *gitRepo) string { return "./evil.git" }},
		{"absolute path", func(g *gitRepo) string { return filepath.Join(g.dir, "evil.git") }},
		{"file URL", func(g *gitRepo) string { return "file://" + filepath.Join(g.dir, "evil.git") }},
		{"percent-encoded file URL", func(g *gitRepo) string {
			// Git decodes %65 to 'e' and pushes into evil.git; the
			// containment check decodes via net/url so it compares the
			// same path git resolves rather than the escaped literal.
			return "file://" + filepath.Join(g.dir, "%65vil.git")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			g := newGitRepo(t)
			g.writeFile(t, "new.md", "# new\n")
			runGit(t, g.dir, "init", "--bare", "evil.git")
			marker := filepath.Join(t.TempDir(), "hook-ran")
			hook := "#!/bin/sh\necho hooked > \"" + marker + "\"\nexit 0\n"
			require.NoError(os.MkdirAll(filepath.Join(g.dir, "evil.git", "hooks"), 0o755))
			require.NoError(os.WriteFile(filepath.Join(g.dir, "evil.git", "hooks", "pre-receive"), []byte(hook), 0o755))
			runGit(t, g.dir, "remote", "set-url", "origin", tc.url(g))
			head := gitOutput(t, g.dir, "rev-parse", "HEAD")

			_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

			var unsafe *UnsafeGitConfigError
			require.ErrorAs(err, &unsafe)
			assert.NoFileExists(marker, "in-folder remote pre-receive hook executed")
			assert.Equal(head, gitOutput(t, g.dir, "rev-parse", "HEAD"),
				"publish committed before rejecting the push target")
		})
	}
}

func TestGitPublishRejectsPushInsteadOfRewriteIntoDocsFolder(t *testing.T) {
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	runGit(t, g.dir, "init", "--bare", "evil.git")
	// The remote URL itself looks like a safe network transport; the
	// repo-local rewrite redirects the push into the folder.
	runGit(t, g.dir, "remote", "set-url", "origin", "https://docs.example.invalid/repo.git")
	runGit(t, g.dir, "config", "url../evil.git.pushInsteadOf", "https://docs.example.invalid/repo.git")

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(err, &unsafe)
}

func TestGitPublishRejectsMixedLocalAndNetworkPushURLs(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	// One push invocation would contact both URLs; the local
	// receive-pack hardening cannot be applied per-URL, so the set is
	// refused before anything is committed.
	runGit(t, g.dir, "config", "--add", "remote.origin.pushurl", g.remote)
	runGit(t, g.dir, "config", "--add", "remote.origin.pushurl", "ssh://git@docs.example.invalid/repo.git")
	head := gitOutput(t, g.dir, "rev-parse", "HEAD")

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(err, &unsafe)
	assert.Equal(head, gitOutput(t, g.dir, "rev-parse", "HEAD"),
		"publish committed before rejecting the mixed push urls")
}

func TestGitPublishRejectsRemoteHelperPushTarget(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	marker := filepath.Join(t.TempDir(), "helper-ran")
	runGit(t, g.dir, "remote", "set-url", "origin", `ext::sh -c "touch `+marker+`"`)

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(err, &unsafe)
	assert.NoFileExists(marker, "ext:: remote helper executed during publish")
}

func TestGitPublishNeutralizesLocalRemoteReceiveHooks(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	// Local-path remotes outside the docs folder remain supported, but
	// the target repo's receive-side hooks must not run: receive-pack
	// executes on this machine and a hook would be arbitrary code. The
	// hook exits 1 so, if it ran, the push itself would also fail.
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := "#!/bin/sh\necho hooked > \"" + marker + "\"\nexit 1\n"
	require.NoError(os.WriteFile(filepath.Join(g.remote, "hooks", "pre-receive"), []byte(hook), 0o755))

	res, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	require.NoError(err)
	assert.True(res.Pushed)
	assert.NoFileExists(marker, "local remote pre-receive hook executed during publish")
	assert.Equal(res.Commit, gitOutput(t, g.remote, "rev-parse", "main"))
}

func TestGitPublishRejectsFilterAttributes(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	// Even when the driver is configured globally (here it is undefined),
	// opting paths into a filter marks the repo as LFS-style and is refused.
	g.writeFile(t, ".gitattributes", "*.md filter=lfs diff=lfs\n")

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(err, &unsafe)
	assert.NotEmpty(unsafe.Entries)
}

func TestGitPublishRejectsSubdirectoryFilterAttributes(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "sub/new.md", "# new\n")
	// A .gitattributes nested below the root opts sub/*.md into a filter.
	// A root-only scan would miss this; check-attr resolves it per path.
	g.writeFile(t, "sub/.gitattributes", "*.md filter=lfs\n")

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(err, &unsafe)
	assert.NotEmpty(unsafe.Entries)
}

func TestGitPublishGatesAttributesBeforeStatusRunsFilter(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	marker := filepath.Join(t.TempDir(), "filter-ran")
	// Simulate a globally-installed clean filter, as git-lfs would be on a
	// victim's machine. The repo only chooses to route paths through it.
	runGit(t, g.dir, "config", "--global", "filter.evil.clean",
		"sh -c 'echo ran > \""+marker+"\"; cat'")
	runGit(t, g.dir, "config", "--global", "filter.evil.smudge", "cat")
	// Attacker-controlled repo attributes opt markdown into that filter.
	g.writeFile(t, ".gitattributes", "*.md filter=evil\n")
	// Modify a tracked markdown to the same byte length as the committed
	// blob and backdate it, so git must rehash (running the clean filter)
	// to detect the change during `git status`.
	g.writeFile(t, "seed.md", "xxxx\n")
	old := time.Unix(1_000_000_000, 0)
	require.NoError(os.Chtimes(filepath.Join(g.dir, "seed.md"), old, old))

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(err, &unsafe)
	assert.NoFileExists(marker, "clean filter ran during status before the attribute gate")
}

func TestGitStatusRejectsFilterAttributes(t *testing.T) {
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, ".gitattributes", "*.md filter=lfs\n")

	_, err := g.registry.GitStatus(context.Background(), g.folderID)

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(err, &unsafe)
}

func TestGitChangesRejectsFilterAttributes(t *testing.T) {
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "sub/.gitattributes", "*.md diff=evil\n")
	g.writeFile(t, "sub/new.md", "# new\n")

	_, err := g.registry.GitChanges(context.Background(), g.folderID)

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(err, &unsafe)
}

func TestGitChangesRejectsCommandBearingLocalConfig(t *testing.T) {
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	// The preview must apply the same config gate as publish so a folder
	// with unsafe local config cannot preview as publishable and only
	// fail on submit.
	runGit(t, g.dir, "config", "gpg.program", "/tmp/evil")

	_, err := g.registry.GitChanges(context.Background(), g.folderID)

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(err, &unsafe)
}

func TestGitPublishAllowsBenignLocalConfig(t *testing.T) {
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	// Signing explicitly off and benign attributes must not trip the gate.
	runGit(t, g.dir, "config", "commit.gpgsign", "false")
	runGit(t, g.dir, "config", "tag.gpgsign", "false")
	g.writeFile(t, ".gitattributes", "*.md text=auto eol=lf\n")

	res, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	require.NoError(err)
	require.True(res.Pushed)
}

func TestGitStatusDoesNotRunRepoFsmonitor(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
	monitor := filepath.Join(t.TempDir(), "fsmonitor")
	require.NoError(os.WriteFile(monitor, []byte("#!/bin/sh\necho ran > \""+marker+"\"\nexit 1\n"), 0o755))
	runGit(t, g.dir, "config", "core.fsmonitor", monitor)

	// GitStatus runs `git status`, which honors core.fsmonitor.
	_, err := g.registry.GitStatus(context.Background(), g.folderID)

	require.NoError(err)
	assert.NoFileExists(marker, "core.fsmonitor program executed during git status")
}

func TestGitPublishBlocksExtRemoteHelperOnPush(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	marker := filepath.Join(t.TempDir(), "helper-ran")
	helper := filepath.Join(t.TempDir(), "evil")
	require.NoError(os.WriteFile(helper, []byte("#!/bin/sh\necho ran > \""+marker+"\"\n"), 0o755))
	// An attacker-controlled repo config can opt the ext transport back in
	// (modern git blocks it by default) and point a remote at an arbitrary
	// command, which push would execute without our protocol.ext override.
	runGit(t, g.dir, "config", "protocol.ext.allow", "always")
	runGit(t, g.dir, "remote", "set-url", "origin", "ext::"+helper)

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	require.Error(err)
	assert.NoFileExists(marker, "ext:: remote helper executed during push")
}

func TestGitPublishCommitFailurePreservesStderr(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "new.md", "# new\n")
	runGit(t, g.dir, "config", "user.useConfigOnly", "true")
	runGit(t, g.dir, "config", "--unset", "user.email")

	_, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: x")

	var commitFailed *CommitFailedError
	require.ErrorAs(err, &commitFailed)
	assert.Contains(commitFailed.Stderr, "email")
	assert.NotContains(commitFailed.Stderr, "exit status")
}

func TestGitPublishStagesLiteralPathspec(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	g.writeFile(t, "weird *.md", "# weird\n")
	g.writeFile(t, "decoy.md", "# decoy\n")

	res, err := g.registry.GitPublish(context.Background(), g.folderID, "docs: weird")

	require.NoError(err)
	out := gitOutput(t, g.dir, "ls-tree", "--name-only", res.Commit)
	assert.Contains(out, "weird *.md")
	assert.Contains(out, "decoy.md")
}

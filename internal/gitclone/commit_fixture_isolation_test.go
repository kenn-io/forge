package gitclone

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

// Exercise the real test binary, including TestMain, with hostile inherited Git
// state pointing only at disposable fixtures. Never probe the developer's config.
func TestCommitFixtureIgnoresInheritedGitState(t *testing.T) {
	t.Parallel()
	setupRequire := require.New(t)
	root := isolatedCommitFixtureDir(t)
	parent := filepath.Join(root, "parent")
	setupRequire.NoError(os.Mkdir(parent, 0o700))
	commitTestRun(t, parent, "git", "init", "--initial-branch=main")
	commitTestRun(t, parent, "git", "config", "user.name", "Fixture Owner")
	commitTestRun(t, parent, "git", "config", "user.email", "owner@example.test")
	setupRequire.NoError(os.WriteFile(filepath.Join(parent, "sentinel.txt"), []byte("untouched\n"), 0o600))
	commitTestRun(t, parent, "git", "add", ".")
	commitTestRun(t, parent, "git", "commit", "-m", "sentinel")
	originalHead := gitSHA(t, parent, "HEAD")
	linkedWorktree := filepath.Join(root, "linked-worktree")
	commitTestRun(t, parent, "git", "worktree", "add", "--detach", linkedWorktree, "HEAD")
	externalHome := filepath.Join(root, "external-home")
	setupRequire.NoError(os.Mkdir(externalHome, 0o700))
	globalConfig := filepath.Join(externalHome, ".gitconfig")
	systemConfig := filepath.Join(root, "system.gitconfig")
	for _, path := range []string{globalConfig, systemConfig} {
		setupRequire.NoError(os.WriteFile(path, []byte("[user]\n\tname = External Identity\n"), 0o600))
	}
	protected := make(map[string][]byte)
	for _, path := range []string{globalConfig, systemConfig, filepath.Join(parent, ".git", "config"), filepath.Join(parent, ".git", "index")} {
		contents, err := os.ReadFile(path)
		setupRequire.NoError(err)
		protected[path] = contents
	}
	executable, err := os.Executable()
	setupRequire.NoError(err)
	for _, test := range []struct {
		name     string
		tempBase string
		reject   bool
	}{
		{name: "inherited repository and config", tempBase: filepath.Join(root, "scratch")},
		{name: "temp directory inside repository", tempBase: filepath.Join(parent, "scratch"), reject: true},
		{name: "temp directory inside linked worktree", tempBase: filepath.Join(linkedWorktree, "scratch"), reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			require.NoError(os.Mkdir(test.tempBase, 0o700))
			cmd := procutil.CommandContext(t.Context(), executable, "-test.run=^TestReadCommitStats$", "-test.shuffle=on")
			cmd.Dir = parent
			cmd.Env = append(os.Environ(),
				"HOME="+externalHome, "USERPROFILE="+externalHome, "XDG_CONFIG_HOME="+externalHome,
				"GIT_DIR="+filepath.Join(parent, ".git"), "GIT_WORK_TREE="+parent,
				"GIT_INDEX_FILE="+filepath.Join(parent, ".git", "index"),
				"GIT_CONFIG_GLOBAL="+globalConfig, "GIT_CONFIG_SYSTEM="+systemConfig,
				"GIT_CONFIG_NOSYSTEM=0", "GIT_CONFIG_COUNT=1",
				"GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_VALUE_0=Injected Identity",
				"TMPDIR="+test.tempBase, "TMP="+test.tempBase, "TEMP="+test.tempBase,
			)
			out, err := cmd.CombinedOutput()
			if test.reject {
				require.Error(err)
				assert.Contains(string(out), "commit fixture must be outside all repositories and worktrees")
			} else {
				require.NoError(err, "%s", out)
			}
			assert.Equal(originalHead, gitSHA(t, parent, "HEAD"))
			for path, original := range protected {
				contents, err := os.ReadFile(path)
				require.NoError(err)
				assert.Equal(original, contents, "fixture changed external state: %s", path)
			}
		})
	}
}

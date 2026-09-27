package workspace

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBitbucketFetchesSourceBranch(t *testing.T) {
	for _, kind := range []string{"same_repo", "fork"} {
		t.Run(kind, func(t *testing.T) {
			var baseArgs, forkArgs []string
			runBase := func(_ context.Context, _ string, args ...string) error { baseArgs = args; return nil }
			runFork := func(_ context.Context, _ string, args ...string) error { forkArgs = args; return nil }
			ws := &Workspace{Platform: "bitbucket", ItemNumber: 7}
			spec := &WorkspaceLaunchSpec{Pull: &WorkspaceLaunchPull{HeadRepoKind: kind, HeadBranch: "feature/seven", HeadRepoCloneURL: "https://bitbucket.org/contributor/widgets.git"}}
			require.NoError(t, fetchWorkspaceMergeRequestHeadRefWithGit(t.Context(), runBase, runFork, "/unused", "origin", ws, spec))
			target := "+refs/heads/feature/seven:refs/pull-requests/7/from"
			if kind == "same_repo" {
				assert.Equal(t, gitArgsWithoutHooks("fetch", "--no-tags", "--recurse-submodules=no", "origin", target), baseArgs)
				assert.Empty(t, forkArgs)
			} else {
				assert.Equal(t, gitArgsWithoutHooks("fetch", "--no-tags", "--recurse-submodules=no", spec.Pull.HeadRepoCloneURL, target), forkArgs)
				assert.Empty(t, baseArgs)
			}
		})
	}
}

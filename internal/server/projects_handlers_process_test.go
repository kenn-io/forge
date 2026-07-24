package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
	managedworktree "go.kenn.io/kit/git/managed"
	"go.kenn.io/middleman/internal/procutil"
)

func TestManagedWorktreeExecutionUsesSharedProcessLimiter(t *testing.T) {
	require := require.New(t)
	restore := procutil.SetDefaultLimiterForTest(
		procutil.NewLimiterWithAcquireTimeout(1, time.Millisecond),
	)
	t.Cleanup(restore)
	release, err := procutil.TryAcquire(context.Background(), "hold test slot")
	require.NoError(err)
	t.Cleanup(release)

	_, err = runManagedWorktreeGit(
		context.Background(), gitcmd.Runner{Env: os.Environ()}, t.TempDir(), "status",
	)
	require.ErrorIs(err, procutil.ErrProcessLimitReached)

	err = runManagedWorktreeHook(context.Background(), managedworktree.HookCommand{
		Script: "/bin/true", Dir: t.TempDir(), Env: os.Environ(),
	})
	require.ErrorIs(err, procutil.ErrProcessLimitReached)

	_, err = managedWorktreeIsDirty(context.Background(), t.TempDir())
	require.ErrorIs(err, procutil.ErrProcessLimitReached)
}

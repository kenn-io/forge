package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/apiclient/generated"
)

// TestWorkspaceConcurrentSameRepoOperationsE2E exercises the per-repo
// worktree lock through the public API and SQLite. Two PRs in the same
// repo are created concurrently, then one is retried while the other is
// deleted at the same time. The lock must serialize the underlying
// `git worktree add/remove/prune` calls so the bare clone ends in a
// consistent state — no wedged worktree, no half-created branch, no
// corrupt `worktrees/` metadata.
func TestWorkspaceConcurrentSameRepoOperationsE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := context.Background()
	client := fixture.client

	// Fixture already seeded PR #1; add PR #2 in the same repo so both
	// concurrent creates target the same bare clone but different
	// worktree paths.
	seedPROnHost(t, fixture.database, "github.com", "acme", "widget", 2)

	type createResult struct {
		num int
		ws  *generated.WorkspaceResponse
		err error
	}
	created := make(chan createResult, 2)
	var wg sync.WaitGroup
	for _, num := range []int{1, 2} {
		wg.Go(func() {
			resp, err := client.HTTP.CreateWorkspaceWithResponse(
				ctx,
				generated.CreateWorkspaceInputBody{
					PlatformHost: "github.com",
					Owner:        "acme",
					Name:         "widget",
					MrNumber:     int64(num),
				},
			)
			if err != nil {
				created <- createResult{num: num, err: err}
				return
			}
			if resp.StatusCode() != http.StatusAccepted || resp.JSON202 == nil {
				created <- createResult{
					num: num,
					err: assertableStatusErr(resp.StatusCode()),
				}
				return
			}
			ready := waitForWorkspaceReady(t, ctx, client, resp.JSON202.Id)
			created <- createResult{num: num, ws: ready}
		})
	}
	wg.Wait()
	close(created)

	wsByNumber := map[int]*generated.WorkspaceResponse{}
	for r := range created {
		require.NoError(r.err, "create PR #%d", r.num)
		require.NotNil(r.ws, "create PR #%d", r.num)
		assert.Equal("ready", r.ws.Status)
		wsByNumber[r.num] = r.ws
	}
	require.Len(wsByNumber, 2)

	// Sanity check: the bare clone should now report two managed worktrees
	// in addition to itself. If the lock failed to serialize the two
	// concurrent `worktree add` calls, this list would be missing entries
	// or have duplicate paths.
	worktreeListed := listBareWorktrees(t, fixture.bare)
	require.Equal(3, worktreeListed,
		"expected bare clone + two workspace worktrees")

	// Phase 2: drive a retry and a delete against the same bare clone at
	// the same time. The lock must keep their worktree-mutation paths
	// from clobbering each other.
	retryTarget := wsByNumber[1]
	deleteTarget := wsByNumber[2]
	msg := "forced error for retry overlap"
	require.NoError(fixture.database.UpdateWorkspaceStatus(
		ctx, retryTarget.Id, "error", &msg,
	))

	force := true
	type opResult struct {
		op  string
		err error
	}
	opOut := make(chan opResult, 2)

	var phase2 sync.WaitGroup
	phase2.Go(func() {
		resp, err := client.HTTP.DeleteWorkspaceWithResponse(
			ctx, deleteTarget.Id,
			&generated.DeleteWorkspaceParams{Force: &force},
		)
		switch {
		case err != nil:
			opOut <- opResult{op: "delete", err: err}
		case resp.StatusCode() != http.StatusNoContent:
			opOut <- opResult{op: "delete", err: assertableStatusErr(resp.StatusCode())}
		default:
			opOut <- opResult{op: "delete"}
		}
	})
	phase2.Go(func() {
		resp, err := client.HTTP.RetryWorkspaceWithResponse(ctx, retryTarget.Id)
		switch {
		case err != nil:
			opOut <- opResult{op: "retry", err: err}
		case resp.StatusCode() != http.StatusAccepted || resp.JSON202 == nil:
			opOut <- opResult{op: "retry", err: assertableStatusErr(resp.StatusCode())}
		default:
			waitForWorkspaceReady(t, ctx, client, retryTarget.Id)
			opOut <- opResult{op: "retry"}
		}
	})
	phase2.Wait()
	close(opOut)
	for r := range opOut {
		require.NoError(r.err, "phase2 op %s", r.op)
	}

	// Verify final state. Deleted workspace is gone from the DB;
	// retried workspace is ready again.
	deletedRow, err := fixture.database.GetWorkspace(ctx, deleteTarget.Id)
	require.NoError(err)
	assert.Nil(deletedRow, "deleted workspace must be absent from DB")

	retriedRow, err := fixture.database.GetWorkspace(ctx, retryTarget.Id)
	require.NoError(err)
	require.NotNil(retriedRow)
	assert.Equal("ready", retriedRow.Status)
	assert.Nil(retriedRow.ErrorMessage)

	// The bare clone should now report itself + the surviving worktree
	// (the retried one). Anything else points at a corrupt metadata
	// state — exactly the failure mode the lock is supposed to prevent.
	worktreeListed = listBareWorktrees(t, fixture.bare)
	assert.Equal(2, worktreeListed,
		"bare clone left with corrupt worktree list after concurrent ops")
}

// assertableStatusErr lets the goroutines surface unexpected status
// codes without calling t.Fatal off the test goroutine.
func assertableStatusErr(status int) error {
	return &unexpectedStatusError{status: status}
}

type unexpectedStatusError struct {
	status int
}

func (e *unexpectedStatusError) Error() string {
	return "unexpected HTTP status: " + strings.TrimSpace(http.StatusText(e.status))
}

// listBareWorktrees counts entries in `git worktree list --porcelain`
// for the given bare clone. The bare clone itself counts as one entry;
// each managed worktree adds one more.
func listBareWorktrees(t *testing.T, bare string) int {
	t.Helper()
	out, stderr, err := gitcmd.New().Run(t.Context(), bare, nil, "worktree", "list", "--porcelain")
	require.NoError(t, err, "git worktree list: %s%s", out, stderr)
	count := 0
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			count++
		}
	}
	return count
}

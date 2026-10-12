package workspacetest

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/gitfixture"
)

func TestWorkspaceListReportsCommitsAheadBehindE2E(t *testing.T) {
	runParallelWorkspaceGitTest(t)

	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	var clockNow atomic.Int64
	clockNow.Store(now.UnixNano())
	fixture := setupWorkspaceServerFixture(t, nil, server.ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
		PtyOwnerInProcess:                  true,
		WorkspaceNow: func() time.Time {
			return time.Unix(0, clockNow.Load()).UTC()
		},
	})
	ws := createReadyWorkspace(t, t.Context(), fixture.client)
	waitForWorkspaceEnrichment(t, fixture.client, ws.ID, &clockNow, func(initial *generated.WorkspaceResponse) bool {
		return initial != nil && initial.CommitsAhead != nil && initial.CommitsBehind != nil &&
			initial.WorktreeDirty != nil && *initial.CommitsAhead == 0 &&
			*initial.CommitsBehind == 0 && !*initial.WorktreeDirty
	})

	gitfixture.Run(t, ws.WorktreePath, "update-ref", "-d", "refs/remotes/origin/feature")
	clockNow.Add(int64(workspaceapi.EnrichmentTTL + time.Second))
	waitForWorkspaceEnrichment(t, fixture.client, ws.ID, &clockNow, func(found *generated.WorkspaceResponse) bool {
		return found != nil && found.BranchUpstreamMissing != nil && *found.BranchUpstreamMissing
	})

	includePeers := false
	fleetResponse, err := fixture.client.HTTP.GetSnapshotWithResponse(t.Context(), &generated.GetSnapshotRequestOptions{Query: &generated.GetSnapshotQuery{IncludePeers: &includePeers}})
	require.NoError(err)
	require.NotNil(fleetResponse.JSON200)
	require.NotNil(fleetResponse.JSON200.Workspaces)
	var fleetWorkspace *generated.WorkspaceSummary
	for i := range fleetResponse.JSON200.Workspaces {
		candidate := &fleetResponse.JSON200.Workspaces[i]
		if candidate.ID == ws.ID {
			fleetWorkspace = candidate
			break
		}
	}
	require.NotNil(fleetWorkspace)
	require.NotNil(fleetWorkspace.BranchUpstreamMissing)
	assert.True(*fleetWorkspace.BranchUpstreamMissing)

	gitfixture.Run(t, ws.WorktreePath, "fetch", "origin")

	gitfixture.Run(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	gitfixture.Run(t, ws.WorktreePath, "config", "user.name", "Test")
	for _, name := range []string{"ahead-1.txt", "ahead-2.txt"} {
		require.NoError(os.WriteFile(filepath.Join(ws.WorktreePath, name), []byte(name+"\n"), 0o644))
		gitfixture.Run(t, ws.WorktreePath, "add", ".")
		gitfixture.Run(t, ws.WorktreePath, "commit", "-m", name)
	}
	require.NoError(os.WriteFile(filepath.Join(ws.WorktreePath, "uncommitted.txt"), []byte("dirty\n"), 0o644))
	clockNow.Add(int64(workspaceapi.EnrichmentTTL + time.Second))

	found := waitForWorkspaceEnrichment(t, fixture.client, ws.ID, &clockNow, func(found *generated.WorkspaceResponse) bool {
		return found != nil && found.CommitsAhead != nil && found.CommitsBehind != nil &&
			found.WorktreeDirty != nil && *found.CommitsAhead == 2 &&
			*found.CommitsBehind == 0 && *found.WorktreeDirty
	})
	require.NotNil(found)
	assert.Equal(int64(2), *found.CommitsAhead)
	assert.Equal(int64(0), *found.CommitsBehind)
	assert.True(*found.WorktreeDirty)
}

// A fork PR branch has no upstream: origin carries no ref for it and the fork
// is not an authorized push target. Its counts must still report drift from
// the provider's PR head ref so the list shows whether the local workspace is
// behind the PR.
func TestWorkspaceListReportsForkPullRequestDivergenceE2E(t *testing.T) {
	runParallelWorkspaceGitTest(t)

	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	var clockNow atomic.Int64
	clockNow.Store(now.UnixNano())
	fixture := setupWorkspaceServerFixture(t, nil, server.ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
		PtyOwnerInProcess:                  true,
		WorkspaceNow: func() time.Time {
			return time.Unix(0, clockNow.Load()).UTC()
		},
	})
	repo, err := fixture.database.GetRepoByIdentity(
		t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	headSHA := gitfixture.SHA(t, fixture.remote, "feature")
	gitfixture.Run(t, fixture.remote, "update-ref", "refs/heads/fork-feature", headSHA)
	gitfixture.Run(
		t, fixture.bare, "config", "--add",
		"url."+fixture.remote+".insteadOf", "https://github.com/fork/widget.git",
	)
	seededAt := time.Now().UTC().Truncate(time.Second)
	prID, err := fixture.database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repo.ID, PlatformID: 2000, Number: 2,
		URL: "https://github.com/acme/widget/pull/2", Title: "Fork PR #2",
		Author: "fork-user", State: "open", Body: "fork test body",
		HeadBranch: "fork-feature", BaseBranch: "main",
		HeadRepoCloneURL: "https://github.com/fork/widget.git",
		CreatedAt:        seededAt, UpdatedAt: seededAt, LastActivityAt: seededAt,
	})
	require.NoError(err)
	require.NoError(fixture.database.EnsureKanbanState(t.Context(), prID))
	created, err := fixture.client.HTTP.CreateWorkspaceWithResponse(t.Context(), &generated.CreateWorkspaceRequestOptions{Body: &generated.CreateWorkspaceInputBody{
		Provider: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "widget", MrNumber: 2,
	}})
	require.NoError(err)
	require.NotNil(created.JSON202)
	ws := waitForWorkspaceReady(t, t.Context(), fixture.client, created.JSON202.ID)

	countsAre := func(ahead, behind int64) func(*generated.WorkspaceResponse) bool {
		return func(found *generated.WorkspaceResponse) bool {
			return found != nil && found.CommitsAhead != nil && found.CommitsBehind != nil &&
				*found.CommitsAhead == ahead && *found.CommitsBehind == behind
		}
	}
	waitForWorkspaceEnrichment(t, fixture.client, ws.ID, &clockNow, countsAre(0, 0))

	gitfixture.Run(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	gitfixture.Run(t, ws.WorktreePath, "config", "user.name", "Test")
	require.NoError(os.WriteFile(filepath.Join(ws.WorktreePath, "local.txt"), []byte("local\n"), 0o644))
	gitfixture.Run(t, ws.WorktreePath, "add", ".")
	gitfixture.Run(t, ws.WorktreePath, "commit", "-m", "local only")
	clockNow.Add(int64(workspaceapi.EnrichmentTTL + time.Second))
	waitForWorkspaceEnrichment(t, fixture.client, ws.ID, &clockNow, countsAre(1, 0))

	// The contributor pushes; the next clone fetch moves the provider head ref.
	gitfixture.Run(t, ws.WorktreePath, "update-ref", "refs/pull/2/head", "HEAD")
	gitfixture.Run(t, ws.WorktreePath, "reset", "--hard", "HEAD~1")
	clockNow.Add(int64(workspaceapi.EnrichmentTTL + time.Second))
	found := waitForWorkspaceEnrichment(t, fixture.client, ws.ID, &clockNow, countsAre(0, 1))
	require.NotNil(found)
	require.NotNil(found.CommitsVsPrHead)
	assert.True(*found.CommitsVsPrHead,
		"clients must know these counts are not a pushable upstream")

	// The phone list and remote hosts read workspaces from the fleet snapshot;
	// a dropped flag there would offer branch sync that can only fail.
	includePeers := false
	fleetResponse, err := fixture.client.HTTP.GetSnapshotWithResponse(t.Context(), &generated.GetSnapshotRequestOptions{Query: &generated.GetSnapshotQuery{IncludePeers: &includePeers}})
	require.NoError(err)
	require.NotNil(fleetResponse.JSON200)
	var fleetWorkspace *generated.WorkspaceSummary
	for i := range fleetResponse.JSON200.Workspaces {
		if fleetResponse.JSON200.Workspaces[i].ID == ws.ID {
			fleetWorkspace = &fleetResponse.JSON200.Workspaces[i]
			break
		}
	}
	require.NotNil(fleetWorkspace)
	require.NotNil(fleetWorkspace.CommitsVsPrHead)
	assert.True(*fleetWorkspace.CommitsVsPrHead)
}

func waitForWorkspaceEnrichment(
	t *testing.T,
	client *apiclient.Client,
	workspaceID string,
	clockNow *atomic.Int64,
	matches func(*generated.WorkspaceResponse) bool,
) *generated.WorkspaceResponse {
	t.Helper()
	require := require.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	lastEnrichmentError := ""
	loggedEnrichmentError := ""
	lastResponse := ""
	for {
		started := time.Now()
		resp, err := client.HTTP.ListWorkspacesWithResponse(ctx)
		if resp != nil {
			lastResponse = string(resp.Body)
		}
		require.NoError(err, "request elapsed=%s context=%v last enrichment error=%q response=%s",
			time.Since(started), ctx.Err(), lastEnrichmentError, lastResponse)
		require.NotNil(resp.JSON200, "context=%v response=%s", ctx.Err(), lastResponse)
		for i := range resp.JSON200.Workspaces {
			candidate := &resp.JSON200.Workspaces[i]
			if candidate.ID != workspaceID {
				continue
			}
			if matches(candidate) {
				return candidate
			}
			if candidate.EnrichmentError != nil {
				lastEnrichmentError = *candidate.EnrichmentError
			}
			if candidate.EnrichmentStatus == "failed" {
				// Failed probes use the same TTL as successful refreshes. Keep the
				// controlled clock moving so each failed attempt can retry.
				clockNow.Add(int64(workspaceapi.EnrichmentTTL + time.Second))
				if lastEnrichmentError != loggedEnrichmentError {
					t.Logf("advancing enrichment retry clock after: %s", lastEnrichmentError)
					loggedEnrichmentError = lastEnrichmentError
				}
			}
		}
		select {
		case <-ctx.Done():
			require.NoError(ctx.Err(), "workspace enrichment did not converge: %s", resp.Body)
		case <-ticker.C:
		}
	}
}

func TestWorkspaceListEnrichmentRetriesAfterProbeFailureE2E(t *testing.T) {
	runParallelWorkspaceGitTest(t)
	require := require.New(t)
	var clockNow atomic.Int64
	clockNow.Store(time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC).UnixNano())
	fixture := setupWorkspaceServerFixture(t, nil, server.ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
		PtyOwnerInProcess:                  true,
		WorkspaceNow:                       func() time.Time { return time.Unix(0, clockNow.Load()).UTC() },
	})
	ws := createReadyWorkspace(t, t.Context(), fixture.client)
	countsReady := func(found *generated.WorkspaceResponse) bool {
		return found.CommitsAhead != nil && found.CommitsBehind != nil &&
			*found.CommitsAhead == 0 && *found.CommitsBehind == 0 && found.EnrichmentError == nil
	}
	waitForWorkspaceEnrichment(t, fixture.client, ws.ID, &clockNow, countsReady)

	// Make one real Git probe fail, then restore the same worktree before retrying.
	offlinePath := ws.WorktreePath + "-offline"
	require.NoError(os.Rename(ws.WorktreePath, offlinePath))
	offline := true
	t.Cleanup(func() {
		if offline {
			require.NoError(os.Rename(offlinePath, ws.WorktreePath))
		}
	})
	clockNow.Add(int64(workspaceapi.EnrichmentTTL + time.Second))
	failed := waitForWorkspaceEnrichment(t, fixture.client, ws.ID, &clockNow,
		func(found *generated.WorkspaceResponse) bool { return found.EnrichmentStatus == "failed" })
	require.NotNil(failed.EnrichmentError)
	require.NoError(os.Rename(offlinePath, ws.WorktreePath))
	offline = false

	recovered := waitForWorkspaceEnrichment(t, fixture.client, ws.ID, &clockNow, countsReady)
	require.Nil(recovered.EnrichmentError)
	require.Equal(int64(0), *recovered.CommitsAhead)
	require.Equal(int64(0), *recovered.CommitsBehind)
}

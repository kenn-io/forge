package github

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
)

// bypassGraphQL answers merge bypass reads with a fixed permission and counts
// them.
type bypassGraphQL struct {
	allowed atomic.Bool
	reads   atomic.Int32
}

func newBypassGraphQL(allowed bool) *bypassGraphQL {
	g := &bypassGraphQL{}
	g.allowed.Store(allowed)
	return g
}

func (g *bypassGraphQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	if !bytes.Contains(body, []byte("viewerCanMergeAsAdmin")) {
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":null}}}`)
		return
	}
	g.reads.Add(1)
	if g.allowed.Load() {
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{"viewerCanMergeAsAdmin":true}}}}`)
		return
	}
	_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{"viewerCanMergeAsAdmin":false}}}}`)
}

// schemaRejectingGraphQL answers like a GitHub host whose schema has no
// viewerCanMergeAsAdmin field.
type schemaRejectingGraphQL struct {
	reads atomic.Int32
}

func (g *schemaRejectingGraphQL) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	g.reads.Add(1)
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w,
		`{"errors":[{"message":"Field 'viewerCanMergeAsAdmin' doesn't exist on type 'PullRequest'"}]}`)
}

// setMergeableState records a later sync of pull request number that saw
// mergeableState.
func setMergeableState(t *testing.T, database *db.DB, repoID int64, number int, mergeableState string) {
	t.Helper()
	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, number)
	require.NoError(t, err)
	require.NotNil(t, mr)
	mr.MergeableState = mergeableState
	mr.UpdatedAt = mr.UpdatedAt.Add(time.Second)
	_, err = database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
}

func storedBypass(t *testing.T, database *db.DB, repoID int64, number int) bool {
	t.Helper()
	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, number)
	require.NoError(t, err)
	require.NotNil(t, mr)
	return mr.ViewerCanMergeAsAdmin
}

func TestBlockedPullRequestRecordsBypassAsTheMergeCredential(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	app := newBypassGraphQL(false)
	user := newBypassGraphQL(true)
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)
	setMergeableState(t, database, repoID, 7, "blocked")
	var announced []int
	syncer.SetOnMergeBypassChanged(func(_ context.Context, _ int64, number int) { announced = append(announced, number) })

	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	require.True(storedBypass(t, database, repoID, 7))
	require.Zero(app.reads.Load(), "GitHub answers for the credential that asks, so the sync App must not")
	require.EqualValues(1, user.reads.Load())
	require.Equal([]int{7}, announced)
}

func TestUnblockedPullRequestSkipsBypassReadAndClearsIt(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	gql := newBypassGraphQL(true)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	setMergeableState(t, database, repoID, 7, "blocked")
	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)
	assert.True(storedBypass(t, database, repoID, 7))

	setMergeableState(t, database, repoID, 7, "clean")
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	assert.False(storedBypass(t, database, repoID, 7))
	assert.EqualValues(1, gql.reads.Load(), "a pull request nothing blocks is not read")
}

func TestBypassClearsWhenGitHubWithdrawsIt(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	gql := newBypassGraphQL(true)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	setMergeableState(t, database, repoID, 7, "blocked")
	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)
	assert.True(storedBypass(t, database, repoID, 7))

	gql.allowed.Store(false)
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	assert.False(storedBypass(t, database, repoID, 7))
}

func TestFailedDetailSyncDoesNotReadBypass(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	gql := newBypassGraphQL(true)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	setMergeableState(t, database, repoID, 7, "blocked")

	failed := error(errors.New("pull request detail read failed"))
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &failed)

	assert.False(storedBypass(t, database, repoID, 7))
	assert.Zero(gql.reads.Load())
}

func TestBypassReadsStopForHostWithoutTheField(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	gql := &schemaRejectingGraphQL{}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	setMergeableState(t, database, repoID, 7, "blocked")
	setMergeableState(t, database, repoID, 8, "blocked")

	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 8, &synced)

	assert.False(storedBypass(t, database, repoID, 7))
	assert.False(storedBypass(t, database, repoID, 8))
	assert.EqualValues(1, gql.reads.Load(), "a host that rejected the field is not asked again")
}

func TestDetailSyncRecordsBypassForBlockedPullRequest(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	gql := newBypassGraphQL(true)
	client := servingPR(9)
	client.singlePR.MergeableState = new("blocked")
	syncer, database, _, repoID := newMergeQueueTestSyncerWithClient(t, gql, client)

	require.NoError(syncer.SyncMR(t.Context(), "acme", "widgets", 9))

	require.True(storedBypass(t, database, repoID, 9))
}

// mergeStateGraphQL answers merge state reads with a fixed state and head and
// merge bypass reads with false, counting each kind.
type mergeStateGraphQL struct {
	state       string
	head        string
	failing     atomic.Bool
	stateReads  atomic.Int32
	bypassReads atomic.Int32
}

func (g *mergeStateGraphQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case bytes.Contains(body, []byte("mergeStateStatus")) && g.failing.Load():
		g.stateReads.Add(1)
		_, _ = io.WriteString(w, `{"errors":[{"message":"Something went wrong while executing your query."}]}`)
	case bytes.Contains(body, []byte("mergeStateStatus")):
		g.stateReads.Add(1)
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{"mergeStateStatus":"`+
			g.state+`","headRefOid":"`+g.head+`"}}}}`)
	case bytes.Contains(body, []byte("viewerCanMergeAsAdmin")):
		g.bypassReads.Add(1)
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{"viewerCanMergeAsAdmin":false}}}}`)
	default:
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":null}}}`)
	}
}

// syncSnapshot records a later sync of pull request number that saw
// mergeableState at the given head and base commits.
func syncSnapshot(t *testing.T, database *db.DB, repoID int64, number int, head, base, mergeableState string) {
	t.Helper()
	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, number)
	require.NoError(t, err)
	require.NotNil(t, mr)
	mr.PlatformHeadSHA, mr.PlatformBaseSHA, mr.MergeableState = head, base, mergeableState
	mr.UpdatedAt = mr.UpdatedAt.Add(time.Second)
	_, err = database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
}

func storedMergeableState(t *testing.T, database *db.DB, repoID int64, number int) string {
	t.Helper()
	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, number)
	require.NoError(t, err)
	require.NotNil(t, mr)
	return mr.MergeableState
}

func TestMergeCredentialStateReplacesSyncCredentialBlock(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	app := &mergeStateGraphQL{state: "BLOCKED", head: "head-1"}
	user := &mergeStateGraphQL{state: "CLEAN", head: "head-1"}
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)
	syncSnapshot(t, database, repoID, 7, "head-1", "base-1", "blocked")
	var announced []int
	syncer.SetOnMergeBypassChanged(func(_ context.Context, _ int64, number int) { announced = append(announced, number) })

	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	require.Equal("clean", storedMergeableState(t, database, repoID, 7))
	require.Zero(app.stateReads.Load(), "GitHub answers for the credential that asks, so the sync App must not")
	require.Zero(user.bypassReads.Load(), "a pull request the merge credential may merge needs no bypass")
	require.Equal([]int{7}, announced)

	syncSnapshot(t, database, repoID, 7, "head-1", "base-1", "blocked")
	require.Equal("clean", storedMergeableState(t, database, repoID, 7),
		"a later sync credential read of the same commits must not block the merge again")

	syncSnapshot(t, database, repoID, 7, "head-2", "base-1", "blocked")
	require.Equal("blocked", storedMergeableState(t, database, repoID, 7),
		"a new head commit needs a new merge credential read")
}

func TestMergeCredentialBlockKeepsBlockAndReadsBypass(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	app := &mergeStateGraphQL{state: "BLOCKED", head: "head-1"}
	user := &mergeStateGraphQL{state: "CLEAN", head: "head-1"}
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)
	syncSnapshot(t, database, repoID, 7, "head-1", "base-1", "blocked")
	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)
	assert.Equal("clean", storedMergeableState(t, database, repoID, 7))

	user.state = "BLOCKED"
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	assert.Equal("blocked", storedMergeableState(t, database, repoID, 7))
	assert.EqualValues(1, user.bypassReads.Load())
	syncSnapshot(t, database, repoID, 7, "head-1", "base-1", "blocked")
	assert.Equal("blocked", storedMergeableState(t, database, repoID, 7))
}

func TestMergeCredentialStateForAnotherHeadIsIgnored(t *testing.T) {
	t.Parallel()

	app := &mergeStateGraphQL{state: "BLOCKED", head: "head-1"}
	user := &mergeStateGraphQL{state: "CLEAN", head: "head-2"}
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)
	syncSnapshot(t, database, repoID, 7, "head-1", "base-1", "blocked")

	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	assert.Equal(t, "blocked", storedMergeableState(t, database, repoID, 7))
	assert.Empty(t, syncer.MergeCredentialMergeableStateForRepository(t.Context(), repoID, 7, "head-1"))
	assert.Equal(t, "clean", syncer.MergeCredentialMergeableStateForRepository(t.Context(), repoID, 7, "head-2"))
}

func TestSingleCredentialBlockSkipsMergeStateRead(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	gql := &mergeStateGraphQL{state: "CLEAN", head: "head-1"}
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	syncSnapshot(t, database, repoID, 7, "head-1", "base-1", "blocked")

	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	assert.Equal("blocked", storedMergeableState(t, database, repoID, 7))
	assert.Zero(gql.stateReads.Load(), "the synced state already answers for a credential that also merges")
	assert.Empty(syncer.MergeCredentialMergeableStateForRepository(t.Context(), repoID, 7, "head-1"))
}

func TestUnconfirmedMergeCredentialStateRevertsToSyncBlock(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	app := &mergeStateGraphQL{state: "BLOCKED", head: "head-1"}
	user := &mergeStateGraphQL{state: "CLEAN", head: "head-1"}
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)
	syncSnapshot(t, database, repoID, 7, "head-1", "base-1", "blocked")
	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)
	assert.Equal("clean", storedMergeableState(t, database, repoID, 7))

	user.failing.Store(true)
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	assert.Equal("blocked", storedMergeableState(t, database, repoID, 7),
		"a merge credential read that fails cannot keep the merge enabled")
	syncSnapshot(t, database, repoID, 7, "head-1", "base-1", "blocked")
	assert.Equal("blocked", storedMergeableState(t, database, repoID, 7))
}

func TestMergeCredentialStateDropsWhenRouteStopsSplittingCredentials(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	app := &mergeStateGraphQL{state: "BLOCKED", head: "head-1"}
	user := &mergeStateGraphQL{state: "CLEAN", head: "head-1"}
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)
	syncSnapshot(t, database, repoID, 7, "head-1", "base-1", "blocked")
	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)
	assert.Equal("clean", storedMergeableState(t, database, repoID, 7))

	syncer.SetGitHubRouters(nil)
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	assert.Equal("blocked", storedMergeableState(t, database, repoID, 7),
		"one credential that reads and merges answers for itself")
	assert.Zero(app.stateReads.Load())
}

func TestMergeCredentialReadRefreshesObservationTime(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	app := &mergeStateGraphQL{state: "BLOCKED", head: "head-1"}
	user := &mergeStateGraphQL{state: "CLEAN", head: "head-1"}
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)
	syncSnapshot(t, database, repoID, 7, "head-1", "base-1", "blocked")
	first := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	now := first
	syncer.SetClock(func() time.Time { return now })
	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	now = first.Add(time.Hour)
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(t, err)
	require.NotNil(t, mr)
	assert.Equal("clean", mr.MergeableState)
	require.NotNil(t, mr.MergeableStateObservedAt)
	assert.True(now.Equal(*mr.MergeableStateObservedAt), "observed at %v, want %v", mr.MergeableStateObservedAt, now)
}

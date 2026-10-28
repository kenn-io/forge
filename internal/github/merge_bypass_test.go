package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
)

// bypassGraphQL answers merge rules reads with a fixed merge state and bypass
// permission and counts them.
type bypassGraphQL struct {
	state   atomic.Value
	allowed atomic.Bool
	reads   atomic.Int32
}

func newBypassGraphQL(state string, allowed bool) *bypassGraphQL {
	g := &bypassGraphQL{}
	g.state.Store(state)
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
	_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"mergeStateStatus":%q,"viewerCanMergeAsAdmin":%t}}}}`,
		g.state.Load(), g.allowed.Load())
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
	// Pull request syncs build snapshots from provider data, which never
	// carries the merge credential's rules.
	mr.MergeRules = ""
	mr.UpdatedAt = mr.UpdatedAt.Add(time.Second)
	_, err = database.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
}

func storedRules(t *testing.T, database *db.DB, repoID int64, number int) string {
	t.Helper()
	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, number)
	require.NoError(t, err)
	require.NotNil(t, mr)
	return mr.MergeRules
}

func TestBlockedPullRequestRecordsBypassAsTheMergeCredential(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	app := newBypassGraphQL("BLOCKED", false)
	user := newBypassGraphQL("BLOCKED", true)
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)
	setMergeableState(t, database, repoID, 7, "blocked")
	var announced []int
	syncer.SetOnMergeBypassChanged(func(_ context.Context, _ int64, number int) { announced = append(announced, number) })

	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	require.Equal(MergeRulesBlockedBypassable, storedRules(t, database, repoID, 7))
	require.Zero(app.reads.Load(), "GitHub answers for the credential that asks, so the sync App must not")
	require.EqualValues(1, user.reads.Load())
	require.Equal([]int{7}, announced)
}

func TestRulesThatBlockOnlyTheSyncAppRecordClearForTheMergeCredential(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	// A ruleset whose bypass list holds the user's team blocks the App, but
	// the user's merge bypasses nothing, so GitHub reports it clean.
	app := newBypassGraphQL("BLOCKED", false)
	user := newBypassGraphQL("CLEAN", false)
	syncer, database, repo, repoID := newSplitCredentialMergeQueueSyncer(t, app, user)
	setMergeableState(t, database, repoID, 7, "blocked")

	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)
	require.Equal(MergeRulesClear, storedRules(t, database, repoID, 7))

	setMergeableState(t, database, repoID, 7, "blocked")
	require.Equal(MergeRulesClear, storedRules(t, database, repoID, 7),
		"the App's pull request sync must not overwrite the merge credential's view")
}

func TestMergeRulesStayUnknownWhileGitHubComputesTheMergeState(t *testing.T) {
	t.Parallel()

	gql := newBypassGraphQL("UNKNOWN", true)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	setMergeableState(t, database, repoID, 7, "blocked")
	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	require.Empty(t, storedRules(t, database, repoID, 7))
}

func TestUnblockedPullRequestSkipsBypassReadAndClearsIt(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	gql := newBypassGraphQL("BLOCKED", true)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	setMergeableState(t, database, repoID, 7, "blocked")
	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)
	assert.Equal(MergeRulesBlockedBypassable, storedRules(t, database, repoID, 7))

	setMergeableState(t, database, repoID, 7, "clean")
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	assert.Empty(storedRules(t, database, repoID, 7))
	assert.EqualValues(1, gql.reads.Load(), "a pull request nothing blocks is not read")
}

func TestBypassClearsWhenGitHubWithdrawsIt(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	gql := newBypassGraphQL("BLOCKED", true)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	setMergeableState(t, database, repoID, 7, "blocked")
	var synced error
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)
	assert.Equal(MergeRulesBlockedBypassable, storedRules(t, database, repoID, 7))

	gql.allowed.Store(false)
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &synced)

	assert.Equal(MergeRulesBlocked, storedRules(t, database, repoID, 7))
}

func TestFailedDetailSyncDoesNotReadBypass(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	gql := newBypassGraphQL("BLOCKED", true)
	syncer, database, repo, repoID := newMergeQueueTestSyncer(t, gql)
	setMergeableState(t, database, repoID, 7, "blocked")

	failed := error(errors.New("pull request detail read failed"))
	syncer.refreshMergeBypassAfterDetailSync(t.Context(), repo, repoID, 7, &failed)

	assert.Empty(storedRules(t, database, repoID, 7))
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

	assert.Empty(storedRules(t, database, repoID, 7))
	assert.Empty(storedRules(t, database, repoID, 8))
	assert.EqualValues(1, gql.reads.Load(), "a host that rejected the field is not asked again")
}

func TestDetailSyncRecordsBypassForBlockedPullRequest(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	gql := newBypassGraphQL("BLOCKED", true)
	client := servingPR(9)
	client.singlePR.MergeableState = new("blocked")
	syncer, database, _, repoID := newMergeQueueTestSyncerWithClient(t, gql, client)

	require.NoError(syncer.SyncMR(t.Context(), "acme", "widgets", 9))

	require.Equal(MergeRulesBlockedBypassable, storedRules(t, database, repoID, 9))
}

package github

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
	platformgithub "go.kenn.io/forge/platform/github"
)

const blockingChecksHead = "abc123def456"

// seedBlockingChecksMR stores an open PR targeting a release branch and returns
// a syncer over mock.
func seedBlockingChecksMR(t *testing.T, mock *mockClient, checksJSON string) (*Syncer, RepoRef, int64) {
	t.Helper()
	d := openTestDB(t)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := reposeed.Seed(t.Context(), d, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(t, err)
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	_, err = d.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 1000, Number: 1,
		URL: "https://github.com/owner/repo/pull/1", Title: "test PR", Author: "alice",
		State: "open", HeadBranch: "feature", BaseBranch: "release/1.x",
		PlatformHeadSHA: blockingChecksHead, CIStatus: "success", CIChecksJSON: checksJSON,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(t, err)
	syncer := NewSyncer(map[string]Client{"github.com": mock}, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
	return syncer, repo, repoID
}

func passingBuildRun() []*gh.CheckRun {
	return []*gh.CheckRun{{
		ID: new(int64(1)), Name: new("build"), Status: new("completed"), Conclusion: new("success"),
		CheckSuite: &gh.CheckSuite{ID: new(int64(100))},
	}}
}

func storedCIChecks(t *testing.T, d *db.DB, repoID int64) (*db.MergeRequest, map[string]db.CICheck) {
	t.Helper()
	mr, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
	require.NoError(t, err)
	require.NotNil(t, mr)
	var checks []db.CICheck
	require.NoError(t, json.Unmarshal([]byte(mr.CIChecksJSON), &checks))
	byName := make(map[string]db.CICheck, len(checks))
	for _, check := range checks {
		byName[check.Name] = check
	}
	return mr, byName
}

func TestCIRefreshListsRequiredCheckThatNeverReported(t *testing.T) {
	assert := assert.New(t)
	var askedBranch string
	mock := &mockClient{
		checkRuns: passingBuildRun(),
		getRequiredChecksFn: func(_ context.Context, _, _, branch string) (platformgithub.RequiredChecks, error) {
			askedBranch = branch
			return platformgithub.RequiredChecks{Contexts: []string{"build", "deploy-gate"}}, nil
		},
	}
	syncer, repo, repoID := seedBlockingChecksMR(t, mock, "")

	warnings, err := syncer.RefreshMRCIStatusOnProvider(t.Context(), repo, repoID, 1, blockingChecksHead)

	require.NoError(t, err)
	assert.Empty(warnings)
	assert.Equal("release/1.x", askedBranch, "requirements come from the PR's base branch")
	mr, checks := storedCIChecks(t, syncer.db, repoID)
	assert.Equal("pending", mr.CIStatus)
	assert.True(mr.CIHadPending)
	assert.Equal(db.CICheck{Name: "deploy-gate", Status: "expected", Required: true, Origin: "required_rule"}, checks["deploy-gate"])
	assert.True(checks["build"].Required)
	assert.Empty(checks["build"].Origin)
}

func TestCIRefreshListsRunThatFailedBeforeCreatingAnyCheck(t *testing.T) {
	assert := assert.New(t)
	var askedHead string
	mock := &mockClient{
		checkRuns: passingBuildRun(),
		listAllWorkflowRunsFn: func(_ context.Context, _, _, head string) ([]*gh.WorkflowRun, error) {
			askedHead = head
			return []*gh.WorkflowRun{{
				ID: new(int64(9)), WorkflowID: new(int64(2)), CheckSuiteID: new(int64(200)),
				Name: new("Gate"), Path: new(".github/workflows/gate.yml"), Event: new("pull_request_target"),
				Status: new("completed"), Conclusion: new("startup_failure"),
				HTMLURL: new("https://github.com/owner/repo/actions/runs/9"),
			}}, nil
		},
	}
	syncer, repo, repoID := seedBlockingChecksMR(t, mock, "")

	_, err := syncer.RefreshMRCIStatusOnProvider(t.Context(), repo, repoID, 1, blockingChecksHead)

	require.NoError(t, err)
	assert.Equal(blockingChecksHead, askedHead)
	mr, checks := storedCIChecks(t, syncer.db, repoID)
	assert.Equal("failure", mr.CIStatus)
	assert.Equal(db.CICheck{
		Name: "Gate", Status: "completed", Conclusion: "startup_failure",
		URL: "https://github.com/owner/repo/actions/runs/9", App: "GitHub Actions", Origin: "workflow_run",
	}, checks["Gate"])
}

func TestCIRefreshStaysGreenWhenEveryRequiredCheckPassed(t *testing.T) {
	assert := assert.New(t)
	mock := &mockClient{
		checkRuns: passingBuildRun(),
		getRequiredChecksFn: func(context.Context, string, string, string) (platformgithub.RequiredChecks, error) {
			return platformgithub.RequiredChecks{Contexts: []string{"build"}}, nil
		},
	}
	syncer, repo, repoID := seedBlockingChecksMR(t, mock, "")

	_, err := syncer.RefreshMRCIStatusOnProvider(t.Context(), repo, repoID, 1, blockingChecksHead)

	require.NoError(t, err)
	mr, checks := storedCIChecks(t, syncer.db, repoID)
	assert.Equal("success", mr.CIStatus)
	assert.False(mr.CIHadPending)
	assert.Len(checks, 1)
	assert.True(checks["build"].Required)
}

func TestCIRefreshKeepsLastKnownChecksWhenABlockingSourceFails(t *testing.T) {
	const stored = `[{"name":"deploy-gate","status":"expected","required":true,"origin":"required_rule"}]`
	transient := errors.New("connection reset")
	tests := []struct {
		name string
		mock *mockClient
	}{
		{"required checks read", &mockClient{
			checkRuns: passingBuildRun(),
			getRequiredChecksFn: func(context.Context, string, string, string) (platformgithub.RequiredChecks, error) {
				return platformgithub.RequiredChecks{}, transient
			},
		}},
		{"workflow runs read", &mockClient{
			checkRuns: passingBuildRun(),
			listAllWorkflowRunsFn: func(context.Context, string, string, string) ([]*gh.WorkflowRun, error) {
				return nil, transient
			},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer, repo, repoID := seedBlockingChecksMR(t, tt.mock, stored)

			warnings, err := syncer.RefreshMRCIStatusOnProvider(t.Context(), repo, repoID, 1, blockingChecksHead)

			require.NoError(t, err)
			assert.Equal(t, []string{ciRefreshWarning}, warnings)
			mr, err := syncer.db.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
			require.NoError(t, err)
			assert.JSONEq(t, stored, mr.CIChecksJSON)
		})
	}
}

func TestCIRefreshStillUpdatesWhenWorkflowRunsAreRefused(t *testing.T) {
	assert := assert.New(t)
	mock := &mockClient{
		checkRuns: passingBuildRun(),
		listAllWorkflowRunsFn: func(context.Context, string, string, string) ([]*gh.WorkflowRun, error) {
			return nil, &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusForbidden}, Message: "Resource not accessible by integration"}
		},
		getRequiredChecksFn: func(context.Context, string, string, string) (platformgithub.RequiredChecks, error) {
			return platformgithub.RequiredChecks{WorkflowPaths: []string{".github/workflows/gate.yml"}}, nil
		},
	}
	syncer, repo, repoID := seedBlockingChecksMR(t, mock, "")

	warnings, err := syncer.RefreshMRCIStatusOnProvider(t.Context(), repo, repoID, 1, blockingChecksHead)

	require.NoError(t, err)
	assert.Empty(warnings)
	mr, checks := storedCIChecks(t, syncer.db, repoID)
	assert.Equal("success", mr.CIStatus, "a refused list does not prove the required workflow never ran")
	assert.Contains(checks, "build")
	assert.NotContains(checks, ".github/workflows/gate.yml")
}

func TestUnchangedParentRechecksAStoredFailedRun(t *testing.T) {
	assert := assert.New(t)
	stored := `[{"name":"Gate","status":"completed","conclusion":"startup_failure","origin":"workflow_run"}]`
	syncer, repo, repoID := seedBlockingChecksMR(t, &mockClient{checkRuns: passingBuildRun()}, stored)
	mr, err := syncer.db.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
	require.NoError(t, err)
	require.False(t, mr.CIHadPending)

	_, err = syncer.markUnchangedMRDetailFetched(t.Context(), repo, repoID, 1, mr, 0)

	require.NoError(t, err)
	fresh, checks := storedCIChecks(t, syncer.db, repoID)
	assert.Equal("success", fresh.CIStatus, "a same-head rerun clears the failure without a page visit")
	assert.NotContains(checks, "Gate")
}

func bulkRollupPR(now time.Time, headSHA string) *BulkPR {
	return &BulkPR{
		PR:               buildOpenPRWithSHA(1, now, headSHA),
		Comments:         []*gh.IssueComment{},
		CommentsComplete: true, ReviewsComplete: true, CommitsComplete: true, CIComplete: true,
		CheckRuns: []*gh.CheckRun{{Name: new("build"), Status: new("completed"), Conclusion: new("success")}},
	}
}

func TestBulkRollupKeepsBlockingEntriesForTheSameHead(t *testing.T) {
	assert := assert.New(t)
	stored := `[
		{"name":"build","status":"completed","conclusion":"success","required":true},
		{"name":"deploy-gate","status":"expected","required":true,"origin":"required_rule"},
		{"name":"Gate","status":"completed","conclusion":"startup_failure","origin":"workflow_run"}
	]`
	syncer, repo, repoID := seedBlockingChecksMR(t, &mockClient{}, stored)
	now := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)

	err := syncer.syncOpenMRFromBulk(t.Context(), repo, repoID,
		snapshotRevisions(t, syncer.db, repoID, db.ArchiveItemTypeMergeRequest), now,
		bulkRollupPR(now, blockingChecksHead), false)

	require.NoError(t, err)
	mr, checks := storedCIChecks(t, syncer.db, repoID)
	assert.Equal("failure", mr.CIStatus, "the rollup's green checks must not hide the failed run")
	assert.Equal("expected", checks["deploy-gate"].Status)
	assert.Equal("startup_failure", checks["Gate"].Conclusion)
	assert.True(checks["build"].Required)
}

func TestBulkRollupReplacesAMissingEntryOnceTheCheckReports(t *testing.T) {
	assert := assert.New(t)
	stored := `[{"name":"build","status":"expected","required":true,"origin":"required_rule"}]`
	syncer, repo, repoID := seedBlockingChecksMR(t, &mockClient{}, stored)
	now := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)

	err := syncer.syncOpenMRFromBulk(t.Context(), repo, repoID,
		snapshotRevisions(t, syncer.db, repoID, db.ArchiveItemTypeMergeRequest), now,
		bulkRollupPR(now, blockingChecksHead), false)

	require.NoError(t, err)
	mr, checks := storedCIChecks(t, syncer.db, repoID)
	assert.Equal("success", mr.CIStatus)
	assert.Equal(db.CICheck{Name: "build", Status: "completed", Conclusion: "success", Required: true}, checks["build"])
}

func TestBulkRollupDropsBlockingEntriesWhenTheHeadMoved(t *testing.T) {
	assert := assert.New(t)
	stored := `[{"name":"deploy-gate","status":"expected","required":true,"origin":"required_rule"}]`
	syncer, repo, repoID := seedBlockingChecksMR(t, &mockClient{}, stored)
	now := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)

	err := syncer.syncOpenMRFromBulk(t.Context(), repo, repoID,
		snapshotRevisions(t, syncer.db, repoID, db.ArchiveItemTypeMergeRequest), now,
		bulkRollupPR(now, "fedcba987654"), false)

	require.NoError(t, err)
	_, checks := storedCIChecks(t, syncer.db, repoID)
	assert.NotContains(checks, "deploy-gate")
	assert.Contains(checks, "build")
}

func TestCIRefreshListsEveryBlockingSourceTogether(t *testing.T) {
	assert := assert.New(t)
	mock := &mockClient{
		checkRuns: passingBuildRun(),
		getRequiredChecksFn: func(context.Context, string, string, string) (platformgithub.RequiredChecks, error) {
			return platformgithub.RequiredChecks{Contexts: []string{"build", "deploy-gate"}}, nil
		},
		listAllWorkflowRunsFn: func(context.Context, string, string, string) ([]*gh.WorkflowRun, error) {
			return []*gh.WorkflowRun{{
				ID: new(int64(9)), WorkflowID: new(int64(2)), CheckSuiteID: new(int64(200)),
				Name: new("Gate"), Path: new(".github/workflows/gate.yml"), Event: new("pull_request_target"),
				Status: new("completed"), Conclusion: new("startup_failure"),
			}}, nil
		},
	}
	syncer, repo, repoID := seedBlockingChecksMR(t, mock, "")

	_, err := syncer.RefreshMRCIStatusOnProvider(t.Context(), repo, repoID, 1, blockingChecksHead)

	require.NoError(t, err)
	mr, checks := storedCIChecks(t, syncer.db, repoID)
	assert.Equal("failure", mr.CIStatus, "a failed run outranks a check that has not reported")
	assert.True(mr.CIHadPending)
	assert.Equal(db.CICheck{Name: "deploy-gate", Status: "expected", Required: true, Origin: "required_rule"}, checks["deploy-gate"])
	assert.Equal("workflow_run", checks["Gate"].Origin)
	assert.True(checks["build"].Required)
}

func TestCIRefreshSkipsBranchRulesForAMergedPR(t *testing.T) {
	assert := assert.New(t)
	askedRules := false
	mock := &mockClient{
		checkRuns: passingBuildRun(),
		getRequiredChecksFn: func(context.Context, string, string, string) (platformgithub.RequiredChecks, error) {
			askedRules = true
			return platformgithub.RequiredChecks{Contexts: []string{"build", "deploy-gate"}}, nil
		},
	}
	syncer, repo, repoID := seedBlockingChecksMR(t, mock, "")
	mr, err := syncer.db.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
	require.NoError(t, err)
	mr.State = db.MergeRequestStateMerged
	_, err = syncer.db.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)

	_, err = syncer.RefreshMRCIStatusOnProvider(t.Context(), repo, repoID, 1, blockingChecksHead)

	require.NoError(t, err)
	assert.False(askedRules, "today's rules say nothing about what a merged PR needed")
	refreshed, checks := storedCIChecks(t, syncer.db, repoID)
	assert.Equal("success", refreshed.CIStatus)
	assert.NotContains(checks, "deploy-gate")
	assert.Contains(checks, "build")
}

func TestBulkRollupKeepsAFailedRunWhenAJobSharesItsName(t *testing.T) {
	assert := assert.New(t)
	stored := `[{"name":"build","status":"completed","conclusion":"startup_failure","origin":"workflow_run"}]`
	syncer, repo, repoID := seedBlockingChecksMR(t, &mockClient{}, stored)
	now := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)

	err := syncer.syncOpenMRFromBulk(t.Context(), repo, repoID,
		snapshotRevisions(t, syncer.db, repoID, db.ArchiveItemTypeMergeRequest), now,
		bulkRollupPR(now, blockingChecksHead), false)

	require.NoError(t, err)
	mr, err := syncer.db.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
	require.NoError(t, err)
	assert.Equal("failure", mr.CIStatus, "a passing job of another workflow does not clear the failed run")
	var checks []db.CICheck
	require.NoError(t, json.Unmarshal([]byte(mr.CIChecksJSON), &checks))
	assert.ElementsMatch([]db.CICheck{
		{Name: "build", Status: "completed", Conclusion: "success"},
		{Name: "build", Status: "completed", Conclusion: "startup_failure", Origin: "workflow_run"},
	}, checks)
}

func TestBulkHydrationLeavesAFailedRunForARestRead(t *testing.T) {
	tests := []struct {
		name      string
		stored    string
		wantFresh bool
	}{
		{"failed run", `[{"name":"Gate","status":"completed","conclusion":"startup_failure","origin":"workflow_run"}]`, false},
		{"rollup checks only", `[{"name":"build","status":"completed","conclusion":"success"}]`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer, repo, repoID := seedBlockingChecksMR(t, &mockClient{}, tt.stored)
			now := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)
			bulk := bulkRollupPR(now, blockingChecksHead)
			bulk.ReviewThreadsComplete, bulk.TimelineComplete = true, true

			err := syncer.syncOpenMRFromBulk(t.Context(), repo, repoID,
				snapshotRevisions(t, syncer.db, repoID, db.ArchiveItemTypeMergeRequest), now,
				bulk, false)

			require.NoError(t, err)
			mr, err := syncer.db.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
			require.NoError(t, err)
			assert.Equal(t, tt.wantFresh, mr.DetailFetchedAt != nil,
				"only a REST CI read can clear a failed run, so the detail drain must still pick the PR")
		})
	}
}

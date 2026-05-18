package testutil

import (
	"sync"
	"sync/atomic"
	"testing"

	gh "github.com/google/go-github/v84/github"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFixtureClientPullRequestsAreReturnedAsCopies(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	client := NewFixtureClient()
	fc, ok := client.(*FixtureClient)
	require.True(ok)

	originalHead := "old-head"
	originalBase := "old-base"
	fc.OpenPRs["acme/widgets"] = []*gh.PullRequest{{
		Number: new(1),
		Head:   &gh.PullRequestBranch{SHA: &originalHead},
		Base:   &gh.PullRequestBranch{SHA: &originalBase},
	}}
	fc.PRs["acme/widgets"] = []*gh.PullRequest{{
		Number: new(1),
		Head:   &gh.PullRequestBranch{SHA: &originalHead},
		Base:   &gh.PullRequestBranch{SHA: &originalBase},
	}}

	listed, err := fc.ListOpenPullRequests(t.Context(), "acme", "widgets")
	require.NoError(err)
	require.Len(listed, 1)
	got, err := fc.GetPullRequest(t.Context(), "acme", "widgets", 1)
	require.NoError(err)
	require.NotNil(got)

	mutatedHead := "caller-mutated"
	listed[0].Head.SHA = &mutatedHead
	got.Base.SHA = &mutatedHead
	fc.UpdatePullRequestSHAs("acme", "widgets", 1, "new-head", "new-base")

	assert.Equal("caller-mutated", listed[0].GetHead().GetSHA())
	assert.Equal("caller-mutated", got.GetBase().GetSHA())
	fresh, err := fc.GetPullRequest(t.Context(), "acme", "widgets", 1)
	require.NoError(err)
	require.NotNil(fresh)
	assert.Equal("new-head", fresh.GetHead().GetSHA())
	assert.Equal("new-base", fresh.GetBase().GetSHA())
}

func TestFixtureClientCreateReviewWithCommentsRecordsReview(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	client := NewFixtureClient()
	fc, ok := client.(*FixtureClient)
	require.True(ok)
	headSHA := "head-sha"
	number := 7
	body := "inline note"
	fc.OpenPRs["acme/widgets"] = []*gh.PullRequest{{
		Number: &number,
		Head:   &gh.PullRequestBranch{SHA: &headSHA},
	}}

	review, err := fc.CreateReviewWithComments(
		t.Context(),
		"acme",
		"widgets",
		7,
		"REQUEST_CHANGES",
		"needs work",
		"head-sha",
		[]*gh.DraftReviewComment{{Body: &body}},
	)

	require.NoError(err)
	require.NotNil(review)
	assert.Equal("REQUEST_CHANGES", review.GetState())
	assert.Equal("needs work", review.GetBody())
	require.Len(fc.Reviews["acme/widgets#7"], 1)
	assert.Equal(review.GetID(), fc.Reviews["acme/widgets#7"][0].GetID())
}

func TestFixtureClientCheckRunsConcurrentStatusUpdates(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	client := NewFixtureClient()
	fc, ok := client.(*FixtureClient)
	require.True(ok)

	headSHA := "head-sha"
	status := "queued"
	conclusion := ""
	fc.PRs["acme/widgets"] = []*gh.PullRequest{{
		Number: new(1),
		Head:   &gh.PullRequestBranch{SHA: &headSHA},
	}}
	fc.CheckRuns["acme/widgets@head-sha"] = []*gh.CheckRun{{
		Name:       new("test"),
		Status:     &status,
		Conclusion: &conclusion,
	}}

	const goroutines = 8
	const iterations = 100
	var errors atomic.Int64
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range iterations {
				if !fc.SetPullRequestCheckRunStatus(
					"acme", "widgets", 1, "completed", "success",
				) {
					errors.Add(1)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range iterations {
				runs, err := fc.ListCheckRunsForRef(t.Context(), "acme", "widgets", headSHA)
				if err != nil || len(runs) != 1 {
					errors.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	runs, err := fc.ListCheckRunsForRef(t.Context(), "acme", "widgets", headSHA)
	require.NoError(err)
	require.Len(runs, 1)
	assert.Zero(errors.Load())
	assert.Equal("completed", runs[0].GetStatus())
	assert.Equal("success", runs[0].GetConclusion())
}

func TestFixtureClientWorkflowRunsConcurrentAccess(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	client := NewFixtureClient()
	fc, ok := client.(*FixtureClient)
	require.True(ok)

	const goroutines = 8
	const iterations = 100
	var errors atomic.Int64
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := range iterations {
				runID := int64(j)
				fc.SetWorkflowRuns(
					"acme", "widgets", "head-sha",
					[]*gh.WorkflowRun{{ID: &runID}},
				)
			}
		}()
		go func() {
			defer wg.Done()
			for range iterations {
				_, err := fc.ListWorkflowRunsForHeadSHA(
					t.Context(), "acme", "widgets", "head-sha",
				)
				if err != nil {
					errors.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	runs, err := fc.ListWorkflowRunsForHeadSHA(t.Context(), "acme", "widgets", "head-sha")
	require.NoError(err)
	assert.Zero(errors.Load())
	assert.Len(runs, 1)
}

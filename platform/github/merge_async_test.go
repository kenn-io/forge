package github_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/github"
)

// fakeAsyncMerge is a GitHub.com merge-async endpoint whose operation
// finishes completeAfter the submission. A zero completeAfter never finishes.
type fakeAsyncMerge struct {
	t             *testing.T
	completeAfter time.Duration
	submittedAt   time.Time
	polls         int
}

func (f *fakeAsyncMerge) roundTrip(req *http.Request) (*http.Response, error) {
	const operationPath = "/repos/acme/widget/pulls/7/merge-async"
	status, body := http.StatusOK, `{"status":"pending","details":{"uuid":"operation-id"}}`
	switch {
	case req.Method == http.MethodPut && req.URL.Path == operationPath && f.submittedAt.IsZero():
		f.submittedAt = time.Now()
		status = http.StatusAccepted
	case req.Method == http.MethodGet && req.URL.Path == operationPath+"/operation-id" && !f.submittedAt.IsZero():
		f.polls++
		if f.completeAfter > 0 && time.Since(f.submittedAt) >= f.completeAfter {
			body = `{"status":"merged","details":{"message":"Pull request merged.","sha":"merge-sha"}}`
		}
	default:
		assert.Fail(f.t, "unexpected request", "%s %s", req.Method, req.URL.Path)
		status, body = http.StatusNotFound, `{"message":"Not Found"}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (f *fakeAsyncMerge) client(t *testing.T) *github.Client {
	httpClient := &http.Client{Transport: platform.RoundTripFunc(f.roundTrip)}
	client, err := github.NewClient(github.ClientConfig{
		Host: "github.com", Read: httpClient, Write: httpClient, Notifications: httpClient,
		Clock: time.Now,
	})
	require.NoError(t, err)
	return client
}

func TestMergeAsyncReportsCompletionWithinOneCappedPoll(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		completeAfter time.Duration
		maxPolls      int
	}{
		{"one second", time.Second, 5},
		{"a few seconds", 2400 * time.Millisecond, 9},
		{"ten seconds", 10 * time.Second, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				assert := assert.New(t)
				fake := &fakeAsyncMerge{t: t, completeAfter: tc.completeAfter}

				result, err := fake.client(t).MergePullRequest(
					t.Context(), "acme", "widget", 7, "title", "message", "squash", "head", false,
				)
				require.NoError(t, err)
				assert.True(result.GetMerged())
				assert.Equal("merge-sha", result.GetSHA())

				lag := time.Since(fake.submittedAt) - tc.completeAfter
				assert.GreaterOrEqual(lag, time.Duration(0))
				assert.LessOrEqual(lag, 400*time.Millisecond, "merge noticed too late")
				assert.LessOrEqual(fake.polls, tc.maxPolls, "polled too often")
			})
		})
	}
}

func TestMergeAsyncGivesUpAfterFourMinutes(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		fake := &fakeAsyncMerge{t: t}

		result, err := fake.client(t).MergePullRequest(
			t.Context(), "acme", "widget", 7, "title", "message", "squash", "head", false,
		)
		assert.Nil(result)
		require.ErrorContains(t, err, "GitHub asynchronous merge did not complete within 4m0s")

		elapsed := time.Since(fake.submittedAt)
		assert.GreaterOrEqual(elapsed, 4*time.Minute)
		assert.LessOrEqual(elapsed, 4*time.Minute+400*time.Millisecond)
		// Three ramp polls (100, 200 and 400ms apart), then one every 400ms
		// until the deadline.
		assert.LessOrEqual(fake.polls, 602)
	})
}

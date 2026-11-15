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
// It records when each request arrives.
type fakeAsyncMerge struct {
	t             *testing.T
	completeAfter time.Duration
	submittedAt   time.Time
	requests      []time.Time
}

// assertPollSpacing checks every gap between consecutive requests: never
// faster than GitHub's documented once a second, never slower than 5s.
func (f *fakeAsyncMerge) assertPollSpacing(t *testing.T) {
	t.Helper()

	for i := 1; i < len(f.requests); i++ {
		gap := f.requests[i].Sub(f.requests[i-1])
		assert.GreaterOrEqual(t, gap, time.Second, "request %d", i)
		assert.LessOrEqual(t, gap, 5*time.Second, "request %d", i)
	}
}

func (f *fakeAsyncMerge) roundTrip(req *http.Request) (*http.Response, error) {
	const operationPath = "/repos/acme/widget/pulls/7/merge-async"
	f.requests = append(f.requests, time.Now())
	status, body := http.StatusOK, `{"status":"pending","details":{"uuid":"operation-id"}}`
	switch {
	case req.Method == http.MethodPut && req.URL.Path == operationPath && f.submittedAt.IsZero():
		f.submittedAt = time.Now()
		status = http.StatusAccepted
	case req.Method == http.MethodGet && req.URL.Path == operationPath+"/operation-id" && !f.submittedAt.IsZero():
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
	t.Helper()

	httpClient := &http.Client{Transport: platform.RoundTripFunc(f.roundTrip)}
	client, err := github.NewClient(github.ClientConfig{
		Host: "github.com", Read: httpClient, Write: httpClient, Notifications: httpClient,
		Clock: time.Now,
	})
	require.NoError(t, err)
	return client
}

func TestMergeAsyncReportsCompletionWithinOnePollInterval(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		completeAfter time.Duration
		maxLag        time.Duration
	}{
		{"one second", time.Second, 0},
		{"a few seconds", 2400 * time.Millisecond, 1210 * time.Millisecond},
		{"ten seconds", 10 * time.Second, 2 * time.Second},
		{"three minutes", 3 * time.Minute, 5 * time.Second},
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
				assert.LessOrEqual(lag, tc.maxLag, "merge noticed too late")
				fake.assertPollSpacing(t)
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
		assert.LessOrEqual(elapsed, 4*time.Minute+5*time.Second)
		fake.assertPollSpacing(t)
		// The submission plus at most 60 polls: polling every second would
		// spend 240 requests of the hourly limit on a merge that never finishes.
		assert.LessOrEqual(len(fake.requests), 61)
	})
}

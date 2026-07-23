package github

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/tokenauth"
)

// countingRoundTripper records wire attempts and returns a fixed status so a
// test can prove exactly how many attempts reached the underlying transport.
type countingRoundTripper struct {
	calls  atomic.Int64
	status int
}

func (c *countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return &http.Response{
		StatusCode: c.status,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestWireAttemptAllowanceRefusesBeyondAdmittedCeiling(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	base := &countingRoundTripper{status: http.StatusInternalServerError}
	budget := NewSyncBudget(100)
	transport := WrapSyncBudgetTransport(base, budget)
	ctx := WithWireAttemptAllowance(WithArchiveSyncBudget(t.Context()), 2)

	for attempt := 1; attempt <= 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.test/repos/o/r", nil)
		require.NoError(err)
		resp, err := transport.RoundTrip(req)
		require.NoError(err)
		assert.Equal(http.StatusInternalServerError, resp.StatusCode)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.test/repos/o/r", nil)
	require.NoError(err)
	resp, err := transport.RoundTrip(req)
	assert.Nil(resp)
	require.ErrorIs(err, platform.ErrWireAttemptBudget)

	assert.Equal(int64(2), base.calls.Load())
	assert.Equal(2, budget.ArchiveSpent())
	assert.Equal(2, budget.Spent())
}

func TestWireAttemptAllowanceBoundsAuthRetries(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	base := &countingRoundTripper{status: http.StatusUnauthorized}
	budget := NewSyncBudget(100)
	// budgetTransport sits beneath AuthTransport, exactly as the production
	// clients layer it, so an authentication retry is a second wire attempt
	// that must draw from the same admitted allowance.
	authRT := tokenauth.AuthTransport{
		Source:              newMutableRuntimeAuthTokenSource("first-token"),
		Base:                WrapSyncBudgetTransport(base, budget),
		SetHeader:           tokenauth.BearerAuthHeader,
		RetryOnUnauthorized: true,
	}
	ctx := WithWireAttemptAllowance(WithArchiveSyncBudget(t.Context()), 1)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.test/repos/o/r", nil)
	require.NoError(err)

	resp, err := authRT.RoundTrip(req)
	assert.Nil(resp)
	require.ErrorIs(err, platform.ErrWireAttemptBudget)
	// The initial attempt spent the only admitted unit; the authentication
	// retry was refused locally without a second wire attempt.
	assert.Equal(int64(1), base.calls.Load())
	assert.Equal(1, budget.ArchiveSpent())
}

func TestWireAttemptAllowanceLeavesContextsWithoutAllowanceUnbounded(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	base := &countingRoundTripper{status: http.StatusInternalServerError}
	budget := NewSyncBudget(100)
	transport := WrapSyncBudgetTransport(base, budget)
	// A live sync context carries no attempt allowance, so its retries are
	// never refused by the archive ceiling.
	ctx := WithSyncBudget(t.Context())

	for range 5 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.test/repos/o/r", nil)
		require.NoError(err)
		resp, err := transport.RoundTrip(req)
		require.NoError(err)
		assert.Equal(http.StatusInternalServerError, resp.StatusCode)
	}
	assert.Equal(int64(5), base.calls.Load())
	assert.Zero(budget.ArchiveSpent())
	assert.Equal(5, budget.Spent())
}

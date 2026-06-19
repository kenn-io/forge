package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBudgetTransport_CountsSyncContext(t *testing.T) {
	assert := assert.New(t)

	budget := NewSyncBudget(100)
	bt := &budgetTransport{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return httptest.NewRecorder().Result(), nil
		}),
		budget: budget,
	}

	ctx := WithSyncBudget(t.Context())
	req, _ := http.NewRequestWithContext(
		ctx, "GET", "https://api.github.com/repos/o/n/pulls", nil,
	)
	_, err := bt.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(1, budget.Spent())
}

func TestBudgetTransport_SkipsNotModifiedResponses(t *testing.T) {
	assert := assert.New(t)

	budget := NewSyncBudget(100)
	bt := &budgetTransport{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			rec.WriteHeader(http.StatusNotModified)
			return rec.Result(), nil
		}),
		budget: budget,
	}

	ctx := WithSyncBudget(t.Context())
	req, _ := http.NewRequestWithContext(
		ctx, "GET", "https://api.github.com/repos/o/n/pulls", nil,
	)
	_, err := bt.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(0, budget.Spent(),
		"304 responses should not spend sync budget")
}

func TestBudgetTransport_SkipsNonSyncContext(t *testing.T) {
	assert := assert.New(t)

	budget := NewSyncBudget(100)
	bt := &budgetTransport{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return httptest.NewRecorder().Result(), nil
		}),
		budget: budget,
	}

	req, _ := http.NewRequestWithContext(
		t.Context(), "GET",
		"https://api.github.com/repos/o/n/pulls", nil,
	)
	_, err := bt.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(0, budget.Spent(),
		"non-sync context should not increment budget")
}

func TestBudgetTransport_CountsMultipleRequests(t *testing.T) {
	assert := assert.New(t)

	budget := NewSyncBudget(100)
	bt := &budgetTransport{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return httptest.NewRecorder().Result(), nil
		}),
		budget: budget,
	}

	ctx := WithSyncBudget(t.Context())
	for range 5 {
		req, _ := http.NewRequestWithContext(
			ctx, "GET",
			"https://api.github.com/repos/o/n/pulls", nil,
		)
		_, err := bt.RoundTrip(req)
		require.NoError(t, err)
	}

	assert.Equal(5, budget.Spent())
}

func TestBudgetTransport_CountsEvenOnError(t *testing.T) {
	assert := assert.New(t)

	budget := NewSyncBudget(100)
	bt := &budgetTransport{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return nil, http.ErrHandlerTimeout
		}),
		budget: budget,
	}

	ctx := WithSyncBudget(t.Context())
	req, _ := http.NewRequestWithContext(
		ctx, "GET",
		"https://api.github.com/repos/o/n/pulls", nil,
	)
	_, _ = bt.RoundTrip(req)

	assert.Equal(1, budget.Spent(),
		"budget should count even when base transport errors")
}

func TestWithSyncBudget_PreservesExistingValues(t *testing.T) {
	type customKey struct{}
	base := context.WithValue(
		t.Context(), customKey{}, "hello",
	)
	ctx := WithSyncBudget(base)

	assert.Equal(t, "hello", ctx.Value(customKey{}))
	_, ok := ctx.Value(syncBudgetKey{}).(bool)
	assert.True(t, ok)
}

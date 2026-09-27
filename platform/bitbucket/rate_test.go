package bitbucket_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucket"
)

type rateObservation struct {
	rate     platform.Rate
	requests int
}

func (r *rateObservation) RecordRequest()                    { r.requests++ }
func (r *rateObservation) UpdateFromRate(rate platform.Rate) { r.rate = rate }

func TestRateLimitPublishesExhaustionAndRecoversOnSuccess(t *testing.T) {
	observer := &rateObservation{}
	status := 429
	c, err := bitbucket.NewClient("bitbucket.org", credential("token"), platform.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "/2.0/repositories/team/{11111111-1111-4111-8111-111111111111}/refs/tags", r.URL.Path)
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"values":[],"isLastPage":true}`)), Request: r}, nil
	}), observer)
	require.NoError(t, err)
	_, err = c.ListTags(t.Context(), ref)
	require.ErrorIs(t, err, platform.ErrRateLimited)
	assert.Equal(t, 0, observer.rate.Remaining)
	assert.Equal(t, -1, observer.rate.Limit)
	assert.True(t, observer.rate.Reset.IsZero())
	status = 200
	_, err = c.ListTags(t.Context(), ref)
	require.NoError(t, err)
	assert.Equal(t, -1, observer.rate.Remaining)
	assert.Equal(t, 2, observer.requests)
}

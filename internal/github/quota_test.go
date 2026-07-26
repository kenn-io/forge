package github

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/tokenauth"
)

var (
	quotaTestApp  = IdentityKey{Host: "github.com", Principal: "installation:42"}
	quotaTestUser = IdentityKey{Host: "github.com", Principal: "user:7"}
)

func TestQuotaRegistryScopesObservationsByCredentialAndResource(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	registry := NewQuotaRegistry()
	reset := time.Date(2026, 7, 24, 18, 0, 0, 0, time.UTC)

	registry.ObserveHeaders(quotaTestApp, QuotaResourceREST,
		quotaTestHeaders(15000, 14900, reset))
	registry.ObserveHeaders(quotaTestUser, QuotaResourceREST,
		quotaTestHeaders(5000, 4900, reset))
	registry.ObserveHeaders(quotaTestApp, QuotaResourceGraphQL,
		quotaTestHeaders(10000, 9900, reset))

	appREST, ok := registry.Get(quotaTestApp, QuotaResourceREST)
	require.True(ok)
	userREST, ok := registry.Get(quotaTestUser, QuotaResourceREST)
	require.True(ok)
	appGraphQL, ok := registry.Get(quotaTestApp, QuotaResourceGraphQL)
	require.True(ok)

	assert.Equal(15000, appREST.Limit)
	assert.Equal(14900, appREST.Remaining)
	assert.Equal(5000, userREST.Limit)
	assert.Equal(4900, userREST.Remaining)
	assert.Equal(10000, appGraphQL.Limit)
	assert.Equal(9900, appGraphQL.Remaining)
	assert.Equal(reset, appGraphQL.ResetAt)
}

func TestQuotaRegistryIncompleteHeadersPreserveLastProviderFacts(t *testing.T) {
	assert := assert.New(t)
	registry := NewQuotaRegistry()
	reset := time.Date(2026, 7, 24, 18, 0, 0, 0, time.UTC)
	registry.ObserveHeaders(quotaTestUser, QuotaResourceREST,
		quotaTestHeaders(5000, 4321, reset))

	registry.ObserveHeaders(quotaTestUser, QuotaResourceREST, http.Header{})

	pool, ok := registry.Get(quotaTestUser, QuotaResourceREST)
	require.True(t, ok)
	assert.Equal(5000, pool.Limit)
	assert.Equal(4321, pool.Remaining)
	assert.Equal(reset, pool.ResetAt)
	assert.Equal(2, pool.Requests)
}

func TestQuotaRegistryReserveRequiresEveryKnownPool(t *testing.T) {
	assert := assert.New(t)
	registry := NewQuotaRegistry()
	reset := time.Now().UTC().Add(time.Hour)
	registry.UpdateSnapshot(quotaTestApp, QuotaResourceREST,
		Rate{Limit: 15000, Remaining: 1000, Reset: reset})

	unknown := registry.CheckReserve(
		quotaTestApp,
		[]QuotaResource{QuotaResourceREST, QuotaResourceGraphQL}, 10, RateReserveBuffer,
	)
	assert.False(unknown.Allowed)
	assert.False(unknown.Known)

	registry.UpdateSnapshot(quotaTestApp, QuotaResourceGraphQL,
		Rate{Limit: 10000, Remaining: RateReserveBuffer + 9, Reset: reset})
	insufficient := registry.CheckReserve(
		quotaTestApp,
		[]QuotaResource{QuotaResourceREST, QuotaResourceGraphQL}, 10, RateReserveBuffer,
	)
	assert.False(insufficient.Allowed)
	assert.True(insufficient.Known)
	assert.Equal(reset, *insufficient.ResetAt)

	registry.UpdateSnapshot(quotaTestApp, QuotaResourceGraphQL,
		Rate{Limit: 10000, Remaining: RateReserveBuffer + 10, Reset: reset})
	allowed := registry.CheckReserve(
		quotaTestApp,
		[]QuotaResource{QuotaResourceREST, QuotaResourceGraphQL}, 10, RateReserveBuffer,
	)
	assert.True(allowed.Allowed)
	assert.True(allowed.Known)
}

func TestQuotaRegistryTreatsExpiredProviderWindowAsUnknown(t *testing.T) {
	registry := NewQuotaRegistry()
	registry.UpdateSnapshot(quotaTestUser, QuotaResourceREST, Rate{
		Limit: 5000, Remaining: 0, Reset: time.Now().UTC().Add(-time.Second),
	})

	availability := registry.CheckReserve(
		quotaTestUser, []QuotaResource{QuotaResourceREST}, 1, RateReserveBuffer,
	)

	assert.False(t, availability.Allowed)
	assert.False(t, availability.Known)
	assert.Nil(t, availability.ResetAt)
}

// TestQuotaTransportAttributesEachChainToItsBoundIdentity pins the split-auth
// attribution rule: a route's read chain spends its App installation pool
// while its mutation/notification chain spends the user's, so one credential's
// usage can never be billed to the other.
func TestQuotaTransportAttributesEachChainToItsBoundIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	registry := NewQuotaRegistry()
	respond := func(limit, remaining int) tokenauth.RoundTripFunc {
		return func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: quotaTestHeaders(
					limit, remaining, time.Now().UTC().Add(time.Hour),
				),
				Body:    io.NopCloser(strings.NewReader("{}")),
				Request: req,
			}, nil
		}
	}
	readChain := &quotaTransport{
		base: respond(15000, 14999), registry: registry,
		identity: quotaTestApp, resource: QuotaResourceREST,
	}
	writeChain := &quotaTransport{
		base: respond(5000, 4999), registry: registry,
		identity: quotaTestUser, resource: QuotaResourceREST,
	}

	readReq, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, "https://api.github.com/repos/acme/widget", nil,
	)
	require.NoError(err)
	_, err = readChain.RoundTrip(readReq)
	require.NoError(err)

	writeReq, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, "https://api.github.com/notifications", nil,
	)
	require.NoError(err)
	_, err = writeChain.RoundTrip(writeReq)
	require.NoError(err)

	app, ok := registry.Get(quotaTestApp, QuotaResourceREST)
	require.True(ok)
	user, ok := registry.Get(quotaTestUser, QuotaResourceREST)
	require.True(ok)
	assert.Equal(14999, app.Remaining)
	assert.Equal(4999, user.Remaining)
}

func quotaTestHeaders(limit, remaining int, reset time.Time) http.Header {
	return http.Header{
		"X-Ratelimit-Limit":     []string{strconv.Itoa(limit)},
		"X-Ratelimit-Remaining": []string{strconv.Itoa(remaining)},
		"X-Ratelimit-Reset":     []string{strconv.FormatInt(reset.Unix(), 10)},
	}
}

// An unknown resource must not hide a known-exhausted one. Background callers
// treat unknown quota as permission to proceed, so if REST is at its reserve
// while GraphQL has never been observed, admitting the work would spend REST
// past the reserve held for foreground mutations.
func TestQuotaAvailabilityUnknownResourceDoesNotMaskExhaustedResource(t *testing.T) {
	assert := assert.New(t)
	registry := NewQuotaRegistry()
	registry.UpdateSnapshot(quotaTestUser, QuotaResourceREST, Rate{
		Limit: 5000, Remaining: 200, Reset: time.Now().UTC().Add(time.Hour),
	})

	availability := registry.CheckReserve(
		quotaTestUser,
		[]QuotaResource{QuotaResourceREST, QuotaResourceGraphQL},
		1,
		200,
	)

	assert.False(availability.Allowed)
	assert.False(availability.Known, "the GraphQL pool has not been observed")
	assert.True(availability.Exhausted, "the REST pool is known to be at its reserve")
	assert.False(availability.AllowedOrUnobserved(),
		"an unobserved GraphQL pool must not admit work the REST reserve forbids")
}

// With nothing observed at all, background work still proceeds: response
// headers are what populate the registry in the first place.
func TestQuotaAvailabilityFullyUnobservedAllowsBackgroundWork(t *testing.T) {
	assert := assert.New(t)
	registry := NewQuotaRegistry()

	availability := registry.CheckReserve(
		quotaTestUser, []QuotaResource{QuotaResourceREST}, 1, 200,
	)

	assert.False(availability.Allowed)
	assert.False(availability.Known)
	assert.False(availability.Exhausted)
	assert.True(availability.AllowedOrUnobserved())
}

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

// The quota registry lives only in memory, so a restart starts it empty while
// the rate tracker rehydrates the same credential's pool from SQLite. Treating
// that as unobserved would admit background work against a reserve the
// persisted state already says is spent -- and the /rate_limit refresh that
// would repopulate the registry is exactly what fails when a credential is in
// trouble.
func TestBackgroundAdmissionUsesPersistedTrackerWhenRegistryIsEmpty(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	identity := IdentityKey{Host: "github.com", Principal: "user:7"}
	bucket := RateBucketKey("github", "github.com", "user:7")
	tracker := NewRateTracker(database, "github.com", "user:7", "rest")
	tracker.UpdateFromSnapshot(Rate{
		Limit: 5000, Remaining: RateReserveBuffer,
		Reset: time.Now().UTC().Add(time.Hour),
	})
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	router, err := NewHostRouter("github.com", &Route{
		Key: RouteKey{Host: "github.com", Owner: "acme"}, Client: &mockClient{},
		ReadIdentity: identity, WriteIdentity: identity,
	})
	require.NoError(err)
	syncer := &Syncer{
		routers:       map[string]*HostRouter{"github.com": router},
		rateTrackers:  map[string]*RateTracker{bucket: tracker},
		quotaRegistry: NewQuotaRegistry(),
	}

	assert.True(
		syncer.backgroundReserveExhausted(repo, QuotaResourceREST, false),
		"a persisted pool at its reserve must not read as unobserved")
}

// A persisted window that has already elapsed describes a quota that has since
// reset, so it must not hold back work the provider would now accept.
func TestBackgroundAdmissionIgnoresPersistedTrackerPastItsResetWindow(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	identity := IdentityKey{Host: "github.com", Principal: "user:7"}
	bucket := RateBucketKey("github", "github.com", "user:7")
	tracker := NewRateTracker(database, "github.com", "user:7", "rest")
	tracker.UpdateFromSnapshot(Rate{
		Limit: 5000, Remaining: RateReserveBuffer,
		Reset: time.Now().UTC().Add(time.Hour),
	})
	tracker.SetResetAtForTesting(time.Now().UTC().Add(-time.Minute))
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	router, err := NewHostRouter("github.com", &Route{
		Key: RouteKey{Host: "github.com", Owner: "acme"}, Client: &mockClient{},
		ReadIdentity: identity, WriteIdentity: identity,
	})
	require.NoError(err)
	syncer := &Syncer{
		routers:       map[string]*HostRouter{"github.com": router},
		rateTrackers:  map[string]*RateTracker{bucket: tracker},
		quotaRegistry: NewQuotaRegistry(),
	}

	assert.False(
		syncer.backgroundReserveExhausted(repo, QuotaResourceREST, false),
		"an elapsed persisted window cannot speak for the current quota")
}

// Concurrent responses complete out of order, so a header issued earlier can
// arrive after a later one. Within one reset window the provider's remaining
// only falls, and a stale header must not hand back quota already spent.
func TestQuotaRegistryHeadersNeverRaiseRemainingWithinAWindow(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	registry := NewQuotaRegistry()
	identity := IdentityKey{Host: "github.com", Principal: "user:7"}
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	header := func(remaining int, at time.Time) http.Header {
		h := http.Header{}
		h.Set("X-RateLimit-Limit", "5000")
		h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		h.Set("X-RateLimit-Reset", strconv.FormatInt(at.Unix(), 10))
		return h
	}

	registry.ObserveHeaders(identity, QuotaResourceREST, header(4000, reset))
	// The straggler was issued before the one above and reports more headroom.
	registry.ObserveHeaders(identity, QuotaResourceREST, header(4200, reset))

	pool, ok := registry.Get(identity, QuotaResourceREST)
	require.True(ok)
	assert.Equal(4000, pool.Remaining,
		"a late-arriving older header must not restore spent quota")

	// A new window is a different quota and replaces the pool outright.
	nextReset := reset.Add(time.Hour)
	registry.ObserveHeaders(identity, QuotaResourceREST, header(5000, nextReset))
	pool, ok = registry.Get(identity, QuotaResourceREST)
	require.True(ok)
	assert.Equal(5000, pool.Remaining)
	assert.True(pool.ResetAt.Equal(nextReset))
}

// A response from the previous reset window can arrive after a new-window
// response. Rewinding ResetAt to the old window would make the pool look
// expired -- and an expired pool reads as unobserved, which admits background
// work the newer observation had already ruled out.
func TestQuotaRegistryHeadersFromAnOlderWindowDoNotRewindThePool(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	registry := NewQuotaRegistry()
	identity := IdentityKey{Host: "github.com", Principal: "user:7"}
	oldReset := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	newReset := oldReset.Add(time.Hour)
	header := func(remaining int, at time.Time) http.Header {
		h := http.Header{}
		h.Set("X-RateLimit-Limit", "5000")
		h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		h.Set("X-RateLimit-Reset", strconv.FormatInt(at.Unix(), 10))
		return h
	}

	registry.ObserveHeaders(identity, QuotaResourceREST, header(300, newReset))
	// The straggler belongs to the window that has already closed.
	registry.ObserveHeaders(identity, QuotaResourceREST, header(4800, oldReset))

	pool, ok := registry.Get(identity, QuotaResourceREST)
	require.True(ok)
	assert.Equal(300, pool.Remaining,
		"a closed window must not overwrite the current one")
	assert.True(pool.ResetAt.Equal(newReset),
		"the pool must not rewind to a window that has already reset")
}

// A delayed /rate_limit response can land after the window it describes has
// closed. Rewinding to it would make the pool read as expired, and expired
// reads as unobserved.
func TestQuotaRegistrySnapshotFromAnOlderWindowDoesNotRewindThePool(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	registry := NewQuotaRegistry()
	identity := IdentityKey{Host: "github.com", Principal: "user:7"}
	oldReset := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	newReset := oldReset.Add(time.Hour)

	registry.UpdateSnapshot(identity, QuotaResourceREST, Rate{
		Limit: 5000, Remaining: 300, Reset: newReset,
	})
	registry.UpdateSnapshot(identity, QuotaResourceREST, Rate{
		Limit: 5000, Remaining: 4800, Reset: oldReset,
	})

	pool, ok := registry.Get(identity, QuotaResourceREST)
	require.True(ok)
	assert.Equal(300, pool.Remaining)
	assert.True(pool.ResetAt.Equal(newReset))
}

// The reserve is evaluated on the snapshot cadence, not per repository, per
// queue item, or per page. Inside one window every consumer answers from the
// same decision, so a pool that moves mid-window is not observed until the
// window turns.
func TestBackgroundReserveIsEvaluatedOncePerCadenceWindow(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	identity := IdentityKey{Host: "github.com", Principal: "user:7"}
	registry := NewQuotaRegistry()
	reset := time.Now().UTC().Add(time.Hour)
	registry.UpdateSnapshot(identity, QuotaResourceREST, Rate{
		Limit: 5000, Remaining: 4000, Reset: reset,
	})
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	router, err := NewHostRouter("github.com", &Route{
		Key: RouteKey{Host: "github.com", Owner: "acme"}, Client: &mockClient{},
		ReadIdentity: identity, WriteIdentity: identity,
	})
	require.NoError(err)
	clock := time.Now().UTC()
	syncer := &Syncer{
		routers:       map[string]*HostRouter{"github.com": router},
		quotaRegistry: registry,
		now:           func() time.Time { return clock },
	}

	assert.False(syncer.backgroundReserveExhausted(
		repo, QuotaResourceREST, false))

	registry.UpdateSnapshot(identity, QuotaResourceREST, Rate{
		Limit: 5000, Remaining: RateReserveBuffer, Reset: reset,
	})
	assert.False(
		syncer.backgroundReserveExhausted(repo, QuotaResourceREST, false),
		"the verdict holds for the window rather than being re-derived")

	clock = clock.Add(rateLimitSnapshotRefreshInterval)
	assert.True(
		syncer.backgroundReserveExhausted(repo, QuotaResourceREST, false),
		"the window turning is what picks up the new pool")
}

// A snapshot refresh replaces the numbers the cached verdict was computed from,
// so the refresh itself must drop the verdict rather than leave it to age out.
// This is what puts the reserve on the snapshot's cadence rather than merely on
// the same interval.
func TestSnapshotRefreshDropsTheCachedReserveVerdict(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	identity := IdentityKey{Host: "github.com", Principal: "user:7"}
	bucket := RateBucketKey("github", "github.com", "user:7")
	tracker := NewRateTracker(database, "github.com", "user:7", "rest")
	registry := NewQuotaRegistry()
	reset := time.Now().UTC().Add(time.Hour)
	registry.UpdateSnapshot(identity, QuotaResourceREST, Rate{
		Limit: 5000, Remaining: 4000, Reset: reset,
	})
	repo := RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"}
	// The refresh reports a credential that has reached its reserve.
	client := &credentialRateLimitSnapshotMockClient{
		mockClient: &mockClient{},
		appSnapshot: &RateLimitSnapshot{
			Core: &Rate{Limit: 5000, Remaining: RateReserveBuffer, Reset: reset},
		},
	}
	router, err := NewHostRouter("github.com", &Route{
		Key: RouteKey{Host: "github.com", Owner: "acme"}, Client: client,
		ReadIdentity: identity, WriteIdentity: identity,
	})
	require.NoError(err)
	syncer := &Syncer{
		clients:                  registryFromGitHubClients(map[string]Client{"github.com": client}),
		routers:                  map[string]*HostRouter{"github.com": router},
		rateTrackers:             map[string]*RateTracker{bucket: tracker},
		quotaRegistry:            registry,
		rateLimitSnapshotRefresh: make(map[string]time.Time),
		now:                      time.Now,
	}
	require.False(
		syncer.backgroundReserveExhausted(repo, QuotaResourceREST, false),
		"the credential starts with headroom",
	)

	syncer.RefreshRateLimitSnapshots(t.Context())

	assert.True(
		syncer.backgroundReserveExhausted(repo, QuotaResourceREST, false),
		"a fresh snapshot must take effect without waiting out the window")
}

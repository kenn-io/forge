package federation

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testHubID        = "0123456789abcdef0123456789abcdef"
	testNodeID       = "fedcba9876543210fedcba9876543210"
	testEnrollmentID = "11111111111111111111111111111111"
)

func TestEnrollmentActivationIsIdempotentAfterLostResponse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := newEnrollmentStoreForTest(t, func() time.Time { return now })
	token, err := store.CreateOneTimeToken(
		hubIdentityForTest(), now.Add(time.Minute),
	)
	require.NoError(err)
	request := joinRequestForTest(testEnrollmentID, testNodeID, "https://spoke-a.example")

	first, err := store.Begin(t.Context(), token.Token, request)
	require.NoError(err)
	_, err = store.Begin(t.Context(), token.Token, request)
	require.ErrorIs(err, ErrEnrollmentTokenConsumed)

	resumed, err := store.Resume(t.Context(), first.ID, request.NodeID)
	require.NoError(err)
	assert.Equal(first, resumed)
	require.NoError(store.Activate(t.Context(), first.ID, now.Add(time.Hour)))
	require.NoError(store.Activate(t.Context(), first.ID, now.Add(time.Hour)))
	_, err = store.Begin(t.Context(), token.Token, request)
	require.ErrorIs(err, ErrEnrollmentTokenConsumed)

	active, err := store.Resume(t.Context(), first.ID, request.NodeID)
	require.NoError(err)
	assert.Equal(EnrollmentActive, active.State)
	assert.Equal(ActivationLeaseVersion, active.ActivationLeaseVersion)
	assert.Equal(now.Add(time.Hour), active.ActivationValidUntil)
	_, err = store.Begin(t.Context(), token.Token,
		joinRequestForTest("22222222222222222222222222222222",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "https://spoke-b.example"))
	assert.ErrorIs(err, ErrEnrollmentTokenConsumed)
}

func TestEnrollmentCleanupPinsPreparationButRemovesUnstartedPending(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := newEnrollmentStoreForTest(t, func() time.Time { return now })

	removableToken, err := store.CreateOneTimeToken(
		hubIdentityForTest(), now.Add(time.Minute),
	)
	require.NoError(err)
	removable, err := store.Begin(t.Context(), removableToken.Token,
		joinRequestForTest(testEnrollmentID, testNodeID, "https://spoke-a.example"))
	require.NoError(err)

	pinnedToken, err := store.CreateOneTimeToken(
		hubIdentityForTest(), now.Add(time.Minute),
	)
	require.NoError(err)
	pinned, err := store.Begin(t.Context(), pinnedToken.Token, joinRequestForTest(
		"22222222222222222222222222222222",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "https://spoke-b.example",
	))
	require.NoError(err)
	require.NoError(store.MarkPreparationStarted(t.Context(), pinned.ID))

	now = now.Add(2 * time.Minute)
	removed, err := store.CleanupExpired(t.Context())
	require.NoError(err)
	assert.Equal(1, removed)
	_, err = store.Resume(t.Context(), removable.ID, removable.NodeID)
	require.ErrorIs(err, ErrEnrollmentNotFound)
	persisted, err := store.Resume(t.Context(), pinned.ID, pinned.NodeID)
	require.NoError(err)
	assert.True(persisted.PreparationStarted)
}

func TestEnrollmentRekeysSameQuiescingRecord(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := newEnrollmentStoreForTest(t, func() time.Time { return now })
	request := joinRequestForTest(testEnrollmentID, testNodeID, "https://spoke-a.example")

	firstToken, err := store.CreateOneTimeToken(
		hubIdentityForTest(), now.Add(time.Minute),
	)
	require.NoError(err)
	first, err := store.Begin(t.Context(), firstToken.Token, request)
	require.NoError(err)
	require.NoError(store.MarkPreparationStarted(t.Context(), first.ID))

	now = now.Add(2 * time.Minute)
	secondToken, err := store.CreateOneTimeToken(
		hubIdentityForTest(), now.Add(time.Minute),
	)
	require.NoError(err)
	rekeyed, err := store.Begin(t.Context(), secondToken.Token, request)
	require.NoError(err)
	assert.Equal(first.ID, rekeyed.ID)
	assert.True(rekeyed.PreparationStarted)
	assert.Equal(now.Add(time.Minute), rekeyed.ExpiresAt)
}

func TestEnrollmentRejectsDuplicateSpokeAtDifferentOriginAndProtocolMismatch(t *testing.T) {
	require := require.New(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := newEnrollmentStoreForTest(t, func() time.Time { return now })
	token, err := store.CreateOneTimeToken(
		hubIdentityForTest(), now.Add(time.Minute),
	)
	require.NoError(err)
	_, err = store.Begin(t.Context(), token.Token,
		joinRequestForTest(testEnrollmentID, testNodeID, "https://NODE-A.example:443/"))
	require.NoError(err)

	otherToken, err := store.CreateOneTimeToken(
		hubIdentityForTest(), now.Add(time.Minute),
	)
	require.NoError(err)
	_, err = store.Begin(t.Context(), otherToken.Token, joinRequestForTest(
		"22222222222222222222222222222222", testNodeID,
		"https://spoke-b.example",
	))
	require.ErrorIs(err, ErrDuplicateNodeID)

	badProtocol := joinRequestForTest(
		"33333333333333333333333333333333",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "https://spoke-c.example",
	)
	badProtocol.ProtocolVersion = ProtocolVersion - 1
	_, err = store.Begin(t.Context(), otherToken.Token, badProtocol)
	require.ErrorIs(err, ErrProtocolMismatch)
}

func TestEnrollmentRejectsHubIdentityAsSpoke(t *testing.T) {
	require := require.New(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := newEnrollmentStoreForTest(t, func() time.Time { return now })
	token, err := store.CreateOneTimeToken(
		hubIdentityForTest(), now.Add(time.Minute),
	)
	require.NoError(err)

	_, err = store.Begin(t.Context(), token.Token, joinRequestForTest(
		testEnrollmentID, testHubID, "https://spoke-a.example",
	))

	require.ErrorIs(err, ErrEnrollmentConflict)
	require.Empty(store.List())
}

func TestEnrollmentRevocationPersistsAcrossReload(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "enrollments.json")
	store, err := Open(path, StoreOptions{Now: func() time.Time { return now }})
	require.NoError(err)
	token, err := store.CreateOneTimeToken(
		hubIdentityForTest(), now.Add(time.Minute),
	)
	require.NoError(err)
	enrollment, err := store.Begin(t.Context(), token.Token,
		joinRequestForTest(testEnrollmentID, testNodeID, "https://spoke-a.example"))
	require.NoError(err)
	require.NoError(store.Revoke(t.Context(), enrollment.ID))

	reopened, err := Open(path, StoreOptions{Now: func() time.Time { return now }})
	require.NoError(err)
	revoked, err := reopened.Resume(t.Context(), enrollment.ID, enrollment.NodeID)
	require.NoError(err)
	assert.Equal(EnrollmentRevoked, revoked.State)
	assert.False(revoked.RevokedAt.IsZero())
}

func TestEnrollmentForSpokeReturnsReplacementAfterRevocation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := newEnrollmentStoreForTest(t, func() time.Time { return now })
	firstToken, err := store.CreateOneTimeToken(
		hubIdentityForTest(), now.Add(time.Minute),
	)
	require.NoError(err)
	first, err := store.Begin(t.Context(), firstToken.Token,
		joinRequestForTest(testEnrollmentID, testNodeID, "https://spoke-a.example"))
	require.NoError(err)
	require.NoError(store.Revoke(t.Context(), first.ID))

	secondToken, err := store.CreateOneTimeToken(
		hubIdentityForTest(), now.Add(time.Minute),
	)
	require.NoError(err)
	second, err := store.Begin(t.Context(), secondToken.Token, joinRequestForTest(
		"00000000000000000000000000000000", testNodeID, "https://spoke-a.example",
	))
	require.NoError(err)

	current, ok := store.EnrollmentForSpoke(testNodeID)
	require.True(ok)
	assert.Equal(second.ID, current.ID)
	assert.Equal(EnrollmentPending, current.State)
}

func TestCanonicalOriginNormalizesOnlyHTTPSOrigins(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	got, err := CanonicalOrigin("HTTPS://Forge.Example:443/")
	require.NoError(err)
	assert.Equal("https://forge.example", got)
	got, err = CanonicalOrigin("https://[2001:DB8::1]:8443")
	require.NoError(err)
	assert.Equal("https://[2001:db8::1]:8443", got)

	for _, invalid := range []string{
		"http://forge.example", "https://user@forge.example",
		"https://forge.example/path", "https://forge.example?q=1",
		"https://forge.example?", "https://forge.example#fragment", "forge.example",
	} {
		_, err := CanonicalOrigin(invalid)
		assert.Error(err, invalid)
	}
}

func TestLocalPendingEnrollmentRoundTripsWithoutCredentialMaterial(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	path := filepath.Join(t.TempDir(), "enrollments.json")
	store, err := Open(path, StoreOptions{})
	require.NoError(err)
	pending := LocalEnrollment{
		EnrollmentID:    testEnrollmentID,
		NodeID:          testNodeID,
		SpokeName:       "Build Box",
		SpokePlatform:   "linux",
		SpokeBaseURL:    "https://spoke-a.example",
		HubURL:          "https://hub.example",
		ProtocolVersion: ProtocolVersion,
		State:           EnrollmentPending,
		ExpiresAt:       time.Now().Add(time.Minute).UTC(),
	}
	require.NoError(store.SaveLocal(t.Context(), pending))

	reopened, err := Open(path, StoreOptions{})
	require.NoError(err)
	got, ok := reopened.Local()
	require.True(ok)
	assert.Equal(pending, got)
	require.NoError(reopened.ClearLocal(t.Context()))
	_, ok = reopened.Local()
	assert.False(ok)
}

func TestLocalPreparationSealIsBoundAndActivationIsIdempotent(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	path := filepath.Join(t.TempDir(), "enrollments.json")
	store, err := Open(path, StoreOptions{})
	require.NoError(err)
	local := LocalEnrollment{
		EnrollmentID: testEnrollmentID, NodeID: testNodeID,
		SpokePlatform: "linux", SpokeBaseURL: "https://spoke-a.example",
		HubID:           testHubID,
		HubURL:          "https://hub.example",
		ProtocolVersion: ProtocolVersion, State: EnrollmentPending,
		ExpiresAt: time.Now().Add(time.Minute), PreparationRequired: true,
	}
	require.NoError(store.SaveLocal(t.Context(), local))
	require.NoError(store.MarkLocalPreparationStarted(t.Context(), testEnrollmentID))
	seal := LocalPreparationSeal{
		EnrollmentID: testEnrollmentID, NodeID: testNodeID,
		HubID: testHubID, ProtocolVersion: ProtocolVersion,
		PreparationDigest: "digest", Seal: "opaque-seal",
	}
	require.NoError(store.SaveLocalPreparationSeal(t.Context(), seal))
	require.NoError(store.SaveLocalPreparationSeal(t.Context(), seal))

	foreign := seal
	foreign.HubID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	require.ErrorIs(store.SaveLocalPreparationSeal(t.Context(), foreign), ErrPreparationSealMismatch)

	reopened, err := Open(path, StoreOptions{})
	require.NoError(err)
	got, ok := reopened.Local()
	require.True(ok)
	require.NotNil(got.Preparation)
	assert.Equal(seal, *got.Preparation)
	got.Preparation.Seal = "caller-mutated-copy"
	detached, ok := reopened.Local()
	require.True(ok)
	require.NotNil(detached.Preparation)
	assert.Equal(seal.Seal, detached.Preparation.Seal)
	activationValidUntil := time.Now().Add(time.Hour)
	require.NoError(reopened.MarkLocalActive(
		t.Context(), testEnrollmentID, activationValidUntil,
	))
	require.NoError(reopened.MarkLocalActive(
		t.Context(), testEnrollmentID, activationValidUntil,
	))
	got, ok = reopened.Local()
	require.True(ok)
	assert.Equal(EnrollmentActive, got.State)
	assert.False(got.PreparationRequired)
	assert.Equal(ActivationLeaseVersion, got.ActivationLeaseVersion)
	assert.Equal(activationValidUntil.UTC(), got.ActivationValidUntil)
	renewedUntil := activationValidUntil.Add(time.Hour)
	require.NoError(reopened.RenewLocalActivationLease(
		t.Context(), testEnrollmentID, renewedUntil,
	))
	got, ok = reopened.Local()
	require.True(ok)
	assert.Equal(renewedUntil.UTC(), got.ActivationValidUntil)
	require.NoError(reopened.InvalidateLocalActivationLease(
		t.Context(), testEnrollmentID,
	))
	got, ok = reopened.Local()
	require.True(ok)
	assert.True(got.ActivationValidUntil.IsZero())
}

func TestLocalActivationCannotResumeAfterRevocation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	store := newEnrollmentStoreForTest(t, time.Now)
	require.NoError(store.SaveLocal(t.Context(), LocalEnrollment{
		EnrollmentID: testEnrollmentID, NodeID: testNodeID,
		SpokePlatform: "linux", SpokeBaseURL: "https://spoke-a.example",
		HubID: testHubID, HubURL: "https://hub.example",
		ProtocolVersion: ProtocolVersion, State: EnrollmentActive,
		ExpiresAt:              time.Now().Add(time.Hour),
		PreparationStarted:     true,
		ActivationLeaseVersion: ActivationLeaseVersion,
		ActivationValidUntil:   time.Now().Add(time.Hour),
		Preparation: &LocalPreparationSeal{
			EnrollmentID: testEnrollmentID, NodeID: testNodeID,
			HubID: testHubID, ProtocolVersion: ProtocolVersion,
			PreparationDigest: "digest", Seal: "opaque-seal",
		},
	}))
	local, ok := store.Local()
	require.True(ok)
	local.State = EnrollmentRevoked
	require.NoError(store.SaveLocal(t.Context(), local))

	validUntil := time.Now().Add(2 * time.Hour)
	require.ErrorIs(
		store.MarkLocalActive(t.Context(), testEnrollmentID, validUntil),
		ErrEnrollmentRevoked,
	)
	require.ErrorIs(
		store.RenewLocalActivationLease(t.Context(), testEnrollmentID, validUntil),
		ErrEnrollmentRevoked,
	)
	local, ok = store.Local()
	require.True(ok)
	assert.Equal(EnrollmentRevoked, local.State)
}

func newEnrollmentStoreForTest(t *testing.T, now func() time.Time) *Store {
	t.Helper()
	store, err := Open(
		filepath.Join(t.TempDir(), "enrollments.json"), StoreOptions{Now: now},
	)
	require.NoError(t, err)
	return store
}

func hubIdentityForTest() Identity {
	return Identity{
		NodeID: testHubID,
		Name:   "Studio", BaseURL: "https://HUB.example:443/",
	}
}

func joinRequestForTest(enrollmentID, nodeID, baseURL string) JoinRequest {
	return JoinRequest{
		EnrollmentID: enrollmentID,
		NodeID:       nodeID, Name: "Build Box", Platform: "linux",
		BaseURL: baseURL, ProtocolVersion: ProtocolVersion,
		HubCredential: "hub-calls-spoke-token",
	}
}

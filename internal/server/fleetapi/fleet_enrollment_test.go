package fleetapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

const (
	enrollmentHubID     = "0123456789abcdef0123456789abcdef"
	enrollmentNodeID    = "fedcba9876543210fedcba9876543210"
	enrollmentRequestID = "11111111111111111111111111111111"
)

type enrollmentHandlerFixture struct {
	handler     *Handler
	credentials *federationauth.Store
	enrollments *federation.Store
	database    *db.DB
	mux         http.Handler
}

func TestEnrollmentTokenUsesActiveHubOrigin(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	const hubURL = "https://hub.example:8443"
	fixture := newEnrollmentHandlerFixture(t, enrollmentHubID, func(deps *Deps) {
		deps.Config.Fleet.BaseURL = hubURL
	})
	server := httptest.NewServer(fixture.mux)
	t.Cleanup(server.Close)
	body := map[string]any{
		"name": "Hub", "expires_in_seconds": 600,
	}

	body["base_url"] = "https://other.example"
	rejected := doEnrollmentJSON(
		t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/fleet/enrollment-tokens", body, "",
	)
	assert.Equal(http.StatusBadRequest, rejected.StatusCode)
	rejected.Body.Close()

	body["base_url"] = hubURL
	accepted := doEnrollmentJSON(
		t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/fleet/enrollment-tokens", body, "",
	)
	require.Equal(http.StatusCreated, accepted.StatusCode)
	var token federation.EnrollmentToken
	require.NoError(json.NewDecoder(accepted.Body).Decode(&token))
	accepted.Body.Close()
	assert.Equal(hubURL, token.HubURL)
}

func TestEnrollmentHTTPExchangeConsumesTokenAndIsDirectional(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newEnrollmentHandlerFixture(t, enrollmentHubID, nil)
	server := httptest.NewTLSServer(fixture.mux)
	t.Cleanup(server.Close)
	now := time.Now().UTC()
	oneTime, err := fixture.enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: enrollmentHubID, Name: "Studio", BaseURL: server.URL,
	}, now.Add(time.Minute))
	require.NoError(err)

	request := federation.JoinRequest{
		EnrollmentID: enrollmentRequestID,
		NodeID:       enrollmentNodeID, Name: "Build Box", Platform: "linux",
		BaseURL: "https://spoke.example", ProtocolVersion: federation.ProtocolVersion,
		HubCredential: "hub-calls-spoke-token",
	}
	first := postEnrollmentRequest(t, server.Client(), server.URL, oneTime.Token, request)
	require.Equal(http.StatusCreated, first.StatusCode)
	var firstResponse federation.JoinResponse
	require.NoError(json.NewDecoder(first.Body).Decode(&firstResponse))
	first.Body.Close()
	assert.Equal(enrollmentRequestID, firstResponse.EnrollmentID)
	assert.Equal(enrollmentHubID, firstResponse.HubID)
	assert.True(firstResponse.PreparationRequired)

	outbound, ok := fixture.credentials.Outbound(enrollmentNodeID)
	require.True(ok)
	assert.Equal(request.HubCredential, outbound.Token)
	assert.Equal(federationauth.PendingHubToSpokeScopes(), outbound.Scopes)
	principal, ok := fixture.credentials.Authenticate(firstResponse.SpokeCredential)
	require.True(ok)
	assert.Equal(enrollmentNodeID, principal.NodeID)
	assert.Equal(scopeSetForEnrollmentTest(federationauth.PendingSpokeToHubScopes()), principal.Scopes)

	retry := postEnrollmentRequest(t, server.Client(), server.URL, oneTime.Token, request)
	require.Equal(http.StatusConflict, retry.StatusCode)
	retry.Body.Close()
	_, ok = fixture.credentials.Authenticate(firstResponse.SpokeCredential)
	assert.True(ok, "replaying a consumed token must not rotate the credential")

	different := request
	different.EnrollmentID = "22222222222222222222222222222222"
	different.NodeID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	different.BaseURL = "https://other.example"
	rejected := postEnrollmentRequest(t, server.Client(), server.URL, oneTime.Token, different)
	assert.Equal(http.StatusConflict, rejected.StatusCode)
	rejected.Body.Close()
}

func TestEnrollmentHTTPRejectsOversizedInputBeforePersistence(t *testing.T) {
	tests := []struct {
		name       string
		credential string
		wantStatus int
	}{
		{
			name: "credential field", credential: strings.Repeat("x", federation.MaxCredentialLength+1),
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "request body", credential: strings.Repeat("x", maxFederationEnrollmentRequestBytes),
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fixture := newEnrollmentHandlerFixture(t, enrollmentHubID, nil)
			server := httptest.NewTLSServer(fixture.mux)
			t.Cleanup(server.Close)
			oneTime, err := fixture.enrollments.CreateOneTimeToken(federation.Identity{
				NodeID: enrollmentHubID, Name: "Hub", BaseURL: server.URL,
			}, time.Now().Add(time.Minute))
			require.NoError(err)
			request := federation.JoinRequest{
				EnrollmentID: enrollmentRequestID,
				NodeID:       enrollmentNodeID, Name: "Spoke", Platform: "linux",
				BaseURL: "https://spoke.example", ProtocolVersion: federation.ProtocolVersion,
				HubCredential: tt.credential,
			}

			response := postEnrollmentRequest(t, server.Client(), server.URL, oneTime.Token, request)
			assert.Equal(tt.wantStatus, response.StatusCode)
			response.Body.Close()
			assert.Empty(fixture.enrollments.List())
			_, persisted := fixture.credentials.Outbound(enrollmentNodeID)
			assert.False(persisted)
		})
	}
}

func TestFleetJoinPersistsBothCredentialDirectionsAndHubBinding(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hub := newEnrollmentHandlerFixture(t, enrollmentHubID, nil)
	hubServer := httptest.NewTLSServer(hub.mux)
	t.Cleanup(hubServer.Close)
	oneTime, err := hub.enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: enrollmentHubID, Name: "Studio",
		BaseURL: hubServer.URL,
	}, time.Now().Add(time.Minute))
	require.NoError(err)

	var binding config.FleetHub
	spoke := newEnrollmentHandlerFixture(t, enrollmentNodeID, func(deps *Deps) {
		deps.FederationHTTPClient = hubServer.Client()
		require.NoError(deps.Enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
			EnrollmentID: enrollmentRequestID, NodeID: enrollmentNodeID,
			SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
			HubURL:          hubServer.URL,
			ProtocolVersion: federation.ProtocolVersion,
			State:           federation.EnrollmentPending, PreparationRequired: true,
		}))
		deps.PersistHubBinding = func(
			_ context.Context, got config.FleetHub,
		) error {
			binding = got
			return nil
		}
	})
	nodeServer := httptest.NewServer(spoke.mux)
	t.Cleanup(nodeServer.Close)
	body := map[string]any{
		"hub_base_url":     hubServer.URL,
		"spoke_base_url":   "https://spoke.example",
		"name":             "Build Box",
		"enrollment_token": oneTime.Token,
	}
	response := doEnrollmentJSON(
		t, nodeServer.Client(), http.MethodPost,
		nodeServer.URL+"/api/v1/fleet/join", body, "",
	)
	require.Equal(http.StatusOK, response.StatusCode)
	var local federation.LocalEnrollment
	require.NoError(json.NewDecoder(response.Body).Decode(&local))
	response.Body.Close()
	assert.Equal(enrollmentHubID, local.HubID)
	assert.Equal(enrollmentRequestID, local.EnrollmentID)
	assert.Equal(federation.EnrollmentPending, local.State)
	assert.Equal(oneTime.ExpiresAt, local.ExpiresAt)
	assert.True(local.PreparationRequired)
	assert.Equal(enrollmentHubID, binding.NodeID)
	assert.Equal(hubServer.URL, binding.BaseURL)

	hubOutbound, ok := hub.credentials.Outbound(enrollmentNodeID)
	require.True(ok)
	principal, ok := spoke.credentials.Authenticate(hubOutbound.Token)
	require.True(ok)
	assert.Equal(enrollmentHubID, principal.NodeID)
	assert.Equal(
		scopeSetForEnrollmentTest(federationauth.PendingHubToSpokeScopes()),
		principal.Scopes,
	)
	nodeOutbound, ok := spoke.credentials.Outbound(enrollmentHubID)
	require.True(ok)
	principal, ok = hub.credentials.Authenticate(nodeOutbound.Token)
	require.True(ok)
	assert.Equal(enrollmentNodeID, principal.NodeID)

	persisted, ok := spoke.enrollments.Local()
	require.True(ok)
	assert.Equal(local, persisted)
}

func TestSendJoinRequestDoesNotRetryAmbiguousTransportFailure(t *testing.T) {
	var attempts int
	handler := &Handler{
		federationHTTPClient: &http.Client{Transport: roundTripFunc(func(
			*http.Request,
		) (*http.Response, error) {
			attempts++
			return nil, errors.New("response unavailable")
		})},
	}

	_, err := handler.sendJoinRequest(
		t.Context(), "https://hub.example", "one-time-token",
		federation.JoinRequest{},
	)

	var problem *httpapi.ProblemError
	require.ErrorAs(t, err, &problem)
	assert.Equal(t, http.StatusServiceUnavailable, problem.Status)
	assert.Equal(t, 1, attempts)
}

func TestFleetJoinReplacesRevokedLocalEnrollment(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	hub := newEnrollmentHandlerFixture(t, enrollmentHubID, nil)
	hubServer := httptest.NewTLSServer(hub.mux)
	t.Cleanup(hubServer.Close)
	token, err := hub.enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: enrollmentHubID, BaseURL: hubServer.URL,
	}, time.Now().Add(time.Minute))
	require.NoError(err)
	spoke := newEnrollmentHandlerFixture(t, enrollmentNodeID, func(deps *Deps) {
		deps.FederationHTTPClient = hubServer.Client()
		deps.PersistHubBinding = func(context.Context, config.FleetHub) error { return nil }
		require.NoError(deps.Enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
			EnrollmentID: enrollmentRequestID, NodeID: enrollmentNodeID,
			SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
			HubID: enrollmentHubID, HubURL: hubServer.URL,
			ProtocolVersion: federation.ProtocolVersion, State: federation.EnrollmentRevoked,
			ExpiresAt: time.Now().Add(time.Hour),
		}))
		require.NoError(deps.Credentials.StoreInbound(
			enrollmentHubID, "revocation-only-token",
			[]federationauth.Scope{federationauth.ScopeEnrollmentActivate},
		))
	})
	spokeServer := httptest.NewServer(spoke.mux)
	t.Cleanup(spokeServer.Close)

	response := doEnrollmentJSON(
		t, spokeServer.Client(), http.MethodPost,
		spokeServer.URL+"/api/v1/fleet/join", map[string]any{
			"hub_base_url": hubServer.URL, "spoke_base_url": "https://spoke.example",
			"enrollment_token": token.Token,
		}, "",
	)
	require.Equal(http.StatusOK, response.StatusCode)
	var local federation.LocalEnrollment
	require.NoError(json.NewDecoder(response.Body).Decode(&local))
	response.Body.Close()
	assert.NotEqual(enrollmentRequestID, local.EnrollmentID)
	assert.Equal(federation.EnrollmentPending, local.State)
	_, authenticated := spoke.credentials.Authenticate("revocation-only-token")
	assert.False(authenticated)
}

func TestFleetJoinReusesUnboundProvisionalAfterLostAcceptedResponse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hub := newEnrollmentHandlerFixture(t, enrollmentHubID, nil)
	hubServer := httptest.NewTLSServer(hub.mux)
	t.Cleanup(hubServer.Close)
	identity := federation.Identity{
		NodeID: enrollmentHubID, Name: "Studio",
		BaseURL: hubServer.URL,
	}
	oneTime, err := hub.enrollments.CreateOneTimeToken(
		identity, time.Now().Add(time.Minute),
	)
	require.NoError(err)

	hubTransport := hubServer.Client().Transport
	dropResponse := true
	spoke := newEnrollmentHandlerFixture(t, enrollmentNodeID, func(deps *Deps) {
		deps.PersistHubBinding = func(context.Context, config.FleetHub) error {
			return nil
		}
		deps.FederationHTTPClient = &http.Client{Transport: roundTripFunc(func(
			request *http.Request,
		) (*http.Response, error) {
			response, err := hubTransport.RoundTrip(request)
			if err != nil || !dropResponse {
				return response, err
			}
			dropResponse = false
			response.Body.Close()
			return nil, errors.New("hub response lost")
		})}
	})
	nodeServer := httptest.NewServer(spoke.mux)
	t.Cleanup(nodeServer.Close)

	failed := doEnrollmentJSON(
		t, nodeServer.Client(), http.MethodPost,
		nodeServer.URL+"/api/v1/fleet/join", map[string]any{
			"hub_base_url":     hubServer.URL,
			"spoke_base_url":   "https://spoke.example",
			"enrollment_token": oneTime.Token,
		}, "",
	)
	require.Equal(http.StatusServiceUnavailable, failed.StatusCode)
	failed.Body.Close()
	provisional, ok := spoke.enrollments.Local()
	require.True(ok)
	assert.Empty(provisional.HubID)
	accepted, err := hub.enrollments.Get(t.Context(), provisional.EnrollmentID)
	require.NoError(err)
	assert.Equal(federation.EnrollmentPending, accepted.State)
	replacementToken, err := hub.enrollments.CreateOneTimeToken(
		identity, time.Now().Add(time.Minute),
	)
	require.NoError(err)

	joined := doEnrollmentJSON(
		t, nodeServer.Client(), http.MethodPost,
		nodeServer.URL+"/api/v1/fleet/join", map[string]any{
			"hub_base_url":     hubServer.URL,
			"spoke_base_url":   "https://spoke.example",
			"enrollment_token": replacementToken.Token,
		}, "",
	)
	require.Equal(http.StatusOK, joined.StatusCode)
	var local federation.LocalEnrollment
	require.NoError(json.NewDecoder(joined.Body).Decode(&local))
	joined.Body.Close()
	assert.Equal(provisional.EnrollmentID, local.EnrollmentID)
	assert.Equal(enrollmentHubID, local.HubID)
	_, err = hub.enrollments.Get(t.Context(), provisional.EnrollmentID)
	assert.NoError(err)
}

func TestFleetJoinPreservesCompletedOrPreparedLocalEnrollment(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name  string
		local federation.LocalEnrollment
	}{
		{
			name: "active",
			local: federation.LocalEnrollment{
				EnrollmentID: enrollmentRequestID, NodeID: enrollmentNodeID,
				SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
				HubID:           enrollmentHubID,
				HubURL:          "https://hub.example",
				ProtocolVersion: federation.ProtocolVersion,
				State:           federation.EnrollmentActive, ExpiresAt: now.Add(time.Hour),
			},
		},
		{
			name: "prepared",
			local: federation.LocalEnrollment{
				EnrollmentID: enrollmentRequestID, NodeID: enrollmentNodeID,
				SpokePlatform: "linux", SpokeBaseURL: "https://spoke.example",
				HubID:           enrollmentHubID,
				HubURL:          "https://hub.example",
				ProtocolVersion: federation.ProtocolVersion,
				State:           federation.EnrollmentPending, ExpiresAt: now.Add(time.Hour),
				PreparationStarted: true, PreparationRequired: true,
				Preparation: &federation.LocalPreparationSeal{
					EnrollmentID: enrollmentRequestID, NodeID: enrollmentNodeID,
					HubID:             enrollmentHubID,
					ProtocolVersion:   federation.ProtocolVersion,
					PreparationDigest: "digest", Seal: "sealed-proof",
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			requests := 0
			spoke := newEnrollmentHandlerFixture(t, enrollmentNodeID, func(deps *Deps) {
				require.NoError(deps.Enrollments.SaveLocal(t.Context(), test.local))
				deps.FederationHTTPClient = &http.Client{Transport: roundTripFunc(func(
					*http.Request,
				) (*http.Response, error) {
					requests++
					return &http.Response{StatusCode: http.StatusInternalServerError, Body: http.NoBody}, nil
				})}
			})
			nodeServer := httptest.NewServer(spoke.mux)
			t.Cleanup(nodeServer.Close)

			response := doEnrollmentJSON(
				t, nodeServer.Client(), http.MethodPost,
				nodeServer.URL+"/api/v1/fleet/join", map[string]any{
					"hub_base_url":     "https://hub.example",
					"spoke_base_url":   "https://spoke.example",
					"enrollment_token": "replacement-token",
				}, "",
			)
			assert.Equal(http.StatusConflict, response.StatusCode)
			response.Body.Close()
			assert.Zero(requests)
			persisted, ok := spoke.enrollments.Local()
			require.True(ok)
			assert.Equal(test.local, persisted)
		})
	}
}

func TestFleetJoinRejectsHubWithMembers(t *testing.T) {
	requests := 0
	spoke := newEnrollmentHandlerFixture(t, enrollmentNodeID, func(deps *Deps) {
		deps.Config.Fleet.Members = []config.FleetMember{{
			NodeID:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			BaseURL: "https://member.example", State: federation.EnrollmentActive,
		}}
		deps.FederationHTTPClient = &http.Client{Transport: roundTripFunc(func(
			*http.Request,
		) (*http.Response, error) {
			requests++
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: http.NoBody}, nil
		})}
	})
	nodeServer := httptest.NewServer(spoke.mux)
	t.Cleanup(nodeServer.Close)

	response := doEnrollmentJSON(
		t, nodeServer.Client(), http.MethodPost,
		nodeServer.URL+"/api/v1/fleet/join", map[string]any{
			"hub_base_url":     "https://hub.example",
			"spoke_base_url":   "https://spoke.example",
			"enrollment_token": "replacement-token",
		}, "",
	)
	assert.Equal(t, http.StatusConflict, response.StatusCode)
	response.Body.Close()
	assert.Zero(t, requests)
}

func TestEnrollmentActivationRequiresPreparationAndIsIdempotent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var persisted []config.FleetMember
	fixture := newEnrollmentHandlerFixture(t, enrollmentHubID, func(deps *Deps) {
		deps.PersistMember = func(_ context.Context, member config.FleetMember) error {
			persisted = append(persisted, member)
			return nil
		}
	})
	server := httptest.NewTLSServer(authenticatedEnrollmentHandler(fixture))
	t.Cleanup(server.Close)
	oneTime, err := fixture.enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: enrollmentHubID, BaseURL: server.URL,
	}, time.Now().Add(time.Minute))
	require.NoError(err)
	joinRequest := federation.JoinRequest{
		EnrollmentID: enrollmentRequestID, NodeID: enrollmentNodeID,
		Platform: "linux", BaseURL: "https://spoke.example",
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   "hub-calls-spoke-token",
	}
	joined := postEnrollmentRequest(t, server.Client(), server.URL, oneTime.Token, joinRequest)
	require.Equal(http.StatusCreated, joined.StatusCode)
	var joinResponse federation.JoinResponse
	require.NoError(json.NewDecoder(joined.Body).Decode(&joinResponse))
	joined.Body.Close()

	preparationSeal := ""
	activate := func() *http.Response {
		return doEnrollmentJSON(
			t, server.Client(), http.MethodPost,
			server.URL+"/api/v1/federation/enrollments/"+enrollmentRequestID+"/activate",
			map[string]any{
				"protocol_version": federation.ProtocolVersion,
				"preparation_seal": preparationSeal,
			},
			joinResponse.SpokeCredential,
		)
	}
	blocked := activate()
	assert.Equal(http.StatusConflict, blocked.StatusCode)
	blocked.Body.Close()
	sealRequest := db.SpokePreparationSealRequest{
		EnrollmentID: enrollmentRequestID, NodeID: enrollmentNodeID,
		HubNodeID:        enrollmentHubID,
		ProtocolVersion:  federation.ProtocolVersion,
		MigrationVersion: db.WorkspaceLaunchSpecMigrationVersion,
		ReceiptsDigest:   "receipts", DrainedAckGeneration: 1,
	}
	sealRequest.PreparationDigest, err = db.SpokePreparationSealDigest(sealRequest)
	require.NoError(err)
	seal, err := fixture.database.IssueSpokePreparationSeal(t.Context(), sealRequest)
	require.NoError(err)
	preparationSeal = seal.Seal

	active := activate()
	require.Equal(http.StatusOK, active.StatusCode)
	var enrollment federation.Enrollment
	require.NoError(json.NewDecoder(active.Body).Decode(&enrollment))
	active.Body.Close()
	assert.Equal(federation.EnrollmentActive, enrollment.State)
	require.Len(persisted, 1)
	assert.Equal(enrollmentNodeID, persisted[0].NodeID)

	retried := activate()
	assert.Equal(http.StatusOK, retried.StatusCode)
	retried.Body.Close()
	assert.Len(persisted, 1, "an already-active retry does not persist membership twice")

	replayedJoin := postEnrollmentRequest(
		t, server.Client(), server.URL, oneTime.Token, joinRequest,
	)
	assert.Equal(http.StatusConflict, replayedJoin.StatusCode)
	replayedJoin.Body.Close()
	principal, ok := fixture.credentials.Authenticate(joinResponse.SpokeCredential)
	require.True(ok, "replaying a consumed token must not rotate the active credential")
	assert.Equal(
		scopeSetForEnrollmentTest(federationauth.SpokeToHubScopes()),
		principal.Scopes,
	)
	outbound, ok := fixture.credentials.Outbound(enrollmentNodeID)
	require.True(ok)
	assert.Equal(federationauth.HubToSpokeScopes(), outbound.Scopes)
}

func TestSpokePreparationSealIsEnrollmentBoundAndRetrySafe(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newEnrollmentHandlerFixture(t, enrollmentHubID, nil)
	server := httptest.NewTLSServer(authenticatedEnrollmentHandler(fixture))
	t.Cleanup(server.Close)
	oneTime, err := fixture.enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: enrollmentHubID, BaseURL: server.URL,
	}, time.Now().Add(time.Minute))
	require.NoError(err)
	joinRequest := federation.JoinRequest{
		EnrollmentID: enrollmentRequestID, NodeID: enrollmentNodeID,
		Platform: "linux", BaseURL: "https://spoke.example",
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   "hub-calls-spoke-token",
	}
	joined := postEnrollmentRequest(t, server.Client(), server.URL, oneTime.Token, joinRequest)
	require.Equal(http.StatusCreated, joined.StatusCode)
	var joinResponse federation.JoinResponse
	require.NoError(json.NewDecoder(joined.Body).Decode(&joinResponse))
	joined.Body.Close()

	begin := doEnrollmentJSON(
		t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/federation/enrollments/"+enrollmentRequestID+"/preparation/begin",
		map[string]any{}, joinResponse.SpokeCredential,
	)
	require.Equal(http.StatusOK, begin.StatusCode)
	begin.Body.Close()
	sealRequest := db.SpokePreparationSealRequest{
		EnrollmentID: enrollmentRequestID, NodeID: enrollmentNodeID,
		HubNodeID:        enrollmentHubID,
		ProtocolVersion:  federation.ProtocolVersion,
		MigrationVersion: db.WorkspaceLaunchSpecMigrationVersion,
		ReceiptsDigest:   "receipts-digest", DrainedAckGeneration: 4,
	}
	sealRequest.PreparationDigest, err = db.SpokePreparationSealDigest(sealRequest)
	require.NoError(err)
	sealBody := func(request db.SpokePreparationSealRequest) map[string]any {
		return map[string]any{
			"node_id": request.NodeID, "hub_node_id": request.HubNodeID,
			"protocol_version":       request.ProtocolVersion,
			"migration_version":      request.MigrationVersion,
			"receipts_digest":        request.ReceiptsDigest,
			"drained_ack_generation": request.DrainedAckGeneration,
			"preparation_digest":     request.PreparationDigest,
		}
	}
	seal := func(payload map[string]any) *http.Response {
		return doEnrollmentJSON(
			t, server.Client(), http.MethodPost,
			server.URL+"/api/v1/federation/enrollments/"+enrollmentRequestID+"/preparation/seal",
			payload, joinResponse.SpokeCredential,
		)
	}
	first := seal(sealBody(sealRequest))
	require.Equal(http.StatusOK, first.StatusCode)
	var firstSeal db.SpokePreparationSeal
	require.NoError(json.NewDecoder(first.Body).Decode(&firstSeal))
	first.Body.Close()
	assert.NotEmpty(firstSeal.Seal)
	assert.Equal(sealRequest.PreparationDigest, firstSeal.PreparationDigest)

	retry := seal(sealBody(sealRequest))
	require.Equal(http.StatusOK, retry.StatusCode)
	var retrySeal db.SpokePreparationSeal
	require.NoError(json.NewDecoder(retry.Body).Decode(&retrySeal))
	retry.Body.Close()
	assert.Equal(firstSeal, retrySeal)

	different := sealRequest
	different.ReceiptsDigest = "changed-receipts"
	different.PreparationDigest, err = db.SpokePreparationSealDigest(different)
	require.NoError(err)
	conflict := seal(sealBody(different))
	assert.Equal(http.StatusConflict, conflict.StatusCode)
	conflict.Body.Close()

	stored, err := fixture.database.GetSpokePreparationSeal(t.Context(), enrollmentRequestID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(firstSeal.Seal, stored.Seal)
}

func TestEnrollmentRevocationRecoversAfterPartialHubCleanup(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const hubCredential = "hub-calls-spoke-token"

	spoke := newEnrollmentHandlerFixture(t, enrollmentNodeID, func(deps *Deps) {
		deps.Config.Fleet = config.Fleet{
			Enabled: true, Role: config.FleetRoleSpoke,
			Hub: &config.FleetHub{NodeID: enrollmentHubID},
		}
	})
	spokeServer := httptest.NewTLSServer(authenticatedEnrollmentHandler(spoke))
	t.Cleanup(spokeServer.Close)
	require.NoError(spoke.credentials.StoreInbound(
		enrollmentHubID, hubCredential, federationauth.HubToSpokeScopes(),
	))
	require.NoError(spoke.credentials.StoreOutbound(
		enrollmentHubID, "spoke-calls-hub-token", federationauth.SpokeToHubScopes(),
	))
	require.NoError(spoke.enrollments.SaveLocal(t.Context(), federation.LocalEnrollment{
		EnrollmentID: enrollmentRequestID, NodeID: enrollmentNodeID,
		SpokePlatform: "linux", SpokeBaseURL: spokeServer.URL,
		HubID: enrollmentHubID, HubURL: "https://hub.example",
		ProtocolVersion: federation.ProtocolVersion,
		State:           federation.EnrollmentActive,
		ExpiresAt:       time.Now().Add(time.Hour),
	}))
	_, err := spoke.database.BeginSpokePreparation(t.Context(), db.SpokePreparationBinding{
		EnrollmentID: enrollmentRequestID, HubNodeID: enrollmentHubID,
		LocalNodeID: enrollmentNodeID, ProtocolVersion: federation.ProtocolVersion,
	})
	require.NoError(err)

	var removeAttempts int
	var removedNodeID string
	hub := newEnrollmentHandlerFixture(t, enrollmentHubID, func(deps *Deps) {
		deps.FederationHTTPClient = spokeServer.Client()
		deps.RemoveMember = func(_ context.Context, nodeID string) error {
			removeAttempts++
			if removeAttempts == 1 {
				return errors.New("injected member persistence failure")
			}
			removedNodeID = nodeID
			return nil
		}
	})
	hubServer := httptest.NewTLSServer(hub.mux)
	t.Cleanup(hubServer.Close)
	oneTime, err := hub.enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: enrollmentHubID, BaseURL: hubServer.URL,
	}, time.Now().Add(time.Minute))
	require.NoError(err)
	joinRequest := federation.JoinRequest{
		EnrollmentID: enrollmentRequestID, NodeID: enrollmentNodeID,
		Platform: "linux", BaseURL: spokeServer.URL,
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   hubCredential,
	}
	joined := postEnrollmentRequest(
		t, hubServer.Client(), hubServer.URL, oneTime.Token, joinRequest,
	)
	require.Equal(http.StatusCreated, joined.StatusCode)
	var joinResponse federation.JoinResponse
	require.NoError(json.NewDecoder(joined.Body).Decode(&joinResponse))
	joined.Body.Close()
	require.NoError(hub.enrollments.Activate(t.Context(), enrollmentRequestID))
	require.NoError(hub.credentials.UpdateInboundScopes(
		enrollmentNodeID, federationauth.SpokeToHubScopes(),
	))
	require.NoError(hub.credentials.UpdateOutboundScopes(
		enrollmentNodeID, federationauth.HubToSpokeScopes(),
	))

	accepted := doEnrollmentJSON(
		t, spokeServer.Client(), http.MethodGet,
		spokeServer.URL+"/api/v1/federation/identity", nil, hubCredential,
	)
	require.Equal(http.StatusOK, accepted.StatusCode)
	accepted.Body.Close()

	revoked := doEnrollmentJSON(
		t, hubServer.Client(), http.MethodDelete,
		hubServer.URL+"/api/v1/fleet/enrollments/"+enrollmentRequestID,
		nil, "",
	)
	require.Equal(http.StatusInternalServerError, revoked.StatusCode)
	revoked.Body.Close()

	local, ok := spoke.enrollments.Local()
	require.True(ok)
	assert.Equal(federation.EnrollmentRevoked, local.State)
	preparation, err := spoke.database.GetSpokePreparation(t.Context())
	require.NoError(err)
	assert.Equal(db.SpokePreparationOpen, preparation.Phase)
	tombstone, ok := spoke.credentials.Authenticate(hubCredential)
	require.True(ok)
	assert.Equal(
		map[federationauth.Scope]struct{}{federationauth.ScopeEnrollmentActivate: {}},
		tombstone.Scopes,
	)

	retrySpoke := doEnrollmentJSON(
		t, spokeServer.Client(), http.MethodDelete,
		spokeServer.URL+"/api/v1/fleet/enrollments/"+enrollmentRequestID,
		nil, hubCredential,
	)
	require.Equal(http.StatusNoContent, retrySpoke.StatusCode)
	retrySpoke.Body.Close()

	retryHub := doEnrollmentJSON(
		t, hubServer.Client(), http.MethodDelete,
		hubServer.URL+"/api/v1/fleet/enrollments/"+enrollmentRequestID,
		nil, "",
	)
	require.Equal(http.StatusNoContent, retryHub.StatusCode)
	retryHub.Body.Close()

	_, ok = hub.credentials.Authenticate(joinResponse.SpokeCredential)
	assert.False(ok, "the next inbound request must fail authentication")
	_, ok = hub.credentials.Outbound(enrollmentNodeID)
	assert.False(ok, "the hub must stop calling the revoked spoke")
	_, ok = spoke.credentials.Outbound(enrollmentHubID)
	assert.False(ok, "the spoke must stop calling the revoked hub")
	assert.Equal(enrollmentNodeID, removedNodeID)
	persisted, err := hub.enrollments.Get(t.Context(), enrollmentRequestID)
	require.NoError(err)
	assert.Equal(federation.EnrollmentRevoked, persisted.State)
}

func newEnrollmentHandlerFixture(
	t *testing.T, nodeID string, configure func(*Deps),
) enrollmentHandlerFixture {
	t.Helper()
	credentials, err := federationauth.Open(
		filepath.Join(t.TempDir(), "credentials.json"),
	)
	require.NoError(t, err)
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(t, err)
	deps := Deps{
		DB: dbtest.Open(t), NodeID: nodeID,
		Credentials: credentials, Enrollments: enrollments,
		Config: ConfigSnapshot{Fleet: config.Fleet{
			Enabled: true, Role: config.FleetRoleHub,
		}},
	}
	if configure != nil {
		configure(&deps)
	}
	handler := New(deps)
	mux := http.NewServeMux()
	apiConfig := huma.DefaultConfig("fleet enrollment test", "0.0.0")
	apiConfig.OpenAPIPath = ""
	apiConfig.DocsPath = ""
	apiConfig.SchemasPath = ""
	api := humago.NewWithPrefix(mux, "/api/v1", apiConfig)
	handler.Register(api)
	return enrollmentHandlerFixture{
		handler: handler, credentials: credentials, enrollments: enrollments,
		database: deps.DB, mux: mux,
	}
}

func authenticatedEnrollmentHandler(fixture enrollmentHandlerFixture) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/federation/enrollments/") ||
			strings.Contains(r.URL.Path, "/fleet/enrollments/") ||
			r.URL.Path == "/api/v1/federation/identity" {
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			principal, ok := fixture.credentials.Authenticate(token)
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			r = r.WithContext(federationauth.WithPrincipal(r.Context(), principal))
		}
		fixture.mux.ServeHTTP(w, r)
	})
}

func postEnrollmentRequest(
	t *testing.T, client *http.Client, baseURL, token string, request federation.JoinRequest,
) *http.Response {
	t.Helper()
	return doEnrollmentJSON(
		t, client, http.MethodPost, baseURL+"/api/v1/federation/enrollments",
		request, token,
	)
}

func doEnrollmentJSON(
	t *testing.T, client *http.Client, method, target string, body any, token string,
) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(
		t.Context(), method, target, bytes.NewReader(raw),
	)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	return response
}

func scopeSetForEnrollmentTest(scopes []federationauth.Scope) map[federationauth.Scope]struct{} {
	result := make(map[federationauth.Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		result[scope] = struct{}{}
	}
	return result
}

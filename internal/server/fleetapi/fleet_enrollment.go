package fleetapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"time"

	"go.kenn.io/forge/internal/apiclient/generated"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/tracing"
)

const (
	maxFederationEnrollmentRequestBytes  = 16 << 10
	maxFederationEnrollmentResponseBytes = 1 << 20
)

type createEnrollmentTokenInput struct {
	Body struct {
		BaseURL          string `json:"base_url"`
		Name             string `json:"name,omitempty"`
		ExpiresInSeconds int    `json:"expires_in_seconds" minimum:"1" maximum:"86400"`
	}
}

type enrollmentTokenOutput = httpapi.BodyOutput[federation.EnrollmentToken]

type beginEnrollmentInput struct {
	Authorization string `header:"Authorization"`
	Body          federation.JoinRequest
}

type beginEnrollmentOutput = httpapi.BodyOutput[federation.JoinResponse]

type localJoinInput struct {
	Body struct {
		HubURL          string `json:"hub_base_url"`
		SpokeBaseURL    string `json:"spoke_base_url"`
		Name            string `json:"name,omitempty"`
		EnrollmentToken string `json:"enrollment_token"`
	}
}

type localJoinOutput = httpapi.BodyOutput[federation.LocalEnrollment]

type activateEnrollmentInput struct {
	EnrollmentID string `path:"enrollment_id"`
	Body         struct {
		ProtocolVersion        int    `json:"protocol_version"`
		ActivationLeaseVersion int    `json:"activation_lease_version"`
		PreparationSeal        string `json:"preparation_seal"`
	}
}

type activateEnrollmentOutput = httpapi.BodyOutput[federation.Enrollment]

type beginSpokePreparationInput struct {
	EnrollmentID string `path:"enrollment_id"`
}

type beginSpokePreparationOutput = httpapi.BodyOutput[federation.Enrollment]

type sealSpokePreparationInput struct {
	EnrollmentID string `path:"enrollment_id"`
	Body         struct {
		NodeID               string `json:"node_id"`
		HubNodeID            string `json:"hub_node_id"`
		ProtocolVersion      int    `json:"protocol_version"`
		MigrationVersion     int    `json:"migration_version"`
		ReceiptsDigest       string `json:"receipts_digest"`
		DrainedAckGeneration int64  `json:"drained_ack_generation"`
		PreparationDigest    string `json:"preparation_digest"`
	}
}

type sealSpokePreparationOutput = httpapi.BodyOutput[db.SpokePreparationSeal]

type revokeEnrollmentInput struct {
	EnrollmentID string `path:"enrollment_id"`
}

type abortEnrollmentInput struct {
	EnrollmentID string `path:"enrollment_id"`
}

type federationIdentityOutputBody struct {
	NodeID          string `json:"node_id"`
	ProtocolVersion int    `json:"protocol_version"`
}

type federationIdentityOutput = httpapi.BodyOutput[federationIdentityOutputBody]

func (h *Handler) registerEnrollmentRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "create-fleet-enrollment-token",
		Method:      http.MethodPost, Path: "/fleet/enrollment-tokens",
		DefaultStatus: http.StatusCreated,
		Summary:       "Create a one-time fleet enrollment token", Tags: []string{"Fleet"},
	}, h.createEnrollmentToken)
	huma.Register(api, huma.Operation{
		OperationID: "begin-federation-enrollment",
		Method:      http.MethodPost, Path: "/federation/enrollments",
		MaxBodyBytes:  maxFederationEnrollmentRequestBytes,
		DefaultStatus: http.StatusCreated,
		Summary:       "Begin or resume a federation enrollment", Tags: []string{"Fleet"},
	}, h.beginEnrollment)
	huma.Register(api, huma.Operation{
		OperationID: "join-federation",
		Method:      http.MethodPost, Path: "/fleet/join",
		Summary: "Join this daemon to a federation hub", Tags: []string{"Fleet"},
	}, h.joinFederation)
	huma.Register(api, huma.Operation{
		OperationID: "activate-federation-enrollment",
		Method:      http.MethodPost, Path: "/federation/enrollments/{enrollment_id}/activate",
		Summary: "Activate a prepared federation member", Tags: []string{"Fleet"},
	}, h.activateEnrollment)
	huma.Register(api, huma.Operation{
		OperationID: "begin-federation-spoke-preparation",
		Method:      http.MethodPost,
		Path:        "/federation/enrollments/{enrollment_id}/preparation/begin",
		Summary:     "Pin a pending enrollment before spoke preparation",
		Tags:        []string{"Fleet"},
	}, h.beginSpokePreparation)
	huma.Register(api, huma.Operation{
		OperationID: "seal-federation-spoke-preparation",
		Method:      http.MethodPost,
		Path:        "/federation/enrollments/{enrollment_id}/preparation/seal",
		Summary:     "Seal completed provider-state handoff for a spoke",
		Tags:        []string{"Fleet"},
	}, h.sealSpokePreparation)
	huma.Register(api, huma.Operation{
		OperationID: "get-federation-identity",
		Method:      http.MethodGet, Path: "/federation/identity",
		Summary: "Read the authenticated federation identity", Tags: []string{"Fleet"},
	}, h.getFederationIdentity)
	huma.Register(api, huma.Operation{
		OperationID: "revoke-federation-enrollment",
		Method:      http.MethodDelete, Path: "/fleet/enrollments/{enrollment_id}",
		Summary: "Revoke a federation member", Tags: []string{"Fleet"},
	}, h.revokeEnrollment)
	huma.Register(api, huma.Operation{
		OperationID: "abort-federation-enrollment",
		Method:      http.MethodPost, Path: "/federation/enrollments/{enrollment_id}/abort",
		Summary: "Abandon this spoke's pending federation enrollment", Tags: []string{"Fleet"},
	}, h.abortEnrollment)
}

func (h *Handler) beginSpokePreparation(
	ctx context.Context,
	input *beginSpokePreparationInput,
) (*beginSpokePreparationOutput, error) {
	if problem := h.requireHubEnrollmentStore(); problem != nil {
		return nil, problem
	}
	principal, ok := federationauth.PrincipalFromContext(ctx)
	if !ok {
		return nil, httpapi.NewProblem(
			http.StatusUnauthorized, httpapi.CodeUnauthorized,
			"federation principal is required", nil,
		)
	}
	enrollment, err := h.enrollments.Resume(ctx, input.EnrollmentID, principal.NodeID)
	if err != nil {
		return nil, enrollmentProblem(err)
	}
	if err := h.enrollments.MarkPreparationStarted(ctx, enrollment.ID); err != nil {
		return nil, enrollmentProblem(err)
	}
	enrollment, err = h.enrollments.Resume(ctx, enrollment.ID, enrollment.NodeID)
	if err != nil {
		return nil, enrollmentProblem(err)
	}
	return &beginSpokePreparationOutput{Body: enrollment}, nil
}

func (h *Handler) sealSpokePreparation(
	ctx context.Context,
	input *sealSpokePreparationInput,
) (*sealSpokePreparationOutput, error) {
	if problem := h.requireHubEnrollmentStore(); problem != nil {
		return nil, problem
	}
	if h.db == nil {
		return nil, httpapi.ServiceUnavailable("spoke preparation seal store unavailable")
	}
	principal, ok := federationauth.PrincipalFromContext(ctx)
	if !ok {
		return nil, httpapi.NewProblem(
			http.StatusUnauthorized, httpapi.CodeUnauthorized,
			"federation principal is required", nil,
		)
	}
	enrollment, err := h.enrollments.Resume(ctx, input.EnrollmentID, principal.NodeID)
	if err != nil {
		return nil, enrollmentProblem(err)
	}
	if input.Body.NodeID != principal.NodeID ||
		input.Body.NodeID != enrollment.NodeID ||
		input.Body.HubNodeID != h.nodeID ||
		input.Body.ProtocolVersion != federation.ProtocolVersion ||
		input.Body.MigrationVersion != db.WorkspaceLaunchSpecMigrationVersion {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict,
			"spoke preparation seal does not match the pending enrollment",
			map[string]any{"reason": "preparationSealBindingMismatch"},
		)
	}
	if err := h.enrollments.MarkPreparationStarted(ctx, enrollment.ID); err != nil {
		return nil, enrollmentProblem(err)
	}
	seal, err := h.db.IssueSpokePreparationSeal(ctx, db.SpokePreparationSealRequest{
		EnrollmentID: input.EnrollmentID, NodeID: input.Body.NodeID,
		HubNodeID:            input.Body.HubNodeID,
		ProtocolVersion:      input.Body.ProtocolVersion,
		MigrationVersion:     input.Body.MigrationVersion,
		ReceiptsDigest:       input.Body.ReceiptsDigest,
		DrainedAckGeneration: input.Body.DrainedAckGeneration,
		PreparationDigest:    input.Body.PreparationDigest,
	})
	if errors.Is(err, db.ErrSpokePreparationConflict) {
		return nil, httpapi.Conflict(httpapi.CodeConflict, err.Error(), map[string]any{
			"reason": "preparationSealConflict",
		})
	}
	if err != nil {
		return nil, httpapi.Internal("issue spoke preparation seal: " + err.Error())
	}
	return &sealSpokePreparationOutput{Body: seal}, nil
}

func (h *Handler) createEnrollmentToken(
	ctx context.Context, input *createEnrollmentTokenInput,
) (*enrollmentTokenOutput, error) {
	if problem := h.requireHubEnrollmentStore(); problem != nil {
		return nil, problem
	}
	configuredURL, err := federation.CanonicalOrigin(
		h.configSnapshot().Fleet.BaseURL,
	)
	if err != nil {
		return nil, httpapi.ServiceUnavailable("fleet hub origin is not configured")
	}
	requestedURL, err := federation.CanonicalOrigin(input.Body.BaseURL)
	if err != nil {
		return nil, httpapi.Validation("body.base_url", err.Error())
	}
	if requestedURL != configuredURL {
		return nil, httpapi.Validation(
			"body.base_url", "origin must match the active fleet hub origin",
		)
	}
	if input.Body.ExpiresInSeconds <= 0 {
		return nil, httpapi.Validation(
			"body.expires_in_seconds", "expiry must be greater than zero",
		)
	}
	if _, err := h.enrollments.CleanupExpired(ctx); err != nil {
		return nil, httpapi.Internal("clean up expired enrollments: " + err.Error())
	}
	token, err := h.enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: h.nodeID, Name: input.Body.Name, BaseURL: configuredURL,
	}, h.now().Add(time.Duration(input.Body.ExpiresInSeconds)*time.Second))
	if err != nil {
		return nil, enrollmentProblem(err)
	}
	return &enrollmentTokenOutput{Body: token}, nil
}

func (h *Handler) beginEnrollment(
	ctx context.Context, input *beginEnrollmentInput,
) (*beginEnrollmentOutput, error) {
	if problem := h.requireHubEnrollmentStore(); problem != nil {
		return nil, problem
	}
	if _, err := h.enrollments.CleanupExpired(ctx); err != nil {
		return nil, httpapi.Internal("clean up expired enrollments: " + err.Error())
	}
	token, ok := enrollmentBearer(input.Authorization)
	if !ok {
		return nil, httpapi.NewProblem(
			http.StatusUnauthorized, httpapi.CodeUnauthorized,
			"missing or invalid one-time enrollment token", nil,
		)
	}
	enrollment, err := h.enrollments.Begin(ctx, token, input.Body)
	if errors.Is(err, federation.ErrEnrollmentTokenInvalid) {
		enrollment, err = h.beginDeclaredEnrollment(ctx, token, input.Body)
	}
	if problem, ok := errors.AsType[*httpapi.ProblemError](err); ok {
		return nil, problem
	}
	if err != nil {
		return nil, enrollmentProblem(err)
	}
	if h.credentials == nil {
		return nil, httpapi.ServiceUnavailable("federation credential store unavailable")
	}
	if err := h.credentials.StoreOutbound(
		enrollment.NodeID, input.Body.HubCredential,
		federationauth.PendingHubToSpokeScopes(),
	); err != nil {
		return nil, httpapi.Internal("persist hub-to-spoke credential: " + err.Error())
	}
	spokeCredential, err := h.credentials.MintInbound(
		enrollment.NodeID, federationauth.PendingSpokeToHubScopes(),
	)
	if err != nil {
		return nil, httpapi.Internal("persist spoke-to-hub credential: " + err.Error())
	}
	return &beginEnrollmentOutput{Body: federation.JoinResponse{
		EnrollmentID:    enrollment.ID,
		HubID:           enrollment.HubID,
		HubName:         enrollment.HubName,
		HubURL:          enrollment.HubURL,
		SpokeCredential: spokeCredential,
		ProtocolVersion: federation.ProtocolVersion,
		State:           enrollment.State, ExpiresAt: enrollment.ExpiresAt,
		PreparationRequired: true,
	}}, nil
}

// beginDeclaredEnrollment admits a join whose bearer is the enrollment
// secret of a spoke in fleet.declared_spokes, joining from that spoke's
// declared origin. Each secret starts one enrollment. A fresh secret
// replaces the enrollment of the machine previously at that origin, which is
// how a reinstalled spoke rejoins.
func (h *Handler) beginDeclaredEnrollment(
	ctx context.Context, token string, request federation.JoinRequest,
) (federation.Enrollment, error) {
	origin, err := federation.CanonicalOrigin(request.BaseURL)
	if err != nil {
		return federation.Enrollment{}, federation.ErrEnrollmentTokenInvalid
	}
	fleet := h.configSnapshot().Fleet
	spoke, ok := fleet.DeclaredSpokeForOrigin(origin)
	if !ok {
		return federation.Enrollment{}, federation.ErrEnrollmentTokenInvalid
	}
	secret, err := federation.ReadDeclaredSecret(spoke.EnrollmentSecretFile)
	if err != nil {
		// A secret manager may not have published the file yet, so the
		// spoke must retry instead of treating its secret as wrong.
		slog.Warn("read declared fleet spoke secret", "name", spoke.Name, "err", err)
		return federation.Enrollment{}, httpapi.ServiceUnavailable(
			"the hub cannot read the enrollment secret it declares for this origin",
		)
	}
	tokenDigest, secretDigest := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(tokenDigest[:], secretDigest[:]) != 1 {
		return federation.Enrollment{}, federation.ErrEnrollmentTokenInvalid
	}
	h.enrollmentMu.Lock()
	defer h.enrollmentMu.Unlock()
	if h.enrollments.DeclaredSecretConsumed(token) {
		return federation.Enrollment{}, federation.ErrEnrollmentTokenConsumed
	}
	hubURL, err := federation.CanonicalOrigin(fleet.BaseURL)
	if err != nil {
		return federation.Enrollment{}, httpapi.ServiceUnavailable("fleet hub origin is not configured")
	}
	hub := federation.Identity{NodeID: h.nodeID, BaseURL: hubURL}
	replaced, err := h.enrollments.DeclaredJoinReplacements(hub, request)
	if err != nil {
		return federation.Enrollment{}, err
	}
	for _, existing := range replaced {
		if err := h.revokeEnrollmentState(ctx, existing, false); err != nil {
			return federation.Enrollment{}, err
		}
		slog.Info(
			"revoke fleet enrollment replaced by a new declared secret",
			"node_id", existing.NodeID, "origin", existing.SpokeBaseURL,
		)
	}
	request.Name = spoke.Name
	return h.enrollments.BeginDeclared(ctx, token, hub, request)
}

// ReconcileDeclaredSpokes applies fleet.declared_spokes to the hub's
// enrollments: a live enrollment at a declared origin becomes declared
// membership, an active declared spoke missing from fleet.members is
// restored there, and declared membership whose origin left the list is
// revoked. CLI-enrolled members at undeclared origins are left alone.
func (h *Handler) ReconcileDeclaredSpokes(ctx context.Context) error {
	if h.requireHubEnrollmentStore() != nil {
		return nil
	}
	h.enrollmentMu.Lock()
	defer h.enrollmentMu.Unlock()
	fleet := h.configSnapshot().Fleet
	enrollments := h.enrollments.List()
	var errs []error
	for _, enrollment := range enrollments {
		if enrollment.State == federation.EnrollmentRevoked {
			// A tombstone whose member entry survived a failed removal.
			if _, member := fleetMember(fleet, enrollment.NodeID); member &&
				enrollment.Declared && !liveNode(enrollments, enrollment.NodeID) {
				if err := h.revokeEnrollmentState(ctx, enrollment, true); err != nil {
					errs = append(errs, err)
					continue
				}
				slog.Info(
					"finish revoking declared fleet spoke",
					"node_id", enrollment.NodeID, "origin", enrollment.SpokeBaseURL,
				)
			}
			continue
		}
		_, declared := fleet.DeclaredSpokeForOrigin(enrollment.SpokeBaseURL)
		switch {
		case !declared && enrollment.Declared:
			// A spoke this request does not reach learns of the revocation at
			// its next activation lease renewal or start, through the
			// credential revokeEnrollmentState keeps for that.
			delivered, err := h.requestSpokeEnrollmentRevocation(ctx, enrollment)
			if err != nil {
				slog.Warn(
					"notify spoke of declared membership removal",
					"node_id", enrollment.NodeID, "err", err,
				)
			}
			if err := h.revokeEnrollmentState(ctx, enrollment, !delivered); err != nil {
				errs = append(errs, err)
				continue
			}
			slog.Info(
				"revoke fleet spoke removed from fleet.declared_spokes",
				"node_id", enrollment.NodeID, "origin", enrollment.SpokeBaseURL,
			)
		case declared:
			if !enrollment.Declared {
				if err := h.enrollments.MarkDeclared(ctx, enrollment.ID); err != nil {
					errs = append(errs, err)
					continue
				}
				slog.Info(
					"adopt enrolled fleet spoke listed in fleet.declared_spokes",
					"node_id", enrollment.NodeID, "origin", enrollment.SpokeBaseURL,
				)
			}
			errs = append(errs, h.restoreDeclaredMember(ctx, fleet, enrollment))
		}
	}
	return errors.Join(errs...)
}

// liveNode reports whether a non-revoked enrollment holds nodeID. Credentials
// are keyed by node ID, so finishing an old revocation must not touch the
// credentials of a live enrollment of the same node.
func liveNode(enrollments []federation.Enrollment, nodeID string) bool {
	return slices.ContainsFunc(enrollments, func(enrollment federation.Enrollment) bool {
		return enrollment.NodeID == nodeID && enrollment.State != federation.EnrollmentRevoked
	})
}

func fleetMember(fleet config.Fleet, nodeID string) (config.FleetMember, bool) {
	index := slices.IndexFunc(fleet.Members, func(member config.FleetMember) bool {
		return member.NodeID == nodeID
	})
	if index < 0 {
		return config.FleetMember{}, false
	}
	return fleet.Members[index], true
}

// restoreDeclaredMember re-adds the fleet.members entry of an active declared
// spoke when a config rewrite dropped it; authorization requires the entry.
func (h *Handler) restoreDeclaredMember(
	ctx context.Context, fleet config.Fleet, enrollment federation.Enrollment,
) error {
	if _, member := fleetMember(fleet, enrollment.NodeID); member ||
		enrollment.State != federation.EnrollmentActive {
		return nil
	}
	if h.persistMember == nil {
		return errors.New("fleet membership persistence unavailable")
	}
	if err := h.persistMember(ctx, config.FleetMember{
		NodeID: enrollment.NodeID, Name: enrollment.SpokeName,
		BaseURL: enrollment.SpokeBaseURL, State: federation.EnrollmentActive,
	}); err != nil {
		return fmt.Errorf("restore declared fleet member: %w", err)
	}
	slog.Info(
		"restore fleet member for declared spoke",
		"node_id", enrollment.NodeID, "origin", enrollment.SpokeBaseURL,
	)
	return nil
}

func (h *Handler) joinFederation(
	ctx context.Context, input *localJoinInput,
) (*localJoinOutput, error) {
	h.enrollmentMu.Lock()
	defer h.enrollmentMu.Unlock()

	if problem := h.requireFederationStores(); problem != nil {
		return nil, problem
	}
	fleet := h.configSnapshot().Fleet
	if !fleet.Enabled {
		return nil, httpapi.NewProblem(
			http.StatusConflict, httpapi.CodeConflict,
			"federation must be enabled before joining a hub",
			map[string]any{"reason": "federationDisabled"},
		)
	}
	if len(fleet.Members) != 0 {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict,
			"revoke or migrate this hub's members before joining another hub",
			map[string]any{"reason": "hubHasMembers"},
		)
	}
	hubURL, err := federation.CanonicalOrigin(input.Body.HubURL)
	if err != nil {
		return nil, httpapi.Validation("body.hub_base_url", err.Error())
	}
	spokeBaseURL, err := federation.CanonicalOrigin(input.Body.SpokeBaseURL)
	if err != nil {
		return nil, httpapi.Validation("body.spoke_base_url", err.Error())
	}
	token := strings.TrimSpace(input.Body.EnrollmentToken)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, httpapi.Validation("body.enrollment_token", "enrollment token is required")
	}

	local, err := HubJoiner{
		Enrollments: h.enrollments, Credentials: h.credentials,
		NodeID: h.nodeID, HTTPClient: h.federationHTTPClient,
		PersistHubBinding: h.persistHubBinding,
	}.Join(ctx, hubURL, spokeBaseURL, strings.TrimSpace(input.Body.Name), token)
	if err != nil {
		return nil, err
	}
	return &localJoinOutput{Body: local}, nil
}

// HubJoiner sends this daemon's enrollment to a hub and records the pending
// binding: the reserved hub-to-spoke credential, the issued spoke-to-hub
// credential, the local enrollment, and fleet.hub. Both `fleet join` and a
// declared spoke's startup use it.
type HubJoiner struct {
	Enrollments       *federation.Store
	Credentials       *federationauth.Store
	NodeID            string
	HTTPClient        *http.Client
	PersistHubBinding func(context.Context, config.FleetHub) error
}

// Join enrolls with hubURL using a one-time token or declared secret.
func (j HubJoiner) Join(
	ctx context.Context, hubURL, spokeBaseURL, name, token string,
) (federation.LocalEnrollment, error) {
	var err error
	if j.HTTPClient == nil {
		j.HTTPClient = newFederationHTTPClient()
	}
	enrollmentID := ""
	if existing, ok := j.Enrollments.Local(); ok {
		if existing.State == federation.EnrollmentRevoked {
			if err := j.Credentials.RevokeOutbound(existing.HubID); err != nil {
				return federation.LocalEnrollment{}, httpapi.Internal("revoke previous hub credential: " + err.Error())
			}
			if err := j.Credentials.RevokeInboundNode(existing.HubID); err != nil {
				return federation.LocalEnrollment{}, httpapi.Internal("revoke previous inbound hub credential: " + err.Error())
			}
			if err := j.Enrollments.ClearLocal(ctx); err != nil {
				return federation.LocalEnrollment{}, httpapi.Internal("clear revoked local enrollment: " + err.Error())
			}
		} else {
			if existing.State != federation.EnrollmentPending ||
				existing.HubID != "" || existing.PreparationStarted ||
				existing.Preparation != nil {
				return federation.LocalEnrollment{}, httpapi.Conflict(
					httpapi.CodeConflict,
					"abort or revoke the existing local enrollment before joining again",
					map[string]any{"reason": "localEnrollmentExists"},
				)
			}
			if err := j.Credentials.RevokePending(existing.EnrollmentID); err != nil {
				return federation.LocalEnrollment{}, httpapi.Internal("revoke provisional hub credential: " + err.Error())
			}
			if existing.HubURL == hubURL && existing.SpokeBaseURL == spokeBaseURL {
				enrollmentID = existing.EnrollmentID
			} else if err := j.Enrollments.ClearLocal(ctx); err != nil {
				return federation.LocalEnrollment{}, httpapi.Internal("clear provisional local enrollment: " + err.Error())
			}
		}
	}
	if enrollmentID == "" {
		enrollmentID, err = federation.NewID()
		if err != nil {
			return federation.LocalEnrollment{}, httpapi.Internal(err.Error())
		}
	}
	local := federation.LocalEnrollment{
		EnrollmentID: enrollmentID, NodeID: j.NodeID,
		SpokeName: name, SpokePlatform: runtime.GOOS,
		SpokeBaseURL: spokeBaseURL, HubURL: hubURL,
		ProtocolVersion: federation.ProtocolVersion,
		State:           federation.EnrollmentPending, PreparationRequired: true,
	}
	if err := j.Enrollments.SaveLocal(ctx, local); err != nil {
		return federation.LocalEnrollment{}, httpapi.Internal("persist pending local enrollment: " + err.Error())
	}
	hubCredential, err := j.Credentials.ReserveInbound(
		enrollmentID, federationauth.PendingHubToSpokeScopes(),
	)
	if err != nil {
		return federation.LocalEnrollment{}, httpapi.Internal("reserve hub credential: " + err.Error())
	}
	response, err := sendJoinRequest(ctx, j.HTTPClient, hubURL, token, federation.JoinRequest{
		EnrollmentID: enrollmentID, NodeID: j.NodeID,
		Name: local.SpokeName, Platform: local.SpokePlatform, BaseURL: spokeBaseURL,
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   hubCredential,
	})
	if err != nil {
		return federation.LocalEnrollment{}, err
	}
	if err := validateJoinResponse(response, enrollmentID, hubURL); err != nil {
		return federation.LocalEnrollment{}, httpapi.NewProblem(
			http.StatusBadGateway, httpapi.CodeUpstreamError,
			"hub returned an invalid enrollment response",
			map[string]any{"reason": err.Error()},
		)
	}
	if err := j.Credentials.BindInbound(enrollmentID, response.HubID); err != nil {
		return federation.LocalEnrollment{}, httpapi.Internal("bind hub credential: " + err.Error())
	}
	if err := j.Credentials.StoreOutbound(
		response.HubID, response.SpokeCredential,
		federationauth.PendingSpokeToHubScopes(),
	); err != nil {
		return federation.LocalEnrollment{}, httpapi.Internal("persist spoke credential: " + err.Error())
	}
	local.HubID = response.HubID
	local.HubName = response.HubName
	local.HubURL = response.HubURL
	local.State = response.State
	local.ExpiresAt = response.ExpiresAt
	local.PreparationRequired = response.PreparationRequired
	if j.PersistHubBinding == nil {
		return federation.LocalEnrollment{}, httpapi.ServiceUnavailable("hub binding persistence unavailable")
	}
	if err := j.PersistHubBinding(ctx, config.FleetHub{
		NodeID: response.HubID,
		Name:   response.HubName, BaseURL: response.HubURL,
	}); err != nil {
		return federation.LocalEnrollment{}, httpapi.Internal("persist hub binding: " + err.Error())
	}
	if err := j.Enrollments.SaveLocal(ctx, local); err != nil {
		return federation.LocalEnrollment{}, httpapi.Internal("persist hub enrollment: " + err.Error())
	}
	return local, nil
}

func (h *Handler) activateEnrollment(
	ctx context.Context, input *activateEnrollmentInput,
) (*activateEnrollmentOutput, error) {
	if problem := h.requireHubEnrollmentStore(); problem != nil {
		return nil, problem
	}
	principal, ok := federationauth.PrincipalFromContext(ctx)
	if !ok {
		return nil, httpapi.NewProblem(
			http.StatusUnauthorized, httpapi.CodeUnauthorized,
			"federation principal is required", nil,
		)
	}
	if input.Body.ProtocolVersion != federation.ProtocolVersion {
		return nil, enrollmentProblem(fmt.Errorf(
			"%w: expected %d, got %d", federation.ErrProtocolMismatch,
			federation.ProtocolVersion, input.Body.ProtocolVersion,
		))
	}
	if input.Body.ActivationLeaseVersion != federation.ActivationLeaseVersion {
		return nil, enrollmentProblem(fmt.Errorf(
			"%w: activation lease expected %d, got %d",
			federation.ErrProtocolMismatch,
			federation.ActivationLeaseVersion,
			input.Body.ActivationLeaseVersion,
		))
	}
	enrollment, err := h.enrollments.Resume(ctx, input.EnrollmentID, principal.NodeID)
	if err != nil {
		return nil, enrollmentProblem(err)
	}
	if enrollment.State == federation.EnrollmentRevoked {
		return nil, enrollmentProblem(federation.ErrEnrollmentRevoked)
	}
	if enrollment.State != federation.EnrollmentPending &&
		enrollment.State != federation.EnrollmentActive {
		return nil, enrollmentProblem(federation.ErrEnrollmentConflict)
	}
	if enrollment.State == federation.EnrollmentPending {
		if h.db == nil {
			return nil, httpapi.ServiceUnavailable("spoke preparation seal store unavailable")
		}
		seal, err := h.db.GetSpokePreparationSeal(ctx, enrollment.ID)
		if err != nil {
			return nil, httpapi.Internal("read spoke preparation seal: " + err.Error())
		}
		presented := strings.TrimSpace(input.Body.PreparationSeal)
		if seal == nil || presented == "" ||
			seal.NodeID != principal.NodeID || seal.NodeID != enrollment.NodeID ||
			seal.HubNodeID != h.nodeID ||
			seal.ProtocolVersion != federation.ProtocolVersion ||
			subtle.ConstantTimeCompare([]byte(seal.Seal), []byte(presented)) != 1 {
			return nil, enrollmentProblem(federation.ErrPreparationRequired)
		}
		if h.persistMember == nil {
			return nil, httpapi.ServiceUnavailable("fleet membership persistence unavailable")
		}
		if err := h.persistMember(ctx, config.FleetMember{
			NodeID: enrollment.NodeID, Name: enrollment.SpokeName,
			BaseURL: enrollment.SpokeBaseURL, State: federation.EnrollmentActive,
		}); err != nil {
			return nil, httpapi.Internal("persist active fleet member: " + err.Error())
		}
	}
	validUntil := h.now().UTC().Add(federation.SpokeActivationLeaseDuration)
	if err := h.enrollments.Activate(ctx, enrollment.ID, validUntil); err != nil {
		return nil, enrollmentProblem(err)
	}
	if err := h.credentials.UpdateInboundScopes(
		enrollment.NodeID, federationauth.SpokeToHubScopes(),
	); err != nil {
		return nil, httpapi.Internal("activate spoke credential: " + err.Error())
	}
	if err := h.credentials.UpdateOutboundScopes(
		enrollment.NodeID, federationauth.HubToSpokeScopes(),
	); err != nil {
		return nil, httpapi.Internal("activate hub credential: " + err.Error())
	}
	enrollment, err = h.enrollments.Resume(ctx, enrollment.ID, enrollment.NodeID)
	if err != nil {
		return nil, enrollmentProblem(err)
	}
	return &activateEnrollmentOutput{Body: enrollment}, nil
}

func (h *Handler) getFederationIdentity(
	ctx context.Context, _ *struct{},
) (*federationIdentityOutput, error) {
	if _, ok := federationauth.PrincipalFromContext(ctx); !ok {
		return nil, httpapi.NewProblem(
			http.StatusUnauthorized, httpapi.CodeUnauthorized,
			"federation principal is required", nil,
		)
	}
	if h.nodeID == "" {
		return nil, httpapi.ServiceUnavailable("federation identity unavailable")
	}
	return &federationIdentityOutput{Body: federationIdentityOutputBody{
		NodeID: h.nodeID, ProtocolVersion: federation.ProtocolVersion,
	}}, nil
}

func (h *Handler) revokeEnrollment(
	ctx context.Context, input *revokeEnrollmentInput,
) (*struct{}, error) {
	if problem := h.requireFederationStores(); problem != nil {
		return nil, problem
	}
	fleet := h.configSnapshot().Fleet
	principal, federated := federationauth.PrincipalFromContext(ctx)
	if federated {
		if local, ok := h.enrollments.Local(); ok &&
			local.EnrollmentID == input.EnrollmentID &&
			local.HubID == principal.NodeID {
			return h.revokeLocalEnrollment(ctx, input)
		}
	}
	if fleet.Enabled && fleet.RoleOrDefault() == config.FleetRoleSpoke {
		return h.revokeLocalEnrollment(ctx, input)
	}
	if problem := h.requireHubEnrollmentStore(); problem != nil {
		return nil, problem
	}
	var enrollment federation.Enrollment
	var err error
	if federated {
		enrollment, err = h.enrollments.Resume(
			ctx, input.EnrollmentID, principal.NodeID,
		)
	} else {
		enrollment, err = h.enrollments.Get(ctx, input.EnrollmentID)
	}
	if err != nil {
		return nil, enrollmentProblem(err)
	}
	if enrollment.State != federation.EnrollmentRevoked {
		if _, err := h.requestSpokeEnrollmentRevocation(ctx, enrollment); err != nil {
			return nil, err
		}
	}
	if err := h.revokeEnrollmentState(ctx, enrollment, false); err != nil {
		return nil, err
	}
	return &struct{}{}, nil
}

func (h *Handler) revokeLocalEnrollment(
	ctx context.Context, input *revokeEnrollmentInput,
) (*struct{}, error) {
	principal, ok := federationauth.PrincipalFromContext(ctx)
	if !ok {
		return nil, httpapi.NewProblem(
			http.StatusUnauthorized, httpapi.CodeUnauthorized,
			"federation principal is required", nil,
		)
	}
	local, ok := h.enrollments.Local()
	if !ok || local.EnrollmentID != input.EnrollmentID ||
		local.HubID != principal.NodeID {
		return nil, httpapi.NewProblem(
			http.StatusForbidden, httpapi.CodeForbidden,
			"federation enrollment does not match the requesting hub",
			map[string]any{"reason": "federationEnrollmentMismatch"},
		)
	}
	if err := RevokeLocalEnrollment(ctx, h.enrollments, h.credentials, h.db, local); err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	return &struct{}{}, nil
}

// RevokeLocalEnrollment records on a spoke that its hub revoked local. The
// spoke keeps the hub's bearer only so the hub can repeat the revocation.
func RevokeLocalEnrollment(
	ctx context.Context, enrollments *federation.Store,
	credentials *federationauth.Store, database *db.DB,
	local federation.LocalEnrollment,
) error {
	local.State = federation.EnrollmentRevoked
	if err := enrollments.SaveLocal(ctx, local); err != nil {
		return fmt.Errorf("revoke local federation enrollment: %w", err)
	}
	if err := credentials.UpdateInboundScopes(
		local.HubID, []federationauth.Scope{federationauth.ScopeEnrollmentActivate},
	); err != nil {
		return fmt.Errorf("retain federation revocation credential: %w", err)
	}
	if err := credentials.RevokeOutbound(local.HubID); err != nil {
		return fmt.Errorf("revoke outbound federation credential: %w", err)
	}
	if database != nil {
		if err := database.AbortSpokePreparation(ctx); err != nil {
			return fmt.Errorf("clear spoke preparation state: %w", err)
		}
	}
	return nil
}

// requestSpokeEnrollmentRevocation tells the spoke the hub revoked its
// enrollment. It reports whether the spoke confirmed; it skips a spoke the
// hub can reach only through a tunnel that is down.
func (h *Handler) requestSpokeEnrollmentRevocation(
	ctx context.Context, enrollment federation.Enrollment,
) (bool, error) {
	direct := true
	for _, member := range h.configSnapshot().Fleet.Members {
		if member.NodeID == enrollment.NodeID && member.OutboundDisabled {
			direct = false
		}
	}
	// A spoke the hub cannot reach observes revocation at its next activation
	// check instead.
	if !direct && !h.tunnels.Connected(enrollment.NodeID) {
		return false, nil
	}
	credential, ok := h.credentials.Outbound(enrollment.NodeID)
	if !ok {
		return false, httpapi.Internal("outbound spoke credential is unavailable")
	}
	fleet := h.configSnapshot().Fleet
	requestContext, cancel := context.WithTimeout(ctx, fleet.PeerTimeoutOrDefault())
	defer cancel()
	request, err := generated.NewRevokeFederationEnrollmentRequest(requestContext, strings.TrimRight(enrollment.SpokeBaseURL, "/")+"/api/v1", &generated.RevokeFederationEnrollmentRequestOptions{PathParams: &generated.RevokeFederationEnrollmentPath{EnrollmentID: enrollment.ID}})
	if err != nil {
		return false, httpapi.Internal("build spoke revocation request: " + err.Error())
	}
	request.Header.Set("Content-Type", "application/json")
	h.authorizeFederationRequest(request.Header, credential)
	response, err := h.memberClientsFor(
		enrollment.NodeID, enrollment.SpokeBaseURL, direct,
	).rest.Do(request)
	if err != nil {
		return false, httpapi.NewProblem(
			http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable,
			"spoke could not confirm enrollment revocation",
			map[string]any{"reason": "spokeUnavailable"},
		)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, httpapi.NewProblem(
			http.StatusBadGateway, httpapi.CodeUpstreamError,
			fmt.Sprintf("spoke enrollment revocation returned HTTP %d", response.StatusCode),
			map[string]any{"reason": "spokeRevocationRejected"},
		)
	}
	return true, nil
}

func (h *Handler) abortEnrollment(
	ctx context.Context, input *abortEnrollmentInput,
) (*struct{}, error) {
	if problem := h.requireHubEnrollmentStore(); problem != nil {
		return nil, problem
	}
	principal, ok := federationauth.PrincipalFromContext(ctx)
	if !ok {
		return nil, httpapi.NewProblem(
			http.StatusUnauthorized, httpapi.CodeUnauthorized,
			"federation principal is required", nil,
		)
	}
	enrollment, err := h.enrollments.Resume(ctx, input.EnrollmentID, principal.NodeID)
	if err != nil {
		return nil, enrollmentProblem(err)
	}
	if enrollment.State != federation.EnrollmentPending {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict, "only a pending enrollment can be aborted",
			map[string]any{"reason": "enrollmentNotPending"},
		)
	}
	if err := h.revokeEnrollmentState(ctx, enrollment, false); err != nil {
		return nil, err
	}
	return &struct{}{}, nil
}

// revokeEnrollmentState revokes an enrollment and its credentials and member
// entry. keepNotice keeps the spoke's credential, narrowed to the activation
// routes, so a spoke the hub could not tell learns of the revocation when it
// next renews its activation lease; the activation route then reports the
// enrollment revoked. Proxying and events need scopes the narrowed
// credential lacks.
func (h *Handler) revokeEnrollmentState(
	ctx context.Context, enrollment federation.Enrollment, keepNotice bool,
) error {
	// Persist the revocation tombstone before fallible local cleanup. A retry
	// can then finish cleanup without contacting a spoke that already revoked
	// the active relationship.
	if err := h.enrollments.Revoke(ctx, enrollment.ID); err != nil {
		return enrollmentProblem(err)
	}
	if h.credentials != nil {
		revokeInbound := func() error { return h.credentials.RevokeInboundNode(enrollment.NodeID) }
		if keepNotice {
			revokeInbound = func() error {
				return h.credentials.RestrictInboundNode(
					enrollment.NodeID, []federationauth.Scope{federationauth.ScopeEnrollmentActivate},
				)
			}
		}
		if err := revokeInbound(); err != nil {
			return httpapi.Internal("revoke inbound federation credential: " + err.Error())
		}
		if h.cancelEventStreams != nil {
			h.cancelEventStreams(enrollment.NodeID)
		}
		if err := h.credentials.RevokeOutbound(enrollment.NodeID); err != nil {
			return httpapi.Internal("revoke outbound federation credential: " + err.Error())
		}
	}
	if h.removeMember != nil {
		if err := h.removeMember(ctx, enrollment.NodeID); err != nil {
			return httpapi.Internal("remove fleet member: " + err.Error())
		}
	}
	return nil
}

func (h *Handler) requireFederationStores() error {
	if h == nil {
		return httpapi.ServiceUnavailable("federation enrollment is unavailable")
	}
	if h.enrollments == nil || h.credentials == nil || h.nodeID == "" {
		return httpapi.ServiceUnavailable("federation enrollment is unavailable")
	}
	return nil
}

func (h *Handler) requireHubEnrollmentStore() error {
	if problem := h.requireFederationStores(); problem != nil {
		return problem
	}
	fleet := h.configSnapshot().Fleet
	if !fleet.Enabled || fleet.RoleOrDefault() != config.FleetRoleHub {
		return httpapi.NewProblem(
			http.StatusConflict, httpapi.CodeConflict,
			"this daemon is not an enabled federation hub",
			map[string]any{"reason": "notHub"},
		)
	}
	return nil
}

func sendJoinRequest(
	ctx context.Context, federationClient *http.Client, hubURL, enrollmentToken string,
	request federation.JoinRequest,
) (federation.JoinResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return federation.JoinResponse{}, httpapi.Internal("encode enrollment request: " + err.Error())
	}
	endpoint := hubURL + "/api/v1/federation/enrollments"
	httpRequest, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, bytes.NewReader(body),
	)
	if err != nil {
		return federation.JoinResponse{}, httpapi.Internal("build enrollment request: " + err.Error())
	}
	httpRequest.Header.Set("Authorization", "Bearer "+enrollmentToken)
	httpRequest.Header.Set("Content-Type", "application/json")
	// The shared client stays untraced: member clients clone its *http.Transport.
	client := *federationClient
	client.Transport = tracing.PeerHTTPTransport(client.Transport)
	response, err := client.Do(httpRequest)
	if err != nil {
		return federation.JoinResponse{}, httpapi.NewProblem(
			http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable,
			"the hub may have accepted the enrollment, so Forge will not retry with the one-time token; create a new token and run fleet join again",
			map[string]any{"reason": "hubUnavailable", "error": err.Error()},
		)
	}
	return decodeJoinResponse(response)
}

func decodeJoinResponse(response *http.Response) (federation.JoinResponse, error) {
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(
		response.Body, maxFederationEnrollmentResponseBytes+1,
	))
	if err != nil {
		return federation.JoinResponse{}, httpapi.NewProblem(
			http.StatusBadGateway, httpapi.CodeUpstreamError,
			"read hub enrollment response", nil,
		)
	}
	if len(raw) > maxFederationEnrollmentResponseBytes {
		return federation.JoinResponse{}, httpapi.NewProblem(
			http.StatusBadGateway, httpapi.CodeUpstreamError,
			"hub enrollment response is too large", nil,
		)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem httpapi.ProblemError
		if json.Unmarshal(raw, &problem) == nil && problem.Status != 0 {
			return federation.JoinResponse{}, &problem
		}
		// A proxy in front of the hub can answer without a problem body. Its
		// status still says whether retrying can help.
		status := response.StatusCode
		if status < http.StatusBadRequest {
			status = http.StatusBadGateway
		}
		return federation.JoinResponse{}, httpapi.NewProblem(
			status, httpapi.CodeUpstreamError,
			fmt.Sprintf("hub enrollment returned HTTP %d", response.StatusCode), nil,
		)
	}
	var result federation.JoinResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return federation.JoinResponse{}, httpapi.NewProblem(
			http.StatusBadGateway, httpapi.CodeUpstreamError,
			"decode hub enrollment response", nil,
		)
	}
	return result, nil
}

func validateJoinResponse(
	response federation.JoinResponse, enrollmentID, hubURL string,
) error {
	if response.EnrollmentID != enrollmentID {
		return errors.New("enrollmentIDMismatch")
	}
	if response.ProtocolVersion != federation.ProtocolVersion {
		return errors.New("protocolVersionMismatch")
	}
	canonicalURL, err := federation.CanonicalOrigin(response.HubURL)
	if err != nil || canonicalURL != hubURL {
		return errors.New("hubOriginMismatch")
	}
	if !federation.ValidNodeID(response.HubID) ||
		response.SpokeCredential == "" ||
		strings.TrimSpace(response.SpokeCredential) != response.SpokeCredential ||
		strings.ContainsAny(response.SpokeCredential, " \t\r\n") {
		return errors.New("hubIdentityInvalid")
	}
	if response.State != federation.EnrollmentPending &&
		response.State != federation.EnrollmentActive {
		return errors.New("enrollmentStateInvalid")
	}
	if response.ExpiresAt.IsZero() {
		return errors.New("enrollmentExpiryInvalid")
	}
	return nil
}

func enrollmentBearer(authorization string) (string, bool) {
	token, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != "" && !strings.ContainsAny(token, " \t\r\n")
}

func enrollmentProblem(err error) error {
	switch {
	case errors.Is(err, federation.ErrEnrollmentTokenInvalid):
		return httpapi.NewProblem(http.StatusUnauthorized, httpapi.CodeUnauthorized,
			"invalid one-time enrollment token", map[string]any{"reason": "enrollmentTokenInvalid"})
	case errors.Is(err, federation.ErrEnrollmentTokenExpired):
		return httpapi.NewProblem(http.StatusUnauthorized, httpapi.CodeUnauthorized,
			"expired one-time enrollment token", map[string]any{"reason": "enrollmentTokenExpired"})
	case errors.Is(err, federation.ErrEnrollmentTokenConsumed):
		return httpapi.NewProblem(http.StatusConflict, httpapi.CodeConflict,
			"one-time enrollment token was consumed by another enrollment",
			map[string]any{"reason": "enrollmentTokenConsumed"})
	case errors.Is(err, federation.ErrDuplicateNodeID):
		return httpapi.NewProblem(http.StatusConflict, httpapi.CodeConflict,
			"node ID is already enrolled at another origin",
			map[string]any{"reason": "duplicateNodeID"})
	case errors.Is(err, federation.ErrDuplicateOrigin),
		errors.Is(err, federation.ErrEnrollmentConflict):
		return httpapi.NewProblem(http.StatusConflict, httpapi.CodeConflict,
			err.Error(), map[string]any{"reason": "enrollmentConflict"})
	case errors.Is(err, federation.ErrProtocolMismatch):
		return httpapi.NewProblem(http.StatusConflict, httpapi.CodeConflict,
			err.Error(), map[string]any{
				"reason": "protocolMismatch", "expected": federation.ProtocolVersion,
			})
	case errors.Is(err, federation.ErrPreparationRequired):
		return httpapi.NewProblem(http.StatusConflict, httpapi.CodeConflict,
			err.Error(), map[string]any{"reason": "preparationRequired"})
	case errors.Is(err, federation.ErrEnrollmentNotFound):
		return httpapi.NotFound(httpapi.CodeNotFound, err.Error(), nil)
	case errors.Is(err, federation.ErrEnrollmentRevoked):
		return httpapi.NewProblem(http.StatusForbidden, httpapi.CodeForbidden,
			err.Error(), map[string]any{"reason": "enrollmentRevoked"})
	default:
		return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
}

func newFederationHTTPClient() *http.Client {
	return hardenedFederationHTTPClient(&http.Client{Timeout: 15 * time.Second}, false)
}

func newFederationMemberClients(base *http.Client) federationMemberClients {
	return federationMemberClients{
		rest:      hardenedFederationMemberHTTPClient(base),
		proxy:     hardenedFederationProxyHTTPClient(base),
		websocket: hardenedFederationHTTPClient(base, true),
	}
}

// Member REST requests carry a live fleet timeout on their request context.
// Transport deadlines would cap that timeout even after a config reload.
func hardenedFederationMemberHTTPClient(base *http.Client) *http.Client {
	client := hardenedFederationHTTPClient(base, true)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return client
	}
	transport = transport.Clone()
	transport.DialContext = (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 0
	transport.ResponseHeaderTimeout = 0
	client.Transport = transport
	return client
}

func hardenedFederationProxyHTTPClient(base *http.Client) *http.Client {
	client := hardenedFederationHTTPClient(base, true)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return client
	}
	transport = transport.Clone()
	transport.ResponseHeaderTimeout = 0
	client.Transport = transport
	return client
}

func hardenedFederationHTTPClient(base *http.Client, streaming bool) *http.Client {
	if base == nil {
		base = &http.Client{Timeout: 15 * time.Second}
	}
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if streaming {
		client.Timeout = 0
	} else if client.Timeout <= 0 || client.Timeout > 15*time.Second {
		client.Timeout = 15 * time.Second
	}

	var transport *http.Transport
	switch existing := base.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone() //nolint:forbidigo // a fresh base; callers wrap the clients they send with
	case *http.Transport:
		transport = existing.Clone()
	}
	if transport == nil {
		return &client
	}
	transport.DialContext = (&net.Dialer{
		Timeout: 5 * time.Second, KeepAlive: 30 * time.Second,
	}).DialContext
	if transport.TLSHandshakeTimeout <= 0 || transport.TLSHandshakeTimeout > 5*time.Second {
		transport.TLSHandshakeTimeout = 5 * time.Second
	}
	if transport.ResponseHeaderTimeout <= 0 || transport.ResponseHeaderTimeout > 10*time.Second {
		transport.ResponseHeaderTimeout = 10 * time.Second
	}
	if transport.ExpectContinueTimeout <= 0 || transport.ExpectContinueTimeout > time.Second {
		transport.ExpectContinueTimeout = time.Second
	}
	if transport.MaxResponseHeaderBytes <= 0 || transport.MaxResponseHeaderBytes > 1<<20 {
		transport.MaxResponseHeaderBytes = 1 << 20
	}
	client.Transport = transport
	return &client
}

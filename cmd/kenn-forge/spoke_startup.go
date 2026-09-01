package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
)

type federationSpokeStartupState string

const (
	federationStartupHub            federationSpokeStartupState = "hub"
	federationStartupActive         federationSpokeStartupState = "active"
	federationStartupActionRequired federationSpokeStartupState = "action_required"
	federationStartupIncompatible   federationSpokeStartupState = "incompatible"

	maxSpokeActivationResponseBytes = 1 << 20
	spokeActivationAttempts         = 3
)

var errHubProtocolMismatch = errors.New("hub federation protocol is incompatible")

type federationSpokeStartup struct {
	State  federationSpokeStartupState
	Reason string
}

func (s federationSpokeStartup) Active() bool {
	return s.State == federationStartupActive
}

func activateFederationSpokeAtStartup(
	ctx context.Context,
	database *db.DB,
	cfg *config.Config,
	nodeID string,
	enrollments *federation.Store,
	credentials *federationauth.Store,
	httpClient *http.Client,
) federationSpokeStartup {
	if cfg == nil || cfg.Fleet.RoleOrDefault() != config.FleetRoleSpoke {
		return federationSpokeStartup{State: federationStartupHub}
	}
	fail := func(state federationSpokeStartupState, reason string) federationSpokeStartup {
		return federationSpokeStartup{State: state, Reason: reason}
	}
	if cfg.Fleet.Hub == nil {
		return fail(federationStartupActionRequired, "fleet spoke hub binding is missing")
	}
	local, ok := enrollments.Local()
	if !ok || local.Preparation == nil {
		return fail(federationStartupActionRequired, "fleet spoke preparation seal is missing")
	}
	if local.ProtocolVersion != federation.ProtocolVersion ||
		local.Preparation.ProtocolVersion != federation.ProtocolVersion {
		return fail(federationStartupIncompatible, "fleet spoke enrollment protocol is incompatible")
	}
	hub := cfg.Fleet.Hub
	if local.State == federation.EnrollmentRevoked || local.NodeID != nodeID ||
		local.SpokeBaseURL != cfg.Fleet.BaseURL ||
		local.HubID != hub.NodeID ||
		local.HubURL != hub.BaseURL ||
		local.Preparation.EnrollmentID != local.EnrollmentID ||
		local.Preparation.NodeID != local.NodeID ||
		local.Preparation.HubID != local.HubID {
		return fail(federationStartupActionRequired, "fleet spoke config does not match its sealed enrollment")
	}
	if database == nil {
		return fail(federationStartupActionRequired, "fleet spoke preparation database is unavailable")
	}
	preparation, err := database.GetSpokePreparation(ctx)
	if err != nil || preparation.Phase != db.SpokePreparationSealed ||
		preparation.EnrollmentID != local.EnrollmentID ||
		preparation.LocalNodeID != local.NodeID ||
		preparation.HubNodeID != local.HubID ||
		preparation.ProtocolVersion != local.ProtocolVersion ||
		preparation.PreparationDigest != local.Preparation.PreparationDigest ||
		preparation.PreparationSeal != local.Preparation.Seal {
		return fail(federationStartupActionRequired, "fleet spoke preparation state does not match its sealed enrollment")
	}
	credential, ok := credentials.Outbound(local.HubID)
	if !ok || !slices.Contains(credential.Scopes, federationauth.ScopeEnrollmentActivate) {
		return fail(federationStartupActionRequired, "fleet spoke hub credential is unavailable")
	}
	if local.State == federation.EnrollmentActive {
		if err := promoteFederationSpokeCredentialScopes(
			credentials, local.HubID,
		); err != nil {
			return fail(
				federationStartupActionRequired,
				"repair fleet spoke credential scopes: "+err.Error(),
			)
		}
		return federationSpokeStartup{State: federationStartupActive}
	}
	if !cfg.Fleet.Enabled {
		return fail(
			federationStartupActionRequired,
			"fleet spoke activation cannot complete while federation is disabled",
		)
	}

	client := spokeActivationHTTPClient(httpClient)
	for attempt := range spokeActivationAttempts {
		activationErr := validateAndActivateFederationSpoke(
			ctx, client, local, credential,
		)
		if activationErr == nil {
			if err := enrollments.MarkLocalActive(ctx, local.EnrollmentID); err != nil {
				return fail(federationStartupActionRequired, "persist fleet spoke activation: "+err.Error())
			}
			if err := promoteFederationSpokeCredentialScopes(
				credentials, local.HubID,
			); err != nil {
				return fail(federationStartupActionRequired, "activate federation credentials: "+err.Error())
			}
			return federationSpokeStartup{State: federationStartupActive}
		}
		if errors.Is(activationErr, errHubProtocolMismatch) {
			return fail(federationStartupIncompatible, activationErr.Error())
		}
		if !isRetryableSpokeActivationError(activationErr) || attempt+1 == spokeActivationAttempts {
			return fail(
				federationStartupActionRequired,
				"fleet spoke hub activation failed: "+activationErr.Error(),
			)
		}
		delay := time.Duration(1<<attempt) * 100 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			ctxErr := ctx.Err()
			if ctxErr == nil {
				ctxErr = context.Canceled
			}
			return fail(
				federationStartupActionRequired,
				"fleet spoke hub activation failed: "+ctxErr.Error(),
			)
		case <-timer.C:
		}
	}
	return fail(federationStartupActionRequired, "fleet spoke activation retry loop exhausted")
}

func validateFederationHubOrigin(
	cfg *config.Config, enrollments *federation.Store,
) error {
	if cfg == nil || cfg.Fleet.RoleOrDefault() != config.FleetRoleHub ||
		enrollments == nil {
		return nil
	}
	for _, enrollment := range enrollments.List() {
		if enrollment.State == federation.EnrollmentRevoked {
			continue
		}
		if enrollment.HubURL != cfg.Fleet.BaseURL {
			return errors.New(
				"fleet.base_url does not match an enrolled member; revoke all members before changing the hub origin",
			)
		}
	}
	return nil
}

func promoteFederationSpokeCredentialScopes(
	credentials *federationauth.Store, hubID string,
) error {
	if err := credentials.UpdateInboundScopes(
		hubID, federationauth.HubToSpokeScopes(),
	); err != nil {
		return fmt.Errorf("promote hub credential: %w", err)
	}
	if err := credentials.UpdateOutboundScopes(
		hubID, federationauth.SpokeToHubScopes(),
	); err != nil {
		return fmt.Errorf("promote spoke credential: %w", err)
	}
	return nil
}

type spokeActivationHTTPError struct {
	status int
}

func (e *spokeActivationHTTPError) Error() string {
	return fmt.Sprintf("hub returned HTTP %d", e.status)
}

func isRetryableSpokeActivationError(err error) bool {
	if responseErr, ok := errors.AsType[*spokeActivationHTTPError](err); ok {
		return responseErr.status >= http.StatusInternalServerError
	}
	return true
}

func validateAndActivateFederationSpoke(
	ctx context.Context,
	client *http.Client,
	local federation.LocalEnrollment,
	credential federationauth.Credential,
) error {
	var identity struct {
		NodeID          string `json:"node_id"`
		ProtocolVersion int    `json:"protocol_version"`
	}
	if err := doSpokeActivationJSON(
		ctx, client, http.MethodGet,
		local.HubURL+"/api/v1/federation/identity",
		local.NodeID, credential.Token, nil, &identity,
	); err != nil {
		return err
	}
	if identity.ProtocolVersion != federation.ProtocolVersion {
		return fmt.Errorf(
			"%w: expected %d, got %d",
			errHubProtocolMismatch,
			federation.ProtocolVersion, identity.ProtocolVersion,
		)
	}
	if identity.NodeID != local.HubID {
		return errors.New("hub identity does not match the sealed enrollment")
	}
	var active federation.Enrollment
	if err := doSpokeActivationJSON(
		ctx, client, http.MethodPost,
		local.HubURL+"/api/v1/federation/enrollments/"+
			local.EnrollmentID+"/activate",
		local.NodeID, credential.Token,
		map[string]any{
			"protocol_version": federation.ProtocolVersion,
			"preparation_seal": local.Preparation.Seal,
		},
		&active,
	); err != nil {
		return err
	}
	if active.ID != local.EnrollmentID || active.NodeID != local.NodeID ||
		active.HubID != local.HubID ||
		active.ProtocolVersion != federation.ProtocolVersion ||
		active.State != federation.EnrollmentActive {
		return errors.New("hub activation response does not match the sealed enrollment")
	}
	return nil
}

func doSpokeActivationJSON(
	ctx context.Context,
	client *http.Client,
	method, endpoint, nodeID, token string,
	body any,
	target any,
) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode federation activation request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build federation activation request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(federationauth.NodeIDHeader, nodeID)
	request.Header.Set(providerplane.ProtocolVersionHeader, providerplane.ProtocolVersionHeaderValue())
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(
		response.Body, maxSpokeActivationResponseBytes+1,
	))
	if err != nil {
		return fmt.Errorf("read federation activation response: %w", err)
	}
	if len(contents) > maxSpokeActivationResponseBytes {
		return errors.New("federation activation response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if response.StatusCode == http.StatusConflict &&
			strings.Contains(string(contents), "protocolMismatch") {
			return errHubProtocolMismatch
		}
		return &spokeActivationHTTPError{status: response.StatusCode}
	}
	if err := json.Unmarshal(contents, target); err != nil {
		return fmt.Errorf("decode federation activation response: %w", err)
	}
	return nil
}

func spokeActivationHTTPClient(base *http.Client) *http.Client {
	client := &http.Client{Timeout: 10 * time.Second}
	if base != nil {
		*client = *base
		if client.Timeout <= 0 || client.Timeout > 10*time.Second {
			client.Timeout = 10 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client
}

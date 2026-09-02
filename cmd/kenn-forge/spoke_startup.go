package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	spokeActivationRenewalInterval  = 6 * time.Hour
	spokeActivationRetryInterval    = time.Minute
	spokeActivationClockSkew        = 5 * time.Minute
)

var (
	errHubProtocolMismatch      = errors.New("hub federation protocol is incompatible")
	errHubActivationInvalid     = errors.New("hub federation activation response is invalid")
	errSpokeActivationSuspended = errors.New("fleet spoke activation renewal is suspended")
	errSpokeActivationInactive  = errors.New("fleet spoke enrollment is inactive")
)

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
	if local.State == federation.EnrollmentActive && !cfg.Fleet.Enabled {
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
		activationValidUntil, activationErr := validateAndActivateFederationSpoke(
			ctx, client, local, credential,
		)
		if activationErr == nil {
			if err := enrollments.MarkLocalActive(
				ctx, local.EnrollmentID, activationValidUntil,
			); err != nil {
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
			if err := suspendFederationSpokeActivation(
				ctx, enrollments, credentials, local,
			); err != nil {
				slog.Error(
					"suspend incompatible fleet spoke activation",
					"activation_err", activationErr, "err", err,
				)
			}
			return fail(federationStartupIncompatible, activationErr.Error())
		}
		if !isRetryableSpokeActivationError(activationErr) || attempt+1 == spokeActivationAttempts {
			if isRetryableSpokeActivationError(activationErr) &&
				local.State == federation.EnrollmentActive &&
				local.ActivationValidUntil.After(time.Now().UTC()) {
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
			if !isRetryableSpokeActivationError(activationErr) {
				if err := suspendFederationSpokeActivation(
					ctx, enrollments, credentials, local,
				); err != nil {
					return fail(
						federationStartupActionRequired,
						"invalidate fleet spoke activation lease: "+err.Error(),
					)
				}
			}
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
	if errors.Is(err, errHubProtocolMismatch) ||
		errors.Is(err, errHubActivationInvalid) {
		return false
	}
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
) (time.Time, error) {
	var identity struct {
		NodeID          string `json:"node_id"`
		ProtocolVersion int    `json:"protocol_version"`
	}
	if err := doSpokeActivationJSON(
		ctx, client, http.MethodGet,
		local.HubURL+"/api/v1/federation/identity",
		local.NodeID, credential.Token, nil, &identity,
	); err != nil {
		return time.Time{}, err
	}
	if identity.ProtocolVersion != federation.ProtocolVersion {
		return time.Time{}, fmt.Errorf(
			"%w: expected %d, got %d",
			errHubProtocolMismatch,
			federation.ProtocolVersion, identity.ProtocolVersion,
		)
	}
	if identity.NodeID != local.HubID {
		return time.Time{}, fmt.Errorf(
			"%w: hub identity does not match the sealed enrollment",
			errHubActivationInvalid,
		)
	}
	var active federation.Enrollment
	if err := doSpokeActivationJSON(
		ctx, client, http.MethodPost,
		local.HubURL+"/api/v1/federation/enrollments/"+
			local.EnrollmentID+"/activate",
		local.NodeID, credential.Token,
		map[string]any{
			"protocol_version":         federation.ProtocolVersion,
			"activation_lease_version": federation.ActivationLeaseVersion,
			"preparation_seal":         local.Preparation.Seal,
		},
		&active,
	); err != nil {
		return time.Time{}, err
	}
	if active.ID != local.EnrollmentID || active.NodeID != local.NodeID ||
		active.HubID != local.HubID ||
		active.ProtocolVersion != federation.ProtocolVersion ||
		active.ActivationLeaseVersion != federation.ActivationLeaseVersion ||
		active.State != federation.EnrollmentActive {
		return time.Time{}, fmt.Errorf(
			"%w: response does not match the sealed enrollment",
			errHubActivationInvalid,
		)
	}
	now := time.Now().UTC()
	validUntil := active.ActivationValidUntil.UTC()
	if !validUntil.After(now) || validUntil.After(
		now.Add(federation.SpokeActivationLeaseDuration+spokeActivationClockSkew),
	) {
		return time.Time{}, fmt.Errorf(
			"%w: response has an invalid lease", errHubActivationInvalid,
		)
	}
	return validUntil, nil
}

func maintainFederationSpokeActivation(
	ctx context.Context,
	enrollments *federation.Store,
	credentials *federationauth.Store,
	httpClient *http.Client,
) {
	timer := time.NewTimer(nextSpokeActivationRenewal(enrollments, time.Now().UTC()))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		delay := spokeActivationRenewalInterval
		if err := renewFederationSpokeActivation(
			ctx, enrollments, credentials, spokeActivationHTTPClient(httpClient),
		); err != nil {
			if errors.Is(err, errSpokeActivationSuspended) ||
				errors.Is(err, errSpokeActivationInactive) {
				slog.Warn("stop fleet spoke activation lease renewal", "err", err)
				return
			}
			delay = spokeActivationRetryInterval
			slog.Warn("renew fleet spoke activation lease", "err", err)
		}
		timer.Reset(delay)
	}
}

func nextSpokeActivationRenewal(
	enrollments *federation.Store, now time.Time,
) time.Duration {
	if enrollments == nil {
		return spokeActivationRetryInterval
	}
	local, ok := enrollments.Local()
	if !ok || !local.ActivationValidUntil.After(now) {
		return spokeActivationRetryInterval
	}
	delay := local.ActivationValidUntil.Sub(now) / 2
	if delay > spokeActivationRenewalInterval {
		return spokeActivationRenewalInterval
	}
	return max(delay, time.Second)
}

func renewFederationSpokeActivation(
	ctx context.Context,
	enrollments *federation.Store,
	credentials *federationauth.Store,
	client *http.Client,
) error {
	local, ok := enrollments.Local()
	if !ok || local.State != federation.EnrollmentActive {
		return errSpokeActivationInactive
	}
	if local.Preparation == nil {
		return errors.New("active sealed fleet spoke enrollment is unavailable")
	}
	credential, ok := credentials.Outbound(local.HubID)
	if !ok || !slices.Contains(credential.Scopes, federationauth.ScopeEnrollmentActivate) {
		return errors.New("fleet spoke hub credential is unavailable")
	}
	validUntil, err := validateAndActivateFederationSpoke(ctx, client, local, credential)
	if err != nil {
		if !isRetryableSpokeActivationError(err) {
			if suspendErr := suspendFederationSpokeActivation(
				ctx, enrollments, credentials, local,
			); suspendErr != nil {
				return errors.Join(err, suspendErr)
			}
			return errors.Join(errSpokeActivationSuspended, err)
		}
		return err
	}
	if err := enrollments.RenewLocalActivationLease(
		ctx, local.EnrollmentID, validUntil,
	); err != nil {
		return fmt.Errorf("persist fleet spoke activation lease: %w", err)
	}
	if err := promoteFederationSpokeCredentialScopes(credentials, local.HubID); err != nil {
		return fmt.Errorf("restore fleet spoke credential scopes: %w", err)
	}
	current, ok := enrollments.Local()
	if !ok || current.EnrollmentID != local.EnrollmentID ||
		current.State != federation.EnrollmentActive ||
		current.ActivationLeaseVersion != federation.ActivationLeaseVersion ||
		!current.ActivationValidUntil.After(time.Now().UTC()) {
		if err := suspendFederationSpokeActivation(
			ctx, enrollments, credentials, local,
		); err != nil {
			return errors.Join(errSpokeActivationSuspended, err)
		}
		return errSpokeActivationSuspended
	}
	return nil
}

func suspendFederationSpokeActivation(
	ctx context.Context,
	enrollments *federation.Store,
	credentials *federationauth.Store,
	local federation.LocalEnrollment,
) error {
	return errors.Join(
		enrollments.InvalidateLocalActivationLease(ctx, local.EnrollmentID),
		credentials.UpdateInboundScopes(
			local.HubID, federationauth.PendingHubToSpokeScopes(),
		),
		credentials.UpdateOutboundScopes(
			local.HubID, federationauth.PendingSpokeToHubScopes(),
		),
	)
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

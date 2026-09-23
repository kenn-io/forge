package server

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"go.kenn.io/forge/internal/apiclient/generated"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
)

func (s *Server) registerSpokePreparationAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "prepare-federation-spoke",
		Method:      http.MethodPost,
		Path:        "/fleet/prepare-spoke",
		Summary:     "Quiesce this daemon and prepare it to become a fleet spoke",
		Tags:        []string{"Fleet"},
	}, s.prepareFederationSpoke)
	huma.Register(api, huma.Operation{
		OperationID: "abort-federation-spoke-preparation",
		Method:      http.MethodPost,
		Path:        "/fleet/prepare-spoke/abort",
		Summary:     "Abort pending spoke preparation and restore standalone writes",
		Tags:        []string{"Fleet"},
	}, s.abortFederationSpokePreparation)
}

func (s *Server) abortFederationSpokePreparation(
	ctx context.Context, input *spokeapi.AbortFederationSpokeInput,
) (*spokeapi.AbortFederationSpokeOutput, error) {
	if s.options.FederationEnrollments == nil ||
		s.options.FederationCredentials == nil {
		return nil, httpapi.ServiceUnavailable("federation enrollment is unavailable")
	}
	local, ok := s.options.FederationEnrollments.Local()
	if !ok || local.State != federation.EnrollmentPending || local.HubID == "" {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict, "a pending hub enrollment is required",
			map[string]any{"reason": "pendingEnrollmentRequired"},
		)
	}
	if err := s.providerWriteGate.CanAbortPreparation(); err != nil {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict, "cannot reopen provider writes: "+err.Error(),
			map[string]any{"reason": "providerWritesStillDraining"},
		)
	}
	report := spokeapi.SpokePreparationAbortReport{EnrollmentID: local.EnrollmentID}
	hubCleanupPending := false
	if local.PreparationStarted || local.ExpiresAt.After(s.now().UTC()) {
		if err := s.requestHubEnrollmentAbort(ctx, local); err != nil {
			if !input.Body.Force {
				return nil, spokeapi.SpokePreparationHubProblem(err)
			}
			hubCleanupPending = true
		} else {
			report.HubRevoked = true
		}
	}
	if err := s.providerWriteGate.AbortPreparation(ctx); err != nil {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict, "cannot reopen provider writes: "+err.Error(),
			map[string]any{"reason": "providerWritesStillDraining"},
		)
	}
	report.RestartRequired = s.providerRouteSpoke
	report.ProviderWritesOpen = !report.RestartRequired
	if err := s.settingsapi.ResetPreparedSpokeBinding(ctx); err != nil {
		return nil, httpapi.Internal("restore standalone fleet role: " + err.Error())
	}
	if err := s.options.FederationCredentials.RevokeOutbound(local.HubID); err != nil {
		return nil, httpapi.Internal("revoke hub credential: " + err.Error())
	}
	if hubCleanupPending {
		local.State = federation.EnrollmentRevoked
		if err := s.options.FederationEnrollments.SaveLocal(ctx, local); err != nil {
			return nil, httpapi.Internal("retain local revocation tombstone: " + err.Error())
		}
		if err := s.options.FederationCredentials.UpdateInboundScopes(
			local.HubID,
			[]federationauth.Scope{federationauth.ScopeEnrollmentActivate},
		); err != nil {
			return nil, httpapi.Internal("retain federation revocation credential: " + err.Error())
		}
		return &spokeapi.AbortFederationSpokeOutput{Body: report}, nil
	}
	if err := s.options.FederationCredentials.RevokeInboundNode(local.HubID); err != nil {
		return nil, httpapi.Internal("revoke inbound hub credential: " + err.Error())
	}
	if err := s.options.FederationEnrollments.ClearLocal(ctx); err != nil {
		return nil, httpapi.Internal("clear local enrollment: " + err.Error())
	}
	return &spokeapi.AbortFederationSpokeOutput{Body: report}, nil
}

func (s *Server) prepareFederationSpoke(
	ctx context.Context,
	_ *struct{},
) (*spokeapi.PrepareFederationSpokeOutput, error) {
	if s.options.FederationEnrollments == nil ||
		s.options.FederationCredentials == nil ||
		s.options.FederationSpokeID == "" {
		return nil, httpapi.ServiceUnavailable("federation enrollment is unavailable")
	}
	local, ok := s.options.FederationEnrollments.Local()
	if !ok || local.State != federation.EnrollmentPending ||
		local.HubID == "" {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict,
			"a pending hub enrollment is required before spoke preparation",
			map[string]any{"reason": "pendingEnrollmentRequired"},
		)
	}
	if err := s.pinHubEnrollment(ctx, local); err != nil {
		return nil, spokeapi.SpokePreparationHubProblem(err)
	}
	if _, err := s.providerWriteGate.BeginQuiesce(ctx, db.SpokePreparationBinding{
		EnrollmentID:    local.EnrollmentID,
		HubNodeID:       local.HubID,
		LocalNodeID:     local.NodeID,
		ProtocolVersion: local.ProtocolVersion,
	}); err != nil {
		if errors.Is(err, db.ErrSpokePreparationConflict) {
			return nil, httpapi.Conflict(httpapi.CodeConflict, err.Error(), map[string]any{
				"reason": "spokePreparationConflict",
			})
		}
		return nil, httpapi.Internal("begin spoke preparation: " + err.Error())
	}

	report := spokeapi.SpokePreparationReport{
		Unprepared:       []db.UnpreparedWorkspace{},
		HandoffConflicts: []db.ProviderStateConflict{},
		HandoffErrors:    []string{},
	}
	status, err := s.providerWriteGate.Status(ctx)
	if err != nil {
		return nil, httpapi.Internal("read spoke preparation status: " + err.Error())
	}
	report.InFlightProviderWrites = status.InFlightProviderWrites
	report.ActiveDeferredMerges = status.ActiveDeferredMerges
	report.UndrainedAcks = status.UndrainedAcks
	if status.InFlightProviderWrites != 0 || status.ActiveDeferredMerges != 0 ||
		status.DrainAckGeneration == nil {
		return &spokeapi.PrepareFederationSpokeOutput{Body: report}, nil
	}
	client, err := s.spokePreparationProviderClient(local)
	if err != nil {
		report.HandoffErrors = append(report.HandoffErrors, err.Error())
	} else {
		s.spokeapi.ReconcileSpokePreparationProjects(ctx, client, &report)
		s.spokeapi.RefreshSpokePreparationLaunchSpecs(ctx, client, &report)
		s.spokeapi.HandoffSpokeProviderState(ctx, client, &report)
	}

	report.Unprepared, err = s.db.ListUnpreparedProviderWorkspacesAt(ctx, s.now().UTC())
	if err != nil {
		return nil, httpapi.Internal("list unprepared workspaces: " + err.Error())
	}
	total, err := s.db.CountProviderBackedWorkspaces(ctx)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	report.ReadyLaunchSpecs = total - len(report.Unprepared)
	status, err = s.providerWriteGate.Status(ctx)
	if err != nil {
		return nil, httpapi.Internal("read spoke preparation status: " + err.Error())
	}
	report.InFlightProviderWrites = status.InFlightProviderWrites
	report.ActiveDeferredMerges = status.ActiveDeferredMerges
	report.UndrainedAcks = status.UndrainedAcks
	if len(report.Unprepared) != 0 || len(report.HandoffConflicts) != 0 ||
		len(report.HandoffErrors) != 0 || status.InFlightProviderWrites != 0 ||
		status.ActiveDeferredMerges != 0 || status.UndrainedAcks != 0 ||
		status.DrainAckGeneration == nil {
		return &spokeapi.PrepareFederationSpokeOutput{Body: report}, nil
	}
	receipts, err := s.db.ListSpokePreparationReceipts(ctx)
	if err != nil {
		return nil, httpapi.Internal("list provider state receipts: " + err.Error())
	}
	receiptsDigest, err := db.SpokePreparationReceiptsDigest(receipts)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	sealRequest := db.SpokePreparationSealRequest{
		EnrollmentID: local.EnrollmentID, NodeID: local.NodeID,
		HubNodeID:            local.HubID,
		ProtocolVersion:      local.ProtocolVersion,
		MigrationVersion:     db.WorkspaceLaunchSpecMigrationVersion,
		ReceiptsDigest:       receiptsDigest,
		DrainedAckGeneration: *status.DrainAckGeneration,
	}
	sealRequest.PreparationDigest, err = db.SpokePreparationSealDigest(sealRequest)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	seal, err := s.requestHubPreparationSeal(ctx, local, sealRequest)
	if err != nil {
		report.HandoffErrors = append(report.HandoffErrors, err.Error())
		return &spokeapi.PrepareFederationSpokeOutput{Body: report}, nil
	}
	if err := spokeapi.ValidateHubPreparationSeal(sealRequest, seal); err != nil {
		report.HandoffErrors = append(report.HandoffErrors, err.Error())
		return &spokeapi.PrepareFederationSpokeOutput{Body: report}, nil
	}
	if err := s.db.StoreLocalSpokePreparationSeal(
		ctx, sealRequest.PreparationDigest, seal.Seal,
	); err != nil {
		return nil, httpapi.Internal("store spoke preparation seal: " + err.Error())
	}
	if err := s.options.FederationEnrollments.SaveLocalPreparationSeal(
		ctx, federation.LocalPreparationSeal{
			EnrollmentID: seal.EnrollmentID, NodeID: seal.NodeID,
			HubID:             seal.HubNodeID,
			ProtocolVersion:   seal.ProtocolVersion,
			PreparationDigest: seal.PreparationDigest,
			Seal:              seal.Seal,
		},
	); err != nil {
		return nil, httpapi.Internal("persist spoke preparation seal: " + err.Error())
	}
	if err := s.persistPreparedSpokeRole(ctx, local, seal); err != nil {
		return nil, httpapi.Internal("persist fleet spoke role: " + err.Error())
	}
	report.ReadyToActivate = true
	report.PreparationSeal = seal.Seal
	report.RestartRequired = true
	return &spokeapi.PrepareFederationSpokeOutput{Body: report}, nil
}

func (s *Server) persistPreparedSpokeRole(
	ctx context.Context,
	local federation.LocalEnrollment,
	seal db.SpokePreparationSeal,
) error {
	prepared, ok := s.options.FederationEnrollments.Local()
	if !ok || prepared.State != federation.EnrollmentPending || prepared.Preparation == nil {
		return errors.New("a sealed pending local enrollment is required before changing fleet role")
	}
	if prepared.EnrollmentID != local.EnrollmentID || prepared.NodeID != local.NodeID ||
		prepared.HubID != local.HubID ||
		prepared.ProtocolVersion != federation.ProtocolVersion ||
		prepared.Preparation.EnrollmentID != seal.EnrollmentID ||
		prepared.Preparation.NodeID != seal.NodeID ||
		prepared.Preparation.HubID != seal.HubNodeID ||
		prepared.Preparation.ProtocolVersion != seal.ProtocolVersion ||
		prepared.Preparation.Seal != seal.Seal {
		return federation.ErrPreparationSealMismatch
	}
	return s.settingsapi.MutatePersistedEnrollmentFleetChecked(ctx, func(fleet *config.Fleet) error {
		if !fleet.Enabled || fleet.Hub == nil ||
			fleet.Hub.NodeID != prepared.HubID ||
			fleet.Hub.BaseURL != prepared.HubURL {
			return errors.New("fleet hub config does not match the sealed local enrollment")
		}
		if len(fleet.Members) != 0 {
			return errors.New("revoke or migrate hub members before changing fleet role to spoke")
		}
		fleet.Role = config.FleetRoleSpoke
		fleet.Members = nil
		return nil
	})
}

func (s *Server) spokePreparationProviderClient(
	local federation.LocalEnrollment,
) (providerplane.Client, error) {
	return providerplane.NewClient(providerplane.Options{
		LocalNodeID: local.NodeID,
		Hub: providerplane.Hub{
			NodeID: local.HubID, BaseURL: local.HubURL,
		},
		Credentials: s.options.FederationCredentials,
		HTTPClient:  s.options.FederationHTTPClient,
	})
}

func (s *Server) pinHubEnrollment(
	ctx context.Context,
	local federation.LocalEnrollment,
) error {
	var response federation.Enrollment
	httpRequest, err := generated.NewBeginFederationSpokePreparationRequest(ctx, local.HubURL+"/api/v1", &generated.BeginFederationSpokePreparationRequestOptions{PathParams: &generated.BeginFederationSpokePreparationPath{EnrollmentID: local.EnrollmentID}})
	if err != nil {
		return err
	}
	if err := s.postHubEnrollmentJSON(ctx, local, httpRequest, &response); err != nil {
		return err
	}
	if response.ID != local.EnrollmentID || response.NodeID != local.NodeID ||
		response.HubID != local.HubID ||
		response.State != federation.EnrollmentPending || !response.PreparationStarted {
		return errors.New("hub pinned a different enrollment")
	}
	return s.options.FederationEnrollments.MarkLocalPreparationStarted(
		ctx, local.EnrollmentID,
	)
}

func (s *Server) requestHubPreparationSeal(
	ctx context.Context,
	local federation.LocalEnrollment,
	request db.SpokePreparationSealRequest,
) (db.SpokePreparationSeal, error) {
	body := &generated.SealFederationSpokePreparationBody{
		NodeID:               request.NodeID,
		HubNodeID:            request.HubNodeID,
		ProtocolVersion:      int64(request.ProtocolVersion),
		MigrationVersion:     int64(request.MigrationVersion),
		ReceiptsDigest:       request.ReceiptsDigest,
		DrainedAckGeneration: request.DrainedAckGeneration,
		PreparationDigest:    request.PreparationDigest,
	}
	var seal db.SpokePreparationSeal
	httpRequest, err := generated.NewSealFederationSpokePreparationRequest(ctx, local.HubURL+"/api/v1", &generated.SealFederationSpokePreparationRequestOptions{PathParams: &generated.SealFederationSpokePreparationPath{EnrollmentID: local.EnrollmentID}, Body: body})
	if err != nil {
		return db.SpokePreparationSeal{}, err
	}
	err = s.postHubEnrollmentJSON(ctx, local, httpRequest, &seal)
	return seal, err
}

func (s *Server) requestHubEnrollmentAbort(
	ctx context.Context, local federation.LocalEnrollment,
) error {
	var response struct{}
	httpRequest, err := generated.NewAbortFederationEnrollmentRequest(ctx, local.HubURL+"/api/v1", &generated.AbortFederationEnrollmentRequestOptions{PathParams: &generated.AbortFederationEnrollmentPath{EnrollmentID: local.EnrollmentID}})
	if err != nil {
		return err
	}
	return s.postHubEnrollmentJSON(ctx, local, httpRequest, &response)
}

func (s *Server) postHubEnrollmentJSON(
	ctx context.Context,
	local federation.LocalEnrollment,
	request *http.Request,
	target any,
) error {
	credential, ok := s.options.FederationCredentials.Outbound(local.HubID)
	if !ok || !slices.Contains(credential.Scopes, federationauth.ScopeEnrollmentActivate) {
		return providerplane.ErrCredentialUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+credential.Token)
	request.Header.Set(federationauth.NodeIDHeader, local.NodeID)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	if s.options.FederationHTTPClient != nil {
		*client = *s.options.FederationHTTPClient
		if client.Timeout <= 0 || client.Timeout > 15*time.Second {
			client.Timeout = 15 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %w", providerplane.ErrHubUnavailable, err)
	}
	defer response.Body.Close()
	encodedResponse, err := io.ReadAll(io.LimitReader(
		response.Body, spokeapi.MaxSpokePreparationResponseBytes+1,
	))
	if err != nil {
		return err
	}
	if len(encodedResponse) > spokeapi.MaxSpokePreparationResponseBytes {
		return providerplane.ErrResponseBodyTooLarge
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var problem httpapi.ProblemError
		if json.Unmarshal(encodedResponse, &problem) == nil && problem.Status != 0 {
			return &problem
		}
		return fmt.Errorf("hub returned HTTP %d", response.StatusCode)
	}
	if target == nil || len(encodedResponse) == 0 {
		return nil
	}
	return json.Unmarshal(encodedResponse, target)
}

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
)

const maxSpokePreparationResponseBytes = 1 << 20

type SpokePreparationReport struct {
	ReadyLaunchSpecs       int                        `json:"ready_launch_specs"`
	Unprepared             []db.UnpreparedWorkspace   `json:"unprepared" nullable:"false"`
	HandoffConflicts       []db.ProviderStateConflict `json:"handoff_conflicts" nullable:"false"`
	HandoffErrors          []string                   `json:"handoff_errors" nullable:"false"`
	InFlightProviderWrites int                        `json:"in_flight_provider_writes"`
	ActiveDeferredMerges   int                        `json:"active_deferred_merges"`
	UndrainedAcks          int                        `json:"undrained_acks"`
	ReadyToActivate        bool                       `json:"ready_to_activate"`
	PreparationSeal        string                     `json:"preparation_seal,omitempty"`
	RestartRequired        bool                       `json:"restart_required"`
}

type prepareFederationSpokeOutput = httpapi.BodyOutput[SpokePreparationReport]

type abortFederationSpokeInput struct {
	Body struct {
		Force bool `json:"force,omitempty"`
	}
}

type SpokePreparationAbortReport struct {
	EnrollmentID       string `json:"enrollment_id"`
	HubRevoked         bool   `json:"hub_revoked"`
	ProviderWritesOpen bool   `json:"provider_writes_open"`
	RestartRequired    bool   `json:"restart_required"`
}

type abortFederationSpokeOutput = httpapi.BodyOutput[SpokePreparationAbortReport]

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
	ctx context.Context, input *abortFederationSpokeInput,
) (*abortFederationSpokeOutput, error) {
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
	report := SpokePreparationAbortReport{EnrollmentID: local.EnrollmentID}
	hubCleanupPending := false
	if local.PreparationStarted || local.ExpiresAt.After(s.now().UTC()) {
		if err := s.requestHubEnrollmentAbort(ctx, local); err != nil {
			if !input.Body.Force {
				return nil, spokePreparationHubProblem(err)
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
	if err := s.resetPreparedSpokeBinding(ctx); err != nil {
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
		return &abortFederationSpokeOutput{Body: report}, nil
	}
	if err := s.options.FederationCredentials.RevokeInboundNode(local.HubID); err != nil {
		return nil, httpapi.Internal("revoke inbound hub credential: " + err.Error())
	}
	if err := s.options.FederationEnrollments.ClearLocal(ctx); err != nil {
		return nil, httpapi.Internal("clear local enrollment: " + err.Error())
	}
	return &abortFederationSpokeOutput{Body: report}, nil
}

func (s *Server) prepareFederationSpoke(
	ctx context.Context,
	_ *struct{},
) (*prepareFederationSpokeOutput, error) {
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
		return nil, spokePreparationHubProblem(err)
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

	report := SpokePreparationReport{
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
		return &prepareFederationSpokeOutput{Body: report}, nil
	}
	client, err := s.spokePreparationProviderClient(local)
	if err != nil {
		report.HandoffErrors = append(report.HandoffErrors, err.Error())
	} else {
		s.reconcileSpokePreparationProjects(ctx, client, &report)
		s.refreshSpokePreparationLaunchSpecs(ctx, client, &report)
		s.handoffSpokeProviderState(ctx, client, &report)
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
		return &prepareFederationSpokeOutput{Body: report}, nil
	}
	receipts, err := s.db.ListSpokePreparationReceipts(ctx)
	if err != nil {
		return nil, httpapi.Internal("list provider state receipts: " + err.Error())
	}
	receiptsDigest, err := spokePreparationReceiptsDigest(receipts)
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
		return &prepareFederationSpokeOutput{Body: report}, nil
	}
	if err := validateHubPreparationSeal(sealRequest, seal); err != nil {
		report.HandoffErrors = append(report.HandoffErrors, err.Error())
		return &prepareFederationSpokeOutput{Body: report}, nil
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
	return &prepareFederationSpokeOutput{Body: report}, nil
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
	return s.mutatePersistedFleetChecked(ctx, func(fleet *config.Fleet) error {
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

func validateHubPreparationSeal(
	request db.SpokePreparationSealRequest,
	seal db.SpokePreparationSeal,
) error {
	if seal.SpokePreparationSealRequest != request {
		return errors.New("hub returned a preparation seal for a different binding")
	}
	if strings.TrimSpace(seal.Seal) == "" || seal.CreatedAt.IsZero() {
		return errors.New("hub returned an incomplete preparation seal")
	}
	return nil
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

func (s *Server) refreshSpokePreparationLaunchSpecs(
	ctx context.Context,
	client providerplane.Client,
	report *SpokePreparationReport,
) {
	unprepared, err := s.db.ListUnpreparedProviderWorkspacesAt(ctx, s.now().UTC())
	if err != nil {
		report.HandoffErrors = append(report.HandoffErrors, "list launch specifications: "+err.Error())
		return
	}
	for _, item := range unprepared {
		workspace := item.Workspace
		current, err := s.db.GetWorkspaceLaunchSpec(ctx, workspace.ID)
		if err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("read workspace %s launch specification: %v", workspace.ID, err))
			continue
		}
		body := providerplane.WorkspaceLaunchRequest{
			Repository: providerplane.RepositoryRoute{
				Provider: workspace.Platform, PlatformHost: workspace.PlatformHost,
				Owner: workspace.RepoOwner, Name: workspace.RepoName,
			},
			ItemType: workspace.ItemType, ItemNumber: workspace.ItemNumber,
			ItemKey: workspace.ItemKey, GitHeadRef: workspace.GitHeadRef,
			PlatformRepoID: item.PlatformRepoID,
		}
		if current != nil {
			body.PlatformRepoID = current.Repository.PlatformRepoID
		}
		var spec db.WorkspaceLaunchSpec
		if err := spokePreparationProviderJSON(
			ctx, client, federationauth.ScopeProviderRead,
			http.MethodPost, "/api/v1/federation/provider/workspace-launch-spec",
			body, &spec,
		); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("refresh workspace %s: %v", workspace.ID, err))
			continue
		}
		if err := providerplane.ValidateFederationWorkspaceLaunchSpecResponse(body, spec); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("refresh workspace %s: invalid hub launch specification: %v", workspace.ID, err))
			continue
		}
		var credentialErr error
		if s.clones == nil {
			credentialErr = gitclone.ErrCredentialUnavailable
		} else {
			credentialErr = s.clones.RequireCredentialRoute(
				ctx, spec.Repository.Provider, spec.Repository.PlatformHost,
				spec.Repository.Owner, spec.Repository.Name,
			)
		}
		if credentialErr != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("refresh workspace %s: %v", workspace.ID, credentialErr))
			continue
		}
		if _, err := s.db.PutRefreshedWorkspaceLaunchSpec(
			ctx, workspace.ID, spec,
		); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("persist workspace %s launch specification: %v", workspace.ID, err))
		}
	}
}

func (s *Server) reconcileSpokePreparationProjects(
	ctx context.Context,
	client providerplane.Client,
	report *SpokePreparationReport,
) {
	projects, err := s.db.ListProjects(ctx)
	if err != nil {
		report.HandoffErrors = append(report.HandoffErrors, "list registered projects: "+err.Error())
		return
	}
	seen := make(map[providerplane.RepositoryRoute]struct{}, len(projects))
	for _, project := range projects {
		if project.PlatformIdentity == nil {
			continue
		}
		route, err := providerplane.CanonicalRepositoryRoute(providerplane.RepositoryRoute{
			Provider:     project.PlatformIdentity.Platform,
			PlatformHost: project.PlatformIdentity.Host,
			Owner:        project.PlatformIdentity.Owner,
			Name:         project.PlatformIdentity.Name,
		})
		if err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("resolve project %s repository: %v", project.ID, err))
			continue
		}
		if _, ok := seen[route]; ok {
			continue
		}
		seen[route] = struct{}{}
		var descriptor providerplane.RepositoryDescriptor
		if err := spokePreparationProviderJSON(
			ctx, client, federationauth.ScopeProviderRead,
			http.MethodPost, "/api/v1/federation/provider/repository-descriptor",
			route, &descriptor,
		); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("resolve project %s repository: %v", project.ID, err))
			continue
		}
		if err := descriptor.ValidateRoute(route); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("resolve project %s repository: invalid hub descriptor: %v", project.ID, err))
			continue
		}
		if err := observeRepositoryDescriptor(ctx, s.db, descriptor); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("persist project %s repository: %v", project.ID, err))
		}
	}
}

func (s *Server) handoffSpokeProviderState(
	ctx context.Context,
	client providerplane.Client,
	report *SpokePreparationReport,
) {
	records, err := s.db.ListProviderStateForHandoff(ctx)
	if err != nil {
		report.HandoffErrors = append(report.HandoffErrors, "inventory provider state: "+err.Error())
		return
	}
	receipts, err := s.db.ListSpokePreparationReceipts(ctx)
	if err != nil {
		report.HandoffErrors = append(report.HandoffErrors, "read provider state receipts: "+err.Error())
		return
	}
	received := make(map[string]db.SpokePreparationReceipt, len(receipts))
	for _, receipt := range receipts {
		received[receipt.StateKind+"\x00"+receipt.SourceKey] = receipt
	}
	for _, record := range records {
		if receipt, ok := received[record.Kind+"\x00"+record.SourceKey]; ok &&
			receipt.ContentDigest == record.ContentDigest {
			continue
		}
		path := "/api/v1/federation/provider-state/workflow-states/import"
		var body any = record.WorkflowState
		if record.Kind == db.ProviderStateReviewDraft {
			path = "/api/v1/federation/provider-state/review-drafts/import"
			body = record.ReviewDraft
		}
		var result db.ProviderStateImportResult
		if err := spokePreparationProviderJSON(
			ctx, client, federationauth.ScopeProviderHandoff,
			http.MethodPost, path, body, &result,
		); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("handoff %s %s: %v", record.Kind, record.SourceKey, err))
			continue
		}
		if result.Conflict != nil {
			report.HandoffConflicts = append(report.HandoffConflicts, *result.Conflict)
			continue
		}
		if strings.TrimSpace(result.Receipt) == "" {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("handoff %s %s returned no receipt", record.Kind, record.SourceKey))
			continue
		}
		if err := s.db.RecordSpokePreparationReceipt(ctx, db.SpokePreparationReceipt{
			StateKind: record.Kind, SourceKey: record.SourceKey,
			ContentDigest: record.ContentDigest,
			HubReceipt:    result.Receipt, ImportedAt: s.now().UTC(),
		}); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("record %s %s receipt: %v", record.Kind, record.SourceKey, err))
		}
	}
}

func spokePreparationProviderJSON(
	ctx context.Context,
	client providerplane.Client,
	scope federationauth.Scope,
	method string,
	path string,
	body any,
	target any,
) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx, method, "https://hub.invalid"+path, bytes.NewReader(encoded),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	return providerplane.ReadJSON(ctx, client, scope, request, target)
}

func (s *Server) pinHubEnrollment(
	ctx context.Context,
	local federation.LocalEnrollment,
) error {
	var response federation.Enrollment
	if err := s.postHubEnrollmentJSON(
		ctx, local,
		"/api/v1/federation/enrollments/"+local.EnrollmentID+"/preparation/begin",
		map[string]any{}, &response,
	); err != nil {
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
	body := map[string]any{
		"node_id":                request.NodeID,
		"hub_node_id":            request.HubNodeID,
		"protocol_version":       request.ProtocolVersion,
		"migration_version":      request.MigrationVersion,
		"receipts_digest":        request.ReceiptsDigest,
		"drained_ack_generation": request.DrainedAckGeneration,
		"preparation_digest":     request.PreparationDigest,
	}
	var seal db.SpokePreparationSeal
	err := s.postHubEnrollmentJSON(
		ctx, local,
		"/api/v1/federation/enrollments/"+local.EnrollmentID+"/preparation/seal",
		body, &seal,
	)
	return seal, err
}

func (s *Server) requestHubEnrollmentAbort(
	ctx context.Context, local federation.LocalEnrollment,
) error {
	var response struct{}
	return s.postHubEnrollmentJSON(
		ctx, local,
		"/api/v1/federation/enrollments/"+local.EnrollmentID+"/abort",
		struct{}{}, &response,
	)
}

func (s *Server) postHubEnrollmentJSON(
	ctx context.Context,
	local federation.LocalEnrollment,
	path string,
	body any,
	target any,
) error {
	credential, ok := s.options.FederationCredentials.Outbound(local.HubID)
	if !ok || !slices.Contains(credential.Scopes, federationauth.ScopeEnrollmentActivate) {
		return providerplane.ErrCredentialUnavailable
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, local.HubURL+path, bytes.NewReader(encoded),
	)
	if err != nil {
		return err
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
		return fmt.Errorf("%w: %v", providerplane.ErrHubUnavailable, err)
	}
	defer response.Body.Close()
	encodedResponse, err := io.ReadAll(io.LimitReader(
		response.Body, maxSpokePreparationResponseBytes+1,
	))
	if err != nil {
		return err
	}
	if len(encodedResponse) > maxSpokePreparationResponseBytes {
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

func spokePreparationHubProblem(err error) error {
	if errors.Is(err, providerplane.ErrHubUnavailable) ||
		errors.Is(err, providerplane.ErrCredentialUnavailable) {
		return httpapi.HubUnavailable(
			"the hub must be reachable before provider writes can be sealed",
		)
	}
	if problem, ok := errors.AsType[*httpapi.ProblemError](err); ok {
		return problem
	}
	return httpapi.Internal("begin hub spoke preparation: " + err.Error())
}

func spokePreparationReceiptsDigest(
	receipts []db.SpokePreparationReceipt,
) (string, error) {
	type semanticReceipt struct {
		StateKind     string `json:"state_kind"`
		SourceKey     string `json:"source_key"`
		ContentDigest string `json:"content_digest"`
		HubReceipt    string `json:"hub_receipt"`
	}
	semantic := make([]semanticReceipt, 0, len(receipts))
	for _, receipt := range receipts {
		semantic = append(semantic, semanticReceipt{
			StateKind: receipt.StateKind, SourceKey: receipt.SourceKey,
			ContentDigest: receipt.ContentDigest,
			HubReceipt:    receipt.HubReceipt,
		})
	}
	encoded, err := json.Marshal(semantic)
	if err != nil {
		return "", fmt.Errorf("encode spoke preparation receipts: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

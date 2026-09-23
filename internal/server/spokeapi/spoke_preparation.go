package spokeapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
)

const MaxSpokePreparationResponseBytes = 1 << 20

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

type PrepareFederationSpokeOutput = httpapi.BodyOutput[SpokePreparationReport]

type AbortFederationSpokeInput struct {
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

type AbortFederationSpokeOutput = httpapi.BodyOutput[SpokePreparationAbortReport]

func ValidateHubPreparationSeal(
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

func (s *Handlers) RefreshSpokePreparationLaunchSpecs(
	ctx context.Context,
	client providerplane.Client,
	report *SpokePreparationReport,
) {
	unprepared, err := s.Db.ListUnpreparedProviderWorkspacesAt(ctx, (*s.Now)().UTC())
	if err != nil {
		report.HandoffErrors = append(report.HandoffErrors, "list launch specifications: "+err.Error())
		return
	}
	for _, item := range unprepared {
		workspace := item.Workspace
		current, err := s.Db.GetWorkspaceLaunchSpec(ctx, workspace.ID)
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
		httpRequest, requestErr := generated.NewFederationResolveWorkspaceLaunchSpecRequest(ctx, "https://hub.invalid/api/v1", &generated.FederationResolveWorkspaceLaunchSpecRequestOptions{Body: providerLaunchRequestBody(body)})
		if err := spokePreparationProviderJSON(ctx, client, federationauth.ScopeProviderRead, httpRequest, requestErr, &spec); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("refresh workspace %s: %v", workspace.ID, err))
			continue
		}
		if err := providerplane.ValidateFederationWorkspaceLaunchSpecResponse(body, spec); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("refresh workspace %s: invalid hub launch specification: %v", workspace.ID, err))
			continue
		}
		_, credentialErr := requireWorkspaceLaunchSpecCredentials(ctx, s.Clones, spec)
		if credentialErr != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("refresh workspace %s: %v", workspace.ID, credentialErr))
			continue
		}
		if _, err := s.Db.PutRefreshedWorkspaceLaunchSpec(
			ctx, workspace.ID, spec,
		); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("persist workspace %s launch specification: %v", workspace.ID, err))
		}
	}
}

func (s *Handlers) ReconcileSpokePreparationProjects(
	ctx context.Context,
	client providerplane.Client,
	report *SpokePreparationReport,
) {
	projects, err := s.Db.ListProjects(ctx)
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
		httpRequest, requestErr := generated.NewFederationGetRepositoryDescriptorRequest(ctx, "https://hub.invalid/api/v1", &generated.FederationGetRepositoryDescriptorRequestOptions{Body: new(generated.RepositoryRoute{Provider: route.Provider, PlatformHost: route.PlatformHost, Owner: route.Owner, Name: route.Name})})
		if err := spokePreparationProviderJSON(ctx, client, federationauth.ScopeProviderRead, httpRequest, requestErr, &descriptor); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("resolve project %s repository: %v", project.ID, err))
			continue
		}
		if err := descriptor.ValidateRoute(route); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("resolve project %s repository: invalid hub descriptor: %v", project.ID, err))
			continue
		}
		if err := observeRepositoryDescriptor(ctx, s.Db, descriptor); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("persist project %s repository: %v", project.ID, err))
		}
	}
}

func (s *Handlers) HandoffSpokeProviderState(
	ctx context.Context,
	client providerplane.Client,
	report *SpokePreparationReport,
) {
	records, err := s.Db.ListProviderStateForHandoff(ctx)
	if err != nil {
		report.HandoffErrors = append(report.HandoffErrors, "inventory provider state: "+err.Error())
		return
	}
	receipts, err := s.Db.ListSpokePreparationReceipts(ctx)
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
		httpRequest, requestErr := providerStateImportRequest(ctx, record)
		var result db.ProviderStateImportResult
		if err := spokePreparationProviderJSON(ctx, client, federationauth.ScopeProviderHandoff, httpRequest, requestErr, &result); err != nil {
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
		if err := s.Db.RecordSpokePreparationReceipt(ctx, db.SpokePreparationReceipt{
			StateKind: record.Kind, SourceKey: record.SourceKey,
			ContentDigest: record.ContentDigest,
			HubReceipt:    result.Receipt, ImportedAt: (*s.Now)().UTC(),
		}); err != nil {
			report.HandoffErrors = append(report.HandoffErrors,
				fmt.Sprintf("record %s %s receipt: %v", record.Kind, record.SourceKey, err))
		}
	}
}

func spokePreparationProviderJSON(ctx context.Context, client providerplane.Client, scope federationauth.Scope, request *http.Request, requestErr error, target any) error {
	if requestErr != nil {
		return requestErr
	}
	return providerplane.ReadJSON(ctx, client, scope, request, target)
}

func SpokePreparationHubProblem(err error) error {
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

func providerStateImportRequest(ctx context.Context, record db.ProviderStateRecord) (*http.Request, error) {
	if record.Kind == db.ProviderStateReviewDraft {
		var body *generated.FederationImportReviewDraftBody
		if draft := record.ReviewDraft; draft != nil {
			body = &generated.FederationImportReviewDraftBody{
				Repository: generated.ProviderStateRepository{Provider: draft.Repository.Provider, PlatformHost: draft.Repository.PlatformHost, PlatformRepoID: draft.Repository.PlatformRepoID, Owner: draft.Repository.Owner, Name: draft.Repository.Name}, PullNumber: int64(draft.PullNumber), Body: draft.Body, Action: draft.Action,
				Comments: make([]generated.ProviderStateReviewComment, 0, len(draft.Comments)),
			}
			for _, comment := range draft.Comments {
				item := generated.ProviderStateReviewComment{
					Body: comment.Body, Path: comment.Path, OldPath: optionalProviderQuery(comment.OldPath), Side: comment.Side,
					StartSide: optionalProviderQuery(comment.StartSide), Line: int64(comment.Line), LineType: comment.LineType,
					DiffHeadSha: comment.DiffHeadSHA, CommitSha: comment.CommitSHA,
				}
				if comment.StartLine != nil {
					item.StartLine = new(int64(*comment.StartLine))
				}
				if comment.OldLine != nil {
					item.OldLine = new(int64(*comment.OldLine))
				}
				if comment.NewLine != nil {
					item.NewLine = new(int64(*comment.NewLine))
				}
				body.Comments = append(body.Comments, item)
			}
		}
		return generated.NewFederationImportReviewDraftRequest(ctx, "https://hub.invalid/api/v1", &generated.FederationImportReviewDraftRequestOptions{Body: body})
	}
	var body *generated.FederationImportWorkflowStateBody
	if state := record.WorkflowState; state != nil {
		body = &generated.FederationImportWorkflowStateBody{
			Repository: generated.ProviderStateRepository{Provider: state.Repository.Provider, PlatformHost: state.Repository.PlatformHost, PlatformRepoID: state.Repository.PlatformRepoID, Owner: state.Repository.Owner, Name: state.Repository.Name}, ItemType: generated.ProviderStateWorkflowPayloadItemType(state.ItemType),
			ItemNumber: int64(state.ItemNumber), Status: state.Status,
			UpdatedSource: optionalProviderQuery(state.UpdatedSource), UpdatedActor: optionalProviderQuery(state.UpdatedActor), UpdatedReason: optionalProviderQuery(state.UpdatedReason),
		}
	}
	return generated.NewFederationImportWorkflowStateRequest(ctx, "https://hub.invalid/api/v1", &generated.FederationImportWorkflowStateRequestOptions{Body: body})
}

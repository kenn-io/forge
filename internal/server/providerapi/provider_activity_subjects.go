package providerapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

type FederationUnassignedActivitySubjectsRequest struct {
	Subjects []spokeapi.FederationActivitySubjectIdentity `json:"subjects" nullable:"false"`
}

type federationUnassignedActivitySubjectsInput struct {
	Body FederationUnassignedActivitySubjectsRequest
}

func federationActivitySubjectIdentityFromProvider(
	identity providerplane.ItemIdentity,
) spokeapi.FederationActivitySubjectIdentity {
	return spokeapi.FederationActivitySubjectIdentity{
		Repository: spokeapi.FederationActivityRepositoryIdentity{
			Provider:       identity.Repository.Provider,
			PlatformHost:   identity.Repository.PlatformHost,
			PlatformRepoID: identity.Repository.PlatformRepoID,
		},
		ItemType: identity.ItemType, ItemNumber: identity.ItemNumber,
	}
}

type federationUnassignedActivitySubjectsOutput = httpapi.BodyOutput[spokeapi.FederationUnassignedActivitySubjectsResponse]

func (s *Handlers) RegisterProviderActivitySubjectAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "federation-filter-unassigned-activity-subjects",
		Method:      http.MethodPost,
		Path:        "/federation/provider/activity/unassigned-subjects/query",
		Summary:     "Filter activity subjects by hub assignment state",
		Tags:        []string{"Fleet"},
	}, s.federationFilterUnassignedActivitySubjects)
}

func (s *Handlers) federationFilterUnassignedActivitySubjects(
	ctx context.Context,
	input *federationUnassignedActivitySubjectsInput,
) (*federationUnassignedActivitySubjectsOutput, error) {
	identities := make([]providerplane.ItemIdentity, 0, len(input.Body.Subjects))
	seen := make(map[providerplane.ItemIdentity]struct{}, len(input.Body.Subjects))
	for _, wireIdentity := range input.Body.Subjects {
		identity := wireIdentity.Provider().Canonical()
		if !identity.Valid() || (identity.ItemType != "pr" && identity.ItemType != "issue") {
			return nil, httpapi.Validation(
				"body.subjects", "each subject must identify a pull request or issue",
			)
		}
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		identities = append(identities, identity)
	}

	releaseReconciliation, err := s.Db.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, httpapi.Internal("filter activity subjects failed")
	}
	defer releaseReconciliation()

	keys := make([]db.WorkspaceSubjectKey, 0, len(identities))
	identityByKey := make(map[db.WorkspaceSubjectKey]providerplane.ItemIdentity, len(identities))
	repositoryIDs := make(map[providerplane.RepositoryIdentity]int64)
	for _, identity := range identities {
		repositoryIdentity := identity.Repository.Canonical()
		repositoryID, resolved := repositoryIDs[repositoryIdentity]
		if !resolved {
			repository, lookupErr := s.Db.GetRepositoryByProviderIDUnderRepositoryReconciliationRead(
				ctx,
				repositoryIdentity.Provider,
				repositoryIdentity.PlatformHost,
				repositoryIdentity.PlatformRepoID,
			)
			if lookupErr != nil {
				return nil, httpapi.Internal("filter activity subjects failed")
			}
			if repository != nil && repository.Lifecycle == db.RepositoryLifecycleActive {
				repositoryID = repository.Repository.ID
			}
			repositoryIDs[repositoryIdentity] = repositoryID
		}
		if repositoryID == 0 {
			continue
		}
		itemType := db.WorkspaceItemTypePullRequest
		if identity.ItemType == "issue" {
			itemType = db.WorkspaceItemTypeIssue
		}
		key := db.WorkspaceSubjectKey{
			RepoID: repositoryID, ItemType: itemType, ItemNumber: identity.ItemNumber,
		}
		keys = append(keys, key)
		identityByKey[key] = identity
	}
	unassignedKeys, err := s.Db.ListUnassignedWorkspaceSubjectKeys(ctx, keys)
	if err != nil {
		return nil, httpapi.Internal("filter activity subjects failed")
	}
	unassigned := make([]spokeapi.FederationActivitySubjectIdentity, 0, len(unassignedKeys))
	for _, key := range keys {
		if _, ok := unassignedKeys[key]; ok {
			unassigned = append(
				unassigned,
				federationActivitySubjectIdentityFromProvider(identityByKey[key]),
			)
		}
	}
	return &federationUnassignedActivitySubjectsOutput{Body: spokeapi.FederationUnassignedActivitySubjectsResponse{
		Subjects: unassigned,
	}}, nil
}

func (s *Handlers) WorkspaceActivityRepositoryIdentities(
	ctx context.Context, snapshot workspaceapi.WorkspaceSubjectSnapshot,
) (map[int64]providerplane.RepositoryIdentity, error) {
	repositories := make(map[int64]providerplane.RepositoryIdentity, len(snapshot.Subjects))
	for key, activity := range snapshot.Subjects {
		repositories[key.RepoID] = providerplane.RepositoryIdentity{
			Provider:       activity.Subject.Platform,
			PlatformHost:   activity.Subject.PlatformHost,
			PlatformRepoID: activity.Subject.PlatformRepoID,
		}.Canonical()
	}
	for key := range snapshot.OwnReferences {
		if _, ok := repositories[key.RepoID]; ok {
			continue
		}
		repository, err := s.Db.GetRepoByID(ctx, key.RepoID)
		if err != nil {
			return nil, err
		}
		if repository == nil {
			continue
		}
		repositories[key.RepoID] = providerplane.RepositoryIdentity{
			Provider:       repository.Platform,
			PlatformHost:   repository.PlatformHost,
			PlatformRepoID: repository.PlatformRepoID,
		}.Canonical()
	}
	return repositories, nil
}

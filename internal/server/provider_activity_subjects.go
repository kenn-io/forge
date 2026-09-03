package server

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

type federationUnassignedActivitySubjectsRequest struct {
	Subjects []federationActivitySubjectIdentity `json:"subjects" nullable:"false"`
}

type federationUnassignedActivitySubjectsInput struct {
	Body federationUnassignedActivitySubjectsRequest
}

type federationUnassignedActivitySubjectsResponse struct {
	Subjects []federationActivitySubjectIdentity `json:"subjects" nullable:"false"`
}

type federationActivityRepositoryIdentity struct {
	Provider       string `json:"provider"`
	PlatformHost   string `json:"platform_host"`
	PlatformRepoID string `json:"platform_repo_id"`
}

type federationActivitySubjectIdentity struct {
	Repository federationActivityRepositoryIdentity `json:"repository"`
	ItemType   string                               `json:"item_type"`
	ItemNumber int                                  `json:"item_number"`
}

func federationActivitySubjectIdentityFromProvider(
	identity providerplane.ItemIdentity,
) federationActivitySubjectIdentity {
	return federationActivitySubjectIdentity{
		Repository: federationActivityRepositoryIdentity{
			Provider:       identity.Repository.Provider,
			PlatformHost:   identity.Repository.PlatformHost,
			PlatformRepoID: identity.Repository.PlatformRepoID,
		},
		ItemType: identity.ItemType, ItemNumber: identity.ItemNumber,
	}
}

func (identity federationActivitySubjectIdentity) provider() providerplane.ItemIdentity {
	return providerplane.ItemIdentity{
		Repository: providerplane.RepositoryIdentity{
			Provider:       identity.Repository.Provider,
			PlatformHost:   identity.Repository.PlatformHost,
			PlatformRepoID: identity.Repository.PlatformRepoID,
		},
		ItemType: identity.ItemType, ItemNumber: identity.ItemNumber,
	}
}

type federationUnassignedActivitySubjectsOutput = httpapi.BodyOutput[federationUnassignedActivitySubjectsResponse]

func (s *Server) registerProviderActivitySubjectAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "federation-filter-unassigned-activity-subjects",
		Method:      http.MethodPost,
		Path:        "/federation/provider/activity/unassigned-subjects/query",
		Summary:     "Filter activity subjects by hub assignment state",
		Tags:        []string{"Fleet"},
	}, s.federationFilterUnassignedActivitySubjects)
}

func (s *Server) federationFilterUnassignedActivitySubjects(
	ctx context.Context,
	input *federationUnassignedActivitySubjectsInput,
) (*federationUnassignedActivitySubjectsOutput, error) {
	identities := make([]providerplane.ItemIdentity, 0, len(input.Body.Subjects))
	seen := make(map[providerplane.ItemIdentity]struct{}, len(input.Body.Subjects))
	for _, wireIdentity := range input.Body.Subjects {
		identity := wireIdentity.provider().Canonical()
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

	releaseReconciliation, err := s.db.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, httpapi.Internal("filter activity subjects failed")
	}
	defer releaseReconciliation()

	keys := make([]db.WorkspaceSubjectKey, 0, len(identities))
	identityByKey := make(map[db.WorkspaceSubjectKey]providerplane.ItemIdentity, len(identities))
	for _, identity := range identities {
		repository, lookupErr := s.db.GetRepositoryByProviderIDUnderRepositoryReconciliationRead(
			ctx,
			identity.Repository.Provider,
			identity.Repository.PlatformHost,
			identity.Repository.PlatformRepoID,
		)
		if lookupErr != nil {
			return nil, httpapi.Internal("filter activity subjects failed")
		}
		if repository == nil || repository.Lifecycle != db.RepositoryLifecycleActive {
			continue
		}
		itemType := db.WorkspaceItemTypePullRequest
		if identity.ItemType == "issue" {
			itemType = db.WorkspaceItemTypeIssue
		}
		key := db.WorkspaceSubjectKey{
			RepoID: repository.Repository.ID, ItemType: itemType, ItemNumber: identity.ItemNumber,
		}
		keys = append(keys, key)
		identityByKey[key] = identity
	}
	unassignedKeys, err := s.db.ListUnassignedWorkspaceSubjectKeys(ctx, keys)
	if err != nil {
		return nil, httpapi.Internal("filter activity subjects failed")
	}
	unassigned := make([]federationActivitySubjectIdentity, 0, len(unassignedKeys))
	for _, key := range keys {
		if _, ok := unassignedKeys[key]; ok {
			unassigned = append(
				unassigned,
				federationActivitySubjectIdentityFromProvider(identityByKey[key]),
			)
		}
	}
	return &federationUnassignedActivitySubjectsOutput{Body: federationUnassignedActivitySubjectsResponse{
		Subjects: unassigned,
	}}, nil
}

func workspaceActivitySubjectIdentities(
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
	repositories map[int64]providerplane.RepositoryIdentity,
) (map[db.WorkspaceSubjectKey]providerplane.ItemIdentity, []providerplane.ItemIdentity) {
	capacity := len(snapshot.Subjects) + len(snapshot.OwnReferences)
	byKey := make(map[db.WorkspaceSubjectKey]providerplane.ItemIdentity, capacity)
	identities := make([]providerplane.ItemIdentity, 0, capacity)
	seen := make(map[providerplane.ItemIdentity]struct{}, capacity)
	appendIdentity := func(key db.WorkspaceSubjectKey, repository providerplane.RepositoryIdentity) {
		itemType := "pr"
		if key.ItemType == db.WorkspaceItemTypeIssue {
			itemType = "issue"
		}
		identity := providerplane.ItemIdentity{
			Repository: repository,
			ItemType:   itemType,
			ItemNumber: key.ItemNumber,
		}.Canonical()
		if !identity.Valid() {
			return
		}
		byKey[key] = identity
		if _, ok := seen[identity]; ok {
			return
		}
		seen[identity] = struct{}{}
		identities = append(identities, identity)
	}
	for key := range snapshot.Subjects {
		appendIdentity(key, repositories[key.RepoID])
	}
	for key := range snapshot.OwnReferences {
		if _, ok := byKey[key]; ok {
			continue
		}
		appendIdentity(key, repositories[key.RepoID])
	}
	return byKey, identities
}

func (s *Server) workspaceActivityRepositoryIdentities(
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
		repository, err := s.db.GetRepoByID(ctx, key.RepoID)
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

func retainActivitySubjectsByIdentity(
	snapshot *workspaceapi.WorkspaceSubjectSnapshot,
	identitiesByKey map[db.WorkspaceSubjectKey]providerplane.ItemIdentity,
	unassigned []providerplane.ItemIdentity,
) {
	allowed := make(map[providerplane.ItemIdentity]struct{}, len(unassigned))
	for _, identity := range unassigned {
		allowed[identity.Canonical()] = struct{}{}
	}
	for key := range snapshot.Subjects {
		if _, ok := allowed[identitiesByKey[key]]; !ok {
			delete(snapshot.Subjects, key)
		}
	}
	for key := range snapshot.OwnReferences {
		if _, ok := allowed[identitiesByKey[key]]; !ok {
			delete(snapshot.OwnReferences, key)
		}
	}
}

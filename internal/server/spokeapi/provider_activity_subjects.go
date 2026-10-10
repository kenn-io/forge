package spokeapi

import (
	"context"
	"strconv"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/platform"
)

type FederationUnassignedActivitySubjectsResponse struct {
	Subjects []FederationActivitySubjectIdentity `json:"subjects" nullable:"false"`
}

type FederationActivityRepositoryIdentity struct {
	Provider     string                 `json:"provider"`
	PlatformHost string                 `json:"platform_host"`
	Key          platform.RepositoryKey `json:"-" repokey:"platform_repo_id,bitbucket_repository_uuid"`
}

func (r FederationActivityRepositoryIdentity) MarshalJSON() ([]byte, error) {
	type plain FederationActivityRepositoryIdentity
	return platform.MarshalKeyedJSON(plain(r))
}

func (r *FederationActivityRepositoryIdentity) UnmarshalJSON(data []byte) error {
	type plain FederationActivityRepositoryIdentity
	return platform.UnmarshalKeyedJSON(data, (*plain)(r))
}

type FederationActivitySubjectIdentity struct {
	Repository FederationActivityRepositoryIdentity `json:"repository"`
	ItemType   string                               `json:"item_type"`
	ItemNumber int                                  `json:"item_number"`
}

func (identity FederationActivitySubjectIdentity) Provider() providerplane.ItemIdentity {
	return providerplane.ItemIdentity{
		Repository: platform.RepositoryIdentity{
			Provider:     identity.Repository.Provider,
			PlatformHost: identity.Repository.PlatformHost,
			Key:          identity.Repository.Key,
		},
		ItemType: identity.ItemType, ItemNumber: identity.ItemNumber,
	}
}

func WorkspaceActivitySubjectIdentities(
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
	repositories map[int64]platform.RepositoryIdentity,
) (map[db.WorkspaceSubjectKey]providerplane.ItemIdentity, []providerplane.ItemIdentity) {
	capacity := len(snapshot.Subjects) + len(snapshot.OwnReferences)
	byKey := make(map[db.WorkspaceSubjectKey]providerplane.ItemIdentity, capacity)
	identities := make([]providerplane.ItemIdentity, 0, capacity)
	seen := make(map[providerplane.ItemIdentity]struct{}, capacity)
	appendIdentity := func(key db.WorkspaceSubjectKey, repository platform.RepositoryIdentity) {
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

func RetainActivitySubjectsByIdentity(
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

// MarkDraftActivitySubjects applies the hub's draft state to pull request
// subjects. A spoke can hold a workspace for a pull request it has no local
// row for, so only the hub knows whether that pull request is a draft.
func (s *HubProviderSource) MarkDraftActivitySubjects(
	ctx context.Context,
	snapshot *workspaceapi.WorkspaceSubjectSnapshot,
	identitiesByKey map[db.WorkspaceSubjectKey]providerplane.ItemIdentity,
) error {
	keys := make([]db.WorkspaceSubjectKey, 0, len(snapshot.Subjects))
	workspaces := make([]fleet.RawWorkspace, 0, len(snapshot.Subjects))
	for key := range snapshot.Subjects {
		identity, ok := identitiesByKey[key]
		if !ok || key.ItemType != db.WorkspaceItemTypePullRequest {
			continue
		}
		workspaces = append(workspaces, fleet.RawWorkspace{
			ID: strconv.Itoa(len(keys)),
			Repository: fleet.RepositoryIdentity{
				Provider:     identity.Repository.Provider,
				PlatformHost: identity.Repository.PlatformHost,
				Key:          identity.Repository.Key,
			},
			ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: key.ItemNumber,
		})
		keys = append(keys, key)
	}
	if len(workspaces) == 0 {
		return nil
	}
	enriched, err := s.WorkspaceProviderState(ctx, workspaces)
	if err != nil {
		return err
	}
	for index, workspace := range enriched {
		if workspace.MRIsDraft == nil {
			continue
		}
		activity := snapshot.Subjects[keys[index]]
		activity.Subject.IsDraft = *workspace.MRIsDraft
		snapshot.Subjects[keys[index]] = activity
	}
	return nil
}

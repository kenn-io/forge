package spokeapi

import (
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

type FederationUnassignedActivitySubjectsResponse struct {
	Subjects []FederationActivitySubjectIdentity `json:"subjects" nullable:"false"`
}

type FederationActivityRepositoryIdentity struct {
	Provider       string `json:"provider"`
	PlatformHost   string `json:"platform_host"`
	PlatformRepoID string `json:"platform_repo_id"`
}

type FederationActivitySubjectIdentity struct {
	Repository FederationActivityRepositoryIdentity `json:"repository"`
	ItemType   string                               `json:"item_type"`
	ItemNumber int                                  `json:"item_number"`
}

func (identity FederationActivitySubjectIdentity) Provider() providerplane.ItemIdentity {
	return providerplane.ItemIdentity{
		Repository: providerplane.RepositoryIdentity{
			Provider:       identity.Repository.Provider,
			PlatformHost:   identity.Repository.PlatformHost,
			PlatformRepoID: identity.Repository.PlatformRepoID,
		},
		ItemType: identity.ItemType, ItemNumber: identity.ItemNumber,
	}
}

func WorkspaceActivitySubjectIdentities(
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

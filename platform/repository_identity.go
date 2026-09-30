package platform

import (
	"strings"
	"uuid"
)

// RepositoryIdentity is the provider-verified key for a repository: provider
// kind, host, and exactly one stable provider key. GitHub, GitLab, Forgejo,
// Gitea, and Bitbucket Data Center assign a positive integer repository ID
// once per host and keep it across renames and transfers. Bitbucket Cloud has
// no integer repository ID; its key is the repository UUID and PlatformRepoID
// stays 0. Owner and name are mutable routes and are intentionally absent.
// Local database IDs are absent too, so the key is comparable across spokes.
//
// Compare identities with ==; values produced by Canonical, and by the
// Identity methods on stored and resolved repositories, are canonical.
type RepositoryIdentity struct {
	Provider                string    `json:"provider"`
	PlatformHost            string    `json:"platform_host"`
	PlatformRepoID          int64     `json:"platform_repo_id"`
	BitbucketRepositoryUUID uuid.UUID `json:"bitbucket_repository_uuid,omitzero"`
}

// Canonical returns the comparable form of a repository identity.
func (r RepositoryIdentity) Canonical() RepositoryIdentity {
	r.Provider = strings.ToLower(strings.TrimSpace(r.Provider))
	r.PlatformHost = strings.ToLower(strings.TrimSpace(r.PlatformHost))
	return r
}

// Valid reports whether the identity is complete. Bitbucket Cloud is complete
// only with a repository UUID and no integer ID. Every other provider is
// complete only with a positive integer ID and no UUID.
func (r RepositoryIdentity) Valid() bool {
	r = r.Canonical()
	if r.Provider == "" || r.PlatformHost == "" {
		return false
	}
	hasID := r.PlatformRepoID > 0
	hasUUID := r.BitbucketRepositoryUUID != uuid.Nil()
	if r.Provider == string(KindBitbucket) && r.PlatformHost == DefaultBitbucketHost {
		return hasUUID && !hasID
	}
	return hasID && !hasUUID
}

// Identity returns the reference's canonical identity. It is incomplete
// (Valid reports false) when the reference was never resolved against the
// provider, as for unverified configured repositories. Comparing it with a
// stored active repository's identity is safe either way: an incomplete
// identity never equals a complete one.
func (r RepoRef) Identity() RepositoryIdentity {
	return RepositoryIdentity{
		Provider: string(r.Platform), PlatformHost: r.Host,
		PlatformRepoID:          r.PlatformID,
		BitbucketRepositoryUUID: r.BitbucketRepositoryUUID,
	}.Canonical()
}

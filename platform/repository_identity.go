package platform

import "strings"

// RepositoryIdentity is the provider-verified key for a repository:
// provider kind, host, and the provider's stable repository ID. Owner and
// name are mutable routes and are intentionally absent. Local numeric
// database IDs are absent too, so the key is comparable across spokes.
//
// Compare identities with ==; values produced by Canonical, and by the
// Identity methods on stored and resolved repositories, are canonical.
type RepositoryIdentity struct {
	Provider       string `json:"provider"`
	PlatformHost   string `json:"platform_host"`
	PlatformRepoID string `json:"platform_repo_id"`
}

// Canonical returns the comparable form of a repository identity.
// Provider repository IDs remain case-sensitive.
func (r RepositoryIdentity) Canonical() RepositoryIdentity {
	r.Provider = strings.ToLower(strings.TrimSpace(r.Provider))
	r.PlatformHost = strings.ToLower(strings.TrimSpace(r.PlatformHost))
	r.PlatformRepoID = strings.TrimSpace(r.PlatformRepoID)
	return r
}

// Valid reports whether the identity is complete.
func (r RepositoryIdentity) Valid() bool {
	r = r.Canonical()
	return r.Provider != "" && r.PlatformHost != "" && r.PlatformRepoID != ""
}

// Identity returns the reference's canonical identity. It is incomplete
// (Valid reports false) when the reference was never resolved against the
// provider, as for unverified configured repositories. Comparing it with a
// stored active repository's identity is safe either way: an incomplete
// identity never equals a complete one.
func (r RepoRef) Identity() RepositoryIdentity {
	return RepositoryIdentity{
		Provider: string(r.Platform), PlatformHost: r.Host,
		PlatformRepoID: r.PlatformExternalID,
	}.Canonical()
}

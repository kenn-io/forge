package platform

import (
	"strings"
)

// RepositoryIdentity is the provider-verified key for a repository: provider
// kind, host, and the stable RepositoryKey. Owner and name are mutable routes
// and are intentionally absent. Local database IDs are absent too, so the
// identity is comparable across spokes.
//
// Compare identities with ==; values produced by Canonical, and by the
// Identity methods on stored and resolved repositories, are canonical.
type RepositoryIdentity struct {
	Provider     string        `json:"provider"`
	PlatformHost string        `json:"platform_host"`
	Key          RepositoryKey `json:"-" repokey:"platform_repo_id,bitbucket_repository_uuid"`
}

func (r RepositoryIdentity) MarshalJSON() ([]byte, error) {
	type plain RepositoryIdentity
	return MarshalKeyedJSON(plain(r))
}

func (r *RepositoryIdentity) UnmarshalJSON(data []byte) error {
	type plain RepositoryIdentity
	return UnmarshalKeyedJSON(data, (*plain)(r))
}

// Canonical returns the comparable form of a repository identity.
func (r RepositoryIdentity) Canonical() RepositoryIdentity {
	r.Provider = strings.ToLower(strings.TrimSpace(r.Provider))
	r.PlatformHost = strings.ToLower(strings.TrimSpace(r.PlatformHost))
	return r
}

// Valid reports whether the identity is complete: Bitbucket Cloud needs a
// UUID key, and every other provider an integer key.
func (r RepositoryIdentity) Valid() bool {
	r = r.Canonical()
	if r.Provider == "" || r.PlatformHost == "" || r.Key.IsZero() {
		return false
	}
	return r.Key.IsUUID() == IsBitbucketCloud(r.Provider, r.PlatformHost)
}

// IsBitbucketCloud reports whether provider and host name Bitbucket Cloud,
// the one provider whose repository key is a UUID.
func IsBitbucketCloud(provider, host string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), string(KindBitbucket)) &&
		strings.EqualFold(strings.TrimSpace(host), DefaultBitbucketHost)
}

// Identity returns the reference's canonical identity. It is incomplete
// (Valid reports false) when the reference was never resolved against the
// provider, as for unverified configured repositories. Comparing it with a
// stored active repository's identity is safe either way: an incomplete
// identity never equals a complete one.
func (r RepoRef) Identity() RepositoryIdentity {
	return RepositoryIdentity{
		Provider: string(r.Platform), PlatformHost: r.Host, Key: r.Key,
	}.Canonical()
}

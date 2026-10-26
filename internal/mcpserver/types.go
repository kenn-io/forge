package mcpserver

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"go.kenn.io/forge/platform"
)

type repoFilterInput struct {
	Provider                string `json:"provider,omitempty" jsonschema:"provider kind: github, gitlab, forgejo, gitea, or bitbucket"`
	PlatformHost            string `json:"platform_host,omitempty" jsonschema:"provider host from kenn_forge_list_repos, such as github.com or a self-hosted instance; defaults to the provider's public host (github.com, gitlab.com, codeberg.org, gitea.com, bitbucket.org); required for self-hosted instances and Bitbucket Data Center"`
	PlatformRepoID          int64  `json:"platform_repo_id,omitempty" jsonschema:"provider's integer repository ID from kenn_forge_list_repos or any Forge item; when omitted, Forge resolves it from provider, host, owner, and name"`
	BitbucketRepositoryUUID string `json:"bitbucket_repository_uuid,omitempty" jsonschema:"Bitbucket Cloud repository UUID from kenn_forge_list_repos; set instead of platform_repo_id for Bitbucket Cloud repositories"`
	RepoPath                string `json:"repo_path,omitempty" jsonschema:"full repository path from kenn_forge_list_repos; preferred for nested namespaces"`
	Owner                   string `json:"owner,omitempty" jsonschema:"repository owner or namespace"`
	Name                    string `json:"name,omitempty" jsonschema:"repository name"`
}

// FieldError reports invalid tool input and names the offending field so
// callers can correct it.
func FieldError(field, message string) *Error {
	return &Error{
		Kind: "invalid_request", Code: ErrorCodeValidation, Message: message,
		Details: map[string]any{"field": field},
	}
}

// repositoryKeyFromInput decodes the flat repository key an MCP caller
// passes: an integer platform_repo_id, or a Bitbucket Cloud UUID. The key is
// zero when the caller passed neither. field prefixes error messages, such
// as "repo." or "item.".
func repositoryKeyFromInput(field string, id int64, repositoryUUID string) (platform.RepositoryKey, error) {
	parsed, err := platform.ParseRepositoryUUID(repositoryUUID)
	if err != nil {
		return platform.RepositoryKey{}, FieldError(
			field+"bitbucket_repository_uuid", field+"bitbucket_repository_uuid must be a UUID",
		)
	}
	key, err := platform.RepositoryKeyFromWire(id, parsed)
	if err != nil {
		return platform.RepositoryKey{}, FieldError(field+"platform_repo_id", fmt.Sprintf(
			"%splatform_repo_id and %sbitbucket_repository_uuid: %v", field, field, err,
		))
	}
	return key, nil
}

// repositoryKeyFields encodes key as the flat MCP fields: the integer ID, or
// the canonical Bitbucket Cloud UUID text.
func repositoryKeyFields(key platform.RepositoryKey) (int64, string) {
	id, repositoryUUID := key.Wire()
	if repositoryUUID == uuid.Nil() {
		return id, ""
	}
	return id, repositoryUUID.String()
}

func (r repoFilterInput) isZero() bool {
	return strings.TrimSpace(r.Provider) == "" && strings.TrimSpace(r.PlatformHost) == "" &&
		r.PlatformRepoID == 0 && strings.TrimSpace(r.BitbucketRepositoryUUID) == "" &&
		strings.Trim(strings.TrimSpace(r.RepoPath), "/") == "" &&
		strings.Trim(strings.TrimSpace(r.Owner), "/") == "" && strings.Trim(strings.TrimSpace(r.Name), "/") == ""
}

// normalize validates a repository reference and fills what tool input may
// omit: the canonical provider kind, the provider's public host, and
// owner/name from repo_path. The key stays zero when the caller omitted it;
// Server.resolveRepositoryKey fills it from the route. field prefixes error
// messages and details, such as "repo." or "item.".
func (r repoFilterInput) normalize(field string) (RepositoryIdentity, error) {
	provider := strings.TrimSpace(r.Provider)
	if provider == "" {
		return RepositoryIdentity{}, FieldError(field+"provider", field+"provider is required")
	}
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return RepositoryIdentity{}, FieldError(field+"provider", err.Error())
	}
	meta, ok := platform.MetadataFor(kind)
	if !ok {
		return RepositoryIdentity{}, FieldError(field+"provider", fmt.Sprintf("unsupported provider %q", provider))
	}
	key, err := repositoryKeyFromInput(field, r.PlatformRepoID, r.BitbucketRepositoryUUID)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	host := strings.ToLower(strings.TrimSpace(r.PlatformHost))
	if host == "" {
		// Bitbucket's public host is Cloud, whose keys are UUIDs; an integer
		// key names a Data Center repository, whose host only the caller knows.
		if kind == platform.KindBitbucket && !key.IsZero() && !key.IsUUID() {
			return RepositoryIdentity{}, FieldError(field+"platform_host",
				field+"platform_host is required for Bitbucket Data Center repositories; copy it from kenn_forge_list_repos")
		}
		host = meta.DefaultHost
	}
	if !key.IsZero() && key.IsUUID() != platform.IsBitbucketCloud(string(kind), host) {
		if key.IsUUID() {
			return RepositoryIdentity{}, FieldError(field+"bitbucket_repository_uuid",
				field+"bitbucket_repository_uuid applies only to Bitbucket Cloud (bitbucket.org); use "+field+"platform_repo_id")
		}
		return RepositoryIdentity{}, FieldError(field+"bitbucket_repository_uuid",
			field+"platform_repo_id is not valid for Bitbucket Cloud (bitbucket.org); use "+field+"bitbucket_repository_uuid")
	}
	repoPath := strings.Trim(strings.TrimSpace(r.RepoPath), "/")
	owner := strings.Trim(strings.TrimSpace(r.Owner), "/")
	name := strings.Trim(strings.TrimSpace(r.Name), "/")
	if repoPath != "" {
		parts := strings.Split(repoPath, "/")
		if len(parts) < 2 || slices.Contains(parts, "") {
			return RepositoryIdentity{}, FieldError(field+"repo_path", field+"repo_path must contain an owner and repository name")
		}
		pathOwner := strings.Join(parts[:len(parts)-1], "/")
		pathName := parts[len(parts)-1]
		if (owner != "" && owner != pathOwner) || (name != "" && name != pathName) {
			return RepositoryIdentity{}, FieldError(field+"repo_path", field+"repo_path conflicts with "+field+"owner or "+field+"name")
		}
		owner, name = pathOwner, pathName
	}
	if owner == "" {
		return RepositoryIdentity{}, FieldError(field+"owner", field+"owner is required")
	}
	if name == "" {
		return RepositoryIdentity{}, FieldError(field+"name", field+"name is required")
	}
	return RepositoryIdentity{
		Provider: string(kind), PlatformHost: host, Key: key,
		RepoPath: owner + "/" + name, Owner: owner, Name: name,
	}, nil
}

type itemRef struct {
	Type                    string `json:"type"`
	Provider                string `json:"provider"`
	PlatformHost            string `json:"platform_host"`
	PlatformRepoID          int64  `json:"platform_repo_id"`
	BitbucketRepositoryUUID string `json:"bitbucket_repository_uuid,omitempty" jsonschema:"Bitbucket Cloud repository UUID; set instead of a nonzero platform_repo_id for Bitbucket Cloud repositories"`
	Owner                   string `json:"owner"`
	Name                    string `json:"name"`
	RepoPath                string `json:"repo_path"`
	Number                  int    `json:"number"`
	Title                   string `json:"title"`
	URL                     string `json:"url"`
	State                   string `json:"state"`
	Author                  string `json:"author"`
	IsDraft                 bool   `json:"is_draft"`
}

func repositoryPath(repo RepositoryIdentity) string {
	if repo.RepoPath != "" {
		return repo.RepoPath
	}
	if repo.Owner == "" {
		return repo.Name
	}
	if repo.Name == "" {
		return repo.Owner
	}
	return repo.Owner + "/" + repo.Name
}

func workflowStatusOrNew(status string) string {
	if status == "" {
		return "new"
	}
	return status
}

func formatMCPTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (p Pull) itemKey() candidateKey {
	return itemKeyFor("pr", p.Number, p.Repository)
}

func (i Issue) itemKey() candidateKey {
	return itemKeyFor("issue", i.Number, i.Repository)
}

func (a ActivityItem) itemKey() candidateKey {
	return itemKeyFor(a.ItemType, a.ItemNumber, a.Repository)
}

func itemKeyFor(itemType string, number int, repo RepositoryIdentity) candidateKey {
	return candidateKey{
		provider: repo.Provider, platformHost: repo.PlatformHost, repoKey: repo.Key,
		repoPath: repositoryPath(repo), owner: repo.Owner, name: repo.Name,
		itemType: itemType, number: number,
	}
}

func (k candidateKey) itemIdentity() ItemIdentity {
	return ItemIdentity{
		Type: k.itemType, Provider: k.provider, PlatformHost: k.platformHost,
		RepoKey: k.repoKey, Owner: k.owner, Name: k.name, Number: k.number,
	}
}

// itemRef encodes the key as the identity fields of an MCP item reference.
func (k candidateKey) itemRef() itemRef {
	platformRepoID, repositoryUUID := repositoryKeyFields(k.repoKey)
	return itemRef{
		Type: k.itemType, Provider: k.provider, PlatformHost: k.platformHost,
		PlatformRepoID: platformRepoID, BitbucketRepositoryUUID: repositoryUUID,
		Owner: k.owner, Name: k.name, RepoPath: k.repoPath, Number: k.number,
	}
}

func (k candidateKey) sortKey() string {
	return strings.Join([]string{
		k.provider,
		k.platformHost,
		k.repoKey.String(),
		k.repoPath,
		k.itemType,
		fmt.Sprintf("%08d", k.number),
	}, "\x1f")
}

func (p Pull) itemRef() itemRef {
	ref := p.itemKey().itemRef()
	ref.Title, ref.URL, ref.State, ref.Author, ref.IsDraft = p.Title, p.URL, p.State, p.Author, p.IsDraft
	return ref
}

func (i Issue) itemRef() itemRef {
	ref := i.itemKey().itemRef()
	ref.Title, ref.URL, ref.State, ref.Author = i.Title, i.URL, i.State, i.Author
	return ref
}

func (a ActivityItem) itemRef() itemRef {
	ref := a.itemKey().itemRef()
	ref.Title, ref.URL, ref.State, ref.Author = a.ItemTitle, a.ItemURL, a.ItemState, a.ItemAuthor
	return ref
}

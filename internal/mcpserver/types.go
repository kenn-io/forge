package mcpserver

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"go.kenn.io/forge/platform"
)

type repoFilterInput struct {
	Provider                string `json:"provider,omitempty" jsonschema:"provider kind, such as github or gitlab"`
	PlatformHost            string `json:"platform_host,omitempty" jsonschema:"provider host; defaults to the provider public host"`
	PlatformRepoID          int64  `json:"platform_repo_id,omitempty" jsonschema:"provider's integer repository ID from kenn_forge_list_repos"`
	BitbucketRepositoryUUID string `json:"bitbucket_repository_uuid,omitempty" jsonschema:"Bitbucket Cloud repository UUID from kenn_forge_list_repos; set instead of platform_repo_id for Bitbucket Cloud repositories"`
	RepoPath                string `json:"repo_path,omitempty" jsonschema:"full repository path from kenn_forge_list_repos; preferred for nested namespaces"`
	Owner                   string `json:"owner,omitempty" jsonschema:"repository owner or namespace"`
	Name                    string `json:"name,omitempty" jsonschema:"repository name"`
}

// repositoryKeyFromInput decodes the flat repository key an MCP caller
// passes: an integer platform_repo_id, or a Bitbucket Cloud UUID. Tool
// handlers call it once where the input enters. field prefixes error
// messages, such as "repo " or "item.".
func repositoryKeyFromInput(field string, id int64, repositoryUUID string) (platform.RepositoryKey, error) {
	parsed, err := platform.ParseRepositoryUUID(repositoryUUID)
	if err != nil {
		return platform.RepositoryKey{}, fmt.Errorf("%sbitbucket_repository_uuid must be a UUID", field)
	}
	key, err := platform.RepositoryKeyFromWire(id, parsed)
	if err != nil {
		return platform.RepositoryKey{}, fmt.Errorf(
			"%splatform_repo_id and %sbitbucket_repository_uuid: %w", field, field, err,
		)
	}
	if key.IsZero() {
		return platform.RepositoryKey{}, fmt.Errorf(
			"%splatform_repo_id or %sbitbucket_repository_uuid is required", field, field,
		)
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

func (r repoFilterInput) repositoryIdentity() (RepositoryIdentity, error) {
	provider := strings.TrimSpace(r.Provider)
	host := strings.TrimSpace(r.PlatformHost)
	repoPath := strings.Trim(strings.TrimSpace(r.RepoPath), "/")
	owner := strings.Trim(strings.TrimSpace(r.Owner), "/")
	name := strings.Trim(strings.TrimSpace(r.Name), "/")
	if provider == "" && host == "" && r.PlatformRepoID == 0 && strings.TrimSpace(r.BitbucketRepositoryUUID) == "" &&
		repoPath == "" && owner == "" && name == "" {
		return RepositoryIdentity{}, nil
	}
	if provider == "" {
		return RepositoryIdentity{}, errors.New("repo provider is required")
	}
	key, err := repositoryKeyFromInput("repo ", r.PlatformRepoID, r.BitbucketRepositoryUUID)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	return keyedRepositoryIdentity(provider, host, key, repoPath, owner, name)
}

// keyedRepositoryIdentity normalizes a repository filter whose key is
// already decoded: canonical provider kind, default host, and owner/name
// derived from repo_path when it is set.
func keyedRepositoryIdentity(
	provider, host string, key platform.RepositoryKey, repoPath, owner, name string,
) (RepositoryIdentity, error) {
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	meta, ok := platform.MetadataFor(kind)
	if !ok {
		return RepositoryIdentity{}, fmt.Errorf("unsupported provider %q", provider)
	}
	if host == "" {
		host = meta.DefaultHost
	}
	if repoPath != "" {
		parts := strings.Split(repoPath, "/")
		if len(parts) < 2 {
			return RepositoryIdentity{}, errors.New("repo_path must contain an owner and repository name")
		}
		owner = strings.Join(parts[:len(parts)-1], "/")
		name = parts[len(parts)-1]
	} else {
		if owner == "" {
			return RepositoryIdentity{}, errors.New("repo owner is required")
		}
		if name == "" {
			return RepositoryIdentity{}, errors.New("repo name is required")
		}
		repoPath = owner + "/" + name
	}
	return RepositoryIdentity{
		Provider: string(kind), PlatformHost: host, Key: key,
		RepoPath: repoPath, Owner: owner, Name: name,
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

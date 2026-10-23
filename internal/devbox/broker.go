// Package devbox implements the account and connection contracts for execution workers.
package devbox

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/githubapp"
)

type Account struct {
	UID          uint32 `toml:"uid" json:"uid"`
	GitHubUserID int64  `toml:"github_user_id" json:"github_user_id"`
	Login        string `toml:"login" json:"login"`
	CommitName   string `toml:"commit_name" json:"commit_name"`
	CommitEmail  string `toml:"commit_email" json:"commit_email"`
	// Service marks a non-human account that acts as the App's bot identity.
	// Its GitHubUserID is the bot user ID, and it skips the human membership
	// and repository permission checks.
	Service bool `toml:"service" json:"service"`
	// WriteRepositoryIDs and ReadRepositoryIDs are a service account's only
	// repositories, by GitHub repository ID. Write admits every profile; read
	// admits only fetches. Developers' GitHub permissions decide their access.
	WriteRepositoryIDs []int64 `toml:"write_repository_ids" json:"write_repository_ids"`
	ReadRepositoryIDs  []int64 `toml:"read_repository_ids" json:"read_repository_ids"`
}

type BrokerConfig struct {
	Socket         string    `toml:"socket"`
	AppID          int64     `toml:"app_id"`
	InstallationID int64     `toml:"installation_id"`
	Organization   string    `toml:"organization"`
	OrganizationID int64     `toml:"organization_id"`
	PrivateKeyFile string    `toml:"private_key_file"`
	Accounts       []Account `toml:"accounts"`
}

type CredentialRequest struct {
	Repository string `json:"repository" minLength:"3" doc:"GitHub owner/name"`
	Profile    string `json:"profile" enum:"git,push,pr"`
}

type Credential struct {
	Token         string    `json:"token"`
	ExpiresAt     time.Time `json:"expires_at"`
	Writable      bool      `json:"writable"`
	GitHubUserID  int64     `json:"github_user_id"`
	RepositoryID  int64     `json:"repository_id"`
	DefaultBranch string    `json:"default_branch"`
}

type brokerCacheKey struct {
	repository string
	profile    string
}

// Broker holds installation tokens only in memory. Account admission is checked
// again before a cached token is returned to a caller.
type Broker struct {
	config BrokerConfig
	key    *rsa.PrivateKey
	apps   *githubapp.Client
	client *http.Client
	base   *url.URL
	mu     sync.Mutex
	cache  map[brokerCacheKey]*githubapp.InstallationToken
}

func NewBroker(cfg BrokerConfig) (*Broker, error) {
	if cfg.AppID <= 0 || cfg.InstallationID <= 0 || cfg.OrganizationID <= 0 ||
		!validRouteSegment(cfg.Organization) || cfg.Socket == "" {
		return nil, errors.New("broker requires a socket, App, installation, and organization identity")
	}
	uids := make(map[uint32]bool)
	users := make(map[int64]bool)
	for _, account := range cfg.Accounts {
		if account.UID == 0 || account.GitHubUserID <= 0 || uids[account.UID] || users[account.GitHubUserID] {
			return nil, errors.New("broker accounts require unique non-root UIDs and GitHub user IDs")
		}
		uids[account.UID], users[account.GitHubUserID] = true, true
		if err := validateRepositoryGrants(account); err != nil {
			return nil, err
		}
	}
	pem, err := os.ReadFile(cfg.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read App private key: %w", err)
	}
	key, err := githubapp.ParsePrivateKey(pem)
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse("https://api.github.com/")
	return &Broker{
		config: cfg, key: key, apps: githubapp.NewClient("github.com"), base: base,
		client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: refuseRedirect},
		cache:  make(map[brokerCacheKey]*githubapp.InstallationToken),
	}, nil
}

// validateRepositoryGrants requires each service account to name at least one
// writable repository, so a missing list cannot mean the whole installation.
func validateRepositoryGrants(account Account) error {
	if !account.Service {
		if len(account.WriteRepositoryIDs) != 0 || len(account.ReadRepositoryIDs) != 0 {
			return errors.New("broker repository lists apply only to service accounts")
		}
		return nil
	}
	if len(account.WriteRepositoryIDs) == 0 {
		return errors.New("broker service accounts require write_repository_ids")
	}
	seen := make(map[int64]bool)
	for _, id := range slices.Concat(account.WriteRepositoryIDs, account.ReadRepositoryIDs) {
		if id <= 0 || seen[id] {
			return errors.New("broker service account repository IDs must be positive and listed once")
		}
		seen[id] = true
	}
	return nil
}

func refuseRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func validRouteSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func ParseRepository(value string) (string, string, error) {
	owner, name, ok := strings.Cut(strings.TrimSuffix(value, ".git"), "/")
	if !ok || !validRouteSegment(owner) || !validRouteSegment(name) {
		return "", "", errors.New("repository must be GitHub owner/name")
	}
	return owner, name, nil
}

func (b *Broker) api(token string) (*gh.Client, error) {
	return gh.NewClient(gh.WithHTTPClient(b.client), gh.WithAuthToken(token), gh.WithURLs(new(b.base.String()), nil))
}

func (b *Broker) Credential(ctx context.Context, uid uint32, request CredentialRequest) (*Credential, error) {
	return b.credential(ctx, uid, request, true)
}

func (b *Broker) credential(ctx context.Context, uid uint32, request CredentialRequest, allowRefresh bool) (*Credential, error) {
	index := slices.IndexFunc(b.config.Accounts, func(account Account) bool { return account.UID == uid })
	if index < 0 {
		return nil, errors.New("account is not enrolled for devbox GitHub access")
	}
	account := b.config.Accounts[index]
	owner, name, err := ParseRepository(request.Repository)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(owner, b.config.Organization) {
		return nil, errors.New("repository is not admitted for devbox GitHub access")
	}
	if request.Profile != "git" && request.Profile != "push" && request.Profile != "pr" {
		return nil, errors.New("unsupported GitHub credential profile")
	}
	appJWT, err := githubapp.SignAppJWT(b.config.AppID, b.key, time.Now())
	if err != nil {
		return nil, err
	}
	appAPI, err := b.api(appJWT)
	if err != nil {
		return nil, err
	}
	installation, _, err := appAPI.Apps.GetInstallation(ctx, b.config.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("GitHub installation unavailable: %w", err)
	}
	if installation.GetAppID() != b.config.AppID || installation.GetAccount().GetID() != b.config.OrganizationID ||
		!strings.EqualFold(installation.GetAccount().GetLogin(), owner) || installation.SuspendedAt != nil {
		return nil, errors.New("GitHub installation does not match the admitted organization and App")
	}
	// This read-only token stays inside the broker, including Members read.
	auth, err := b.token(ctx, appJWT, name, "admission", map[string]string{"metadata": "read", "members": "read"})
	if err != nil {
		return nil, err
	}
	repoID := auth.Repositories[0].ID
	api, err := b.api(auth.Token)
	if err != nil {
		return nil, err
	}
	// A reused name can leave every cached profile scoped to the previous repository.
	refresh := func(failure error) (*Credential, error) {
		if err := b.Erase(uid, request.Repository); err != nil {
			return nil, err
		}
		if !allowRefresh {
			return nil, failure
		}
		return b.credential(ctx, uid, request, false)
	}
	repo, response, err := api.Repositories.Get(ctx, owner, name)
	if err != nil {
		failure := fmt.Errorf("GitHub repository unavailable: %w", err)
		if response != nil && response.StatusCode == http.StatusNotFound {
			return refresh(failure)
		}
		return nil, failure
	}
	if repo.GetOwner().GetID() != b.config.OrganizationID {
		return nil, errors.New("GitHub repository identity differs from the installation token")
	}
	if repo.GetID() != repoID {
		return refresh(errors.New("GitHub repository identity differs from the installation token"))
	}
	var writable bool
	if account.Service {
		writable = slices.Contains(account.WriteRepositoryIDs, repoID)
		if !writable && !slices.Contains(account.ReadRepositoryIDs, repoID) {
			return nil, errors.New("repository is not admitted for this service account")
		}
		if request.Profile != "git" && !writable {
			return nil, errors.New("service account has read-only access to this GitHub repository")
		}
	} else {
		writable, err = b.humanAccess(ctx, api, owner, name, account)
		if err != nil {
			return nil, err
		}
		if request.Profile != "git" && !writable {
			return nil, errors.New("developer does not have write permission for this GitHub repository")
		}
	}
	permissions := map[string]string{"metadata": "read", "contents": "read"}
	profile := "read"
	if writable {
		permissions["contents"], profile = "write", "write"
	}
	if request.Profile == "pr" {
		permissions["pull_requests"] = "write"
		permissions["checks"], permissions["statuses"] = "read", "read"
		profile = "pr"
	}
	token, err := b.token(ctx, appJWT, name, profile, permissions)
	if err != nil {
		return nil, err
	}
	if token.Repositories[0].ID != repoID {
		return refresh(errors.New("GitHub credential repository differs from the authorized repository"))
	}
	return &Credential{
		Token: token.Token, ExpiresAt: token.ExpiresAt, Writable: writable,
		GitHubUserID: account.GitHubUserID, RepositoryID: repoID, DefaultBranch: repo.GetDefaultBranch(),
	}, nil
}

// humanAccess verifies a human account's GitHub identity, organization
// membership and repository permission, and reports whether it may write.
func (b *Broker) humanAccess(
	ctx context.Context, api *gh.Client, owner, name string, account Account,
) (bool, error) {
	user, _, err := api.Users.GetByID(ctx, account.GitHubUserID)
	if err != nil {
		return false, fmt.Errorf("resolve enrolled GitHub identity: %w", err)
	}
	if user.GetID() != account.GitHubUserID || user.GetLogin() == "" {
		return false, errors.New("GitHub identity differs from the enrolled user")
	}
	member, _, err := api.Organizations.IsMember(ctx, owner, user.GetLogin())
	if err != nil {
		return false, fmt.Errorf("check GitHub organization membership: %w", err)
	}
	if !member {
		return false, errors.New("developer is not a member of the admitted GitHub organization")
	}
	permission, _, err := api.Repositories.GetPermissionLevel(ctx, owner, name, user.GetLogin())
	if err != nil {
		return false, fmt.Errorf("check developer GitHub repository permission: %w", err)
	}
	level := permission.GetPermission()
	writable := level == "admin" || level == "maintain" || level == "write"
	if !writable && level != "read" && level != "triage" {
		return false, errors.New("developer has no access to this GitHub repository")
	}
	return writable, nil
}

func (b *Broker) token(ctx context.Context, jwt string, repository string, profile string, permissions map[string]string) (*githubapp.InstallationToken, error) {
	key := brokerCacheKey{repository: strings.ToLower(repository), profile: profile}
	b.mu.Lock()
	cached := b.cache[key]
	b.mu.Unlock()
	if cached != nil && time.Until(cached.ExpiresAt) > 2*time.Minute {
		return cached, nil
	}
	token, err := b.apps.CreateInstallationToken(ctx, jwt, b.config.InstallationID, &githubapp.InstallationTokenRequest{
		Repositories: []string{repository}, Permissions: permissions,
	})
	if err != nil {
		return nil, fmt.Errorf("mint repository GitHub credential: %w", err)
	}
	if token.Token == "" || time.Until(token.ExpiresAt) <= 2*time.Minute ||
		len(token.Repositories) != 1 || token.Repositories[0].ID <= 0 ||
		!strings.EqualFold(token.Repositories[0].Name, repository) || !maps.Equal(token.Permissions, permissions) {
		return nil, errors.New("GitHub returned a token with unexpected repository scope, permissions, or expiry")
	}
	b.mu.Lock()
	b.cache[key] = token
	b.mu.Unlock()
	return token, nil
}

func (b *Broker) Erase(uid uint32, repository string) error {
	if !slices.ContainsFunc(b.config.Accounts, func(account Account) bool { return account.UID == uid }) {
		return errors.New("account is not enrolled for devbox GitHub access")
	}
	owner, name, err := ParseRepository(repository)
	if err != nil {
		return err
	}
	if !strings.EqualFold(owner, b.config.Organization) {
		return errors.New("repository is not admitted for devbox GitHub access")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for key := range b.cache {
		if strings.EqualFold(key.repository, name) {
			delete(b.cache, key)
		}
	}
	return nil
}

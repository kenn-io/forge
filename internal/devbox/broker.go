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

	gh "github.com/google/go-github/v91/github"
	"go.kenn.io/forge/githubapp"
)

type Account struct {
	UID          uint32 `toml:"uid" json:"uid"`
	GitHubUserID int64  `toml:"github_user_id" json:"github_user_id"`
	Login        string `toml:"login" json:"login"`
	CommitName   string `toml:"commit_name" json:"commit_name"`
	CommitEmail  string `toml:"commit_email" json:"commit_email"`
}

type Repository struct {
	ID   int64  `toml:"id" json:"id"`
	Name string `toml:"name" json:"name"`
}

type BrokerConfig struct {
	Socket         string       `toml:"socket"`
	AppID          int64        `toml:"app_id"`
	InstallationID int64        `toml:"installation_id"`
	Organization   string       `toml:"organization"`
	OrganizationID int64        `toml:"organization_id"`
	PrivateKeyFile string       `toml:"private_key_file"`
	Accounts       []Account    `toml:"accounts"`
	Repositories   []Repository `toml:"repositories"`
}

type CredentialRequest struct {
	Repository string `json:"repository" minLength:"3" doc:"GitHub owner/name"`
	Profile    string `json:"profile" enum:"git,push,pr"`
}

type Credential struct {
	Token            string    `json:"token"`
	ExpiresAt        time.Time `json:"expires_at"`
	Writable         bool      `json:"writable"`
	GitHubUserID     int64     `json:"github_user_id"`
	RepositoryID     int64     `json:"repository_id"`
	RepositoryNodeID string    `json:"repository_node_id"`
	DefaultBranch    string    `json:"default_branch"`
}

type brokerCacheKey struct {
	repository int64
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
	}
	repos := make(map[int64]bool)
	names := make(map[string]bool)
	for _, repo := range cfg.Repositories {
		name := strings.ToLower(repo.Name)
		if repo.ID <= 0 || !validRouteSegment(repo.Name) || repos[repo.ID] || names[name] {
			return nil, errors.New("broker repositories require unique positive IDs and names")
		}
		repos[repo.ID], names[name] = true, true
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
	index := slices.IndexFunc(b.config.Accounts, func(account Account) bool { return account.UID == uid })
	if index < 0 {
		return nil, errors.New("account is not enrolled for devbox GitHub access")
	}
	account := b.config.Accounts[index]
	owner, name, err := ParseRepository(request.Repository)
	if err != nil {
		return nil, err
	}
	repoIndex := slices.IndexFunc(b.config.Repositories, func(repo Repository) bool {
		return strings.EqualFold(owner, b.config.Organization) && strings.EqualFold(name, repo.Name)
	})
	if repoIndex < 0 {
		return nil, errors.New("repository is not admitted for devbox GitHub access")
	}
	if request.Profile != "git" && request.Profile != "push" && request.Profile != "pr" {
		return nil, errors.New("unsupported GitHub credential profile")
	}
	repoID := b.config.Repositories[repoIndex].ID
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
	auth, err := b.token(ctx, appJWT, repoID, "admission", map[string]string{"metadata": "read", "members": "read"})
	if err != nil {
		return nil, err
	}
	api, err := b.api(auth.Token)
	if err != nil {
		return nil, err
	}
	user, _, err := api.Users.GetByID(ctx, account.GitHubUserID)
	if err != nil {
		return nil, fmt.Errorf("resolve enrolled GitHub identity: %w", err)
	}
	if user.GetID() != account.GitHubUserID || user.GetLogin() == "" {
		return nil, errors.New("GitHub identity differs from the enrolled user")
	}
	member, _, err := api.Organizations.IsMember(ctx, owner, user.GetLogin())
	if err != nil {
		return nil, fmt.Errorf("check GitHub organization membership: %w", err)
	}
	if !member {
		return nil, errors.New("developer is not a member of the admitted GitHub organization")
	}
	repo, _, err := api.Repositories.Get(ctx, owner, name)
	if err != nil {
		return nil, fmt.Errorf("GitHub repository unavailable: %w", err)
	}
	if repo.GetID() != repoID || repo.GetOwner().GetID() != b.config.OrganizationID || repo.GetNodeID() == "" {
		return nil, errors.New("GitHub repository identity differs from the admitted repository")
	}
	permission, _, err := api.Repositories.GetPermissionLevel(ctx, owner, name, user.GetLogin())
	if err != nil {
		return nil, fmt.Errorf("check developer GitHub repository permission: %w", err)
	}
	level := permission.GetPermission()
	writable := level == "admin" || level == "maintain" || level == "write"
	if !writable && level != "read" && level != "triage" {
		return nil, errors.New("developer has no access to this GitHub repository")
	}
	if request.Profile != "git" && !writable {
		return nil, errors.New("developer does not have write permission for this GitHub repository")
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
	token, err := b.token(ctx, appJWT, repoID, profile, permissions)
	if err != nil {
		return nil, err
	}
	return &Credential{
		Token: token.Token, ExpiresAt: token.ExpiresAt, Writable: writable,
		GitHubUserID: account.GitHubUserID, RepositoryID: repoID, DefaultBranch: repo.GetDefaultBranch(),
		RepositoryNodeID: repo.GetNodeID(),
	}, nil
}

func (b *Broker) token(ctx context.Context, jwt string, repoID int64, profile string, permissions map[string]string) (*githubapp.InstallationToken, error) {
	key := brokerCacheKey{repository: repoID, profile: profile}
	b.mu.Lock()
	cached := b.cache[key]
	b.mu.Unlock()
	if cached != nil && time.Until(cached.ExpiresAt) > 2*time.Minute {
		return cached, nil
	}
	token, err := b.apps.CreateInstallationToken(ctx, jwt, b.config.InstallationID, &githubapp.InstallationTokenRequest{
		RepositoryIDs: []int64{repoID}, Permissions: permissions,
	})
	if err != nil {
		return nil, fmt.Errorf("mint repository GitHub credential: %w", err)
	}
	if token.Token == "" || time.Until(token.ExpiresAt) <= 2*time.Minute ||
		len(token.Repositories) != 1 || token.Repositories[0].ID != repoID || !maps.Equal(token.Permissions, permissions) {
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
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, repo := range b.config.Repositories {
		if strings.EqualFold(owner, b.config.Organization) && strings.EqualFold(name, repo.Name) {
			for key := range b.cache {
				if key.repository == repo.ID {
					delete(b.cache, key)
				}
			}
			return nil
		}
	}
	return errors.New("repository is not admitted for devbox GitHub access")
}

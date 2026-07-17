package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"go.kenn.io/middleman/internal/tokenauth"
)

// IdentityKey identifies the GitHub principal whose rate limit and sync budget
// a request consumes on one host.
type IdentityKey struct {
	Host      string
	Principal string
}

func (k IdentityKey) String() string {
	return strings.ToLower(strings.TrimSpace(k.Host)) + "\x00" +
		strings.TrimSpace(k.Principal)
}

// GitHubIdentity is the stable principal plus safe display metadata resolved
// for one credential route.
type GitHubIdentity struct {
	Key   IdentityKey
	Login string
}

func (i GitHubIdentity) Label() string {
	switch {
	case strings.HasPrefix(i.Key.Principal, "user:") && i.Login != "":
		return "GitHub user " + i.Login
	case strings.HasPrefix(i.Key.Principal, "installation:"):
		return "GitHub App installation " + strings.TrimPrefix(
			i.Key.Principal, "installation:",
		)
	default:
		return i.Key.Principal
	}
}

// IdentityResolver resolves a user credential to GitHub's immutable numeric
// account identity.
type IdentityResolver interface {
	ResolvePAT(context.Context, string, tokenauth.Source) (GitHubIdentity, error)
}

var ErrIdentityChanged = errors.New("GitHub credential identity changed; restart required")

type identityBoundSource struct {
	source   tokenauth.Source
	host     string
	expected IdentityKey
	resolver IdentityResolver

	mu            sync.Mutex
	acceptedToken string
}

// BindSourceIdentity prevents a lazily reloaded PAT from moving a live route to
// a different GitHub user while its trackers and budget remain bound to the
// startup identity. App reads pass through; mutation/user token values are
// re-resolved only when they change.
func BindSourceIdentity(
	source tokenauth.Source,
	host string,
	expected IdentityKey,
	resolver IdentityResolver,
) tokenauth.Source {
	if source == nil || expected.Principal == "" || resolver == nil {
		return source
	}
	return &identityBoundSource{
		source: source, host: host, expected: expected, resolver: resolver,
	}
}

func (s *identityBoundSource) Token(ctx context.Context) (string, error) {
	token, err := s.source.Token(ctx)
	if err != nil {
		return token, err
	}
	if !tokenauth.IsMutationAuth(ctx) && s.source.Descriptor().HasActiveGitHubApp() {
		return token, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == s.acceptedToken {
		return token, nil
	}
	identity, err := s.resolver.ResolvePAT(
		ctx, s.host, staticTokenSource{token: token, desc: s.source.Descriptor()},
	)
	if err != nil {
		return "", err
	}
	if identity.Key != s.expected {
		return "", ErrIdentityChanged
	}
	s.acceptedToken = token
	return token, nil
}

func (s *identityBoundSource) Invalidate() {
	s.source.Invalidate()
	s.mu.Lock()
	s.acceptedToken = ""
	s.mu.Unlock()
}

func (s *identityBoundSource) Descriptor() tokenauth.Descriptor {
	return s.source.Descriptor()
}

type staticTokenSource struct {
	token string
	desc  tokenauth.Descriptor
}

func (s staticTokenSource) Token(context.Context) (string, error) { return s.token, nil }
func (s staticTokenSource) Invalidate()                           {}
func (s staticTokenSource) Descriptor() tokenauth.Descriptor      { return s.desc }

// HTTPIdentityResolver resolves PAT identity through GitHub's authenticated
// user endpoint. Endpoint and NewHTTPClient are injectable for tests.
type HTTPIdentityResolver struct {
	Endpoint      func(host string) string
	NewHTTPClient func(host string, source tokenauth.Source) *http.Client
}

func (r HTTPIdentityResolver) ResolvePAT(
	ctx context.Context, host string, source tokenauth.Source,
) (GitHubIdentity, error) {
	if source == nil {
		return GitHubIdentity{}, fmt.Errorf("resolve GitHub identity for %s: nil token source", host)
	}
	endpoint := authenticatedUserEndpoint(host)
	if r.Endpoint != nil {
		endpoint = r.Endpoint(host)
	}
	client := identityHTTPClientForHost(host, source)
	if r.NewHTTPClient != nil {
		client = r.NewHTTPClient(host, source)
	}
	req, err := http.NewRequestWithContext(
		tokenauth.WithMutationAuth(ctx), http.MethodGet, endpoint, nil,
	)
	if err != nil {
		return GitHubIdentity{}, fmt.Errorf("create GitHub identity request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return GitHubIdentity{}, fmt.Errorf(
			"resolve GitHub identity for %s via %s: %w",
			host, source.Descriptor().SafeString(), err,
		)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, resp.Body)
		return GitHubIdentity{}, fmt.Errorf(
			"resolve GitHub identity for %s via %s: status %d",
			host, source.Descriptor().SafeString(), resp.StatusCode,
		)
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return GitHubIdentity{}, fmt.Errorf(
			"decode GitHub identity for %s via %s: %w",
			host, source.Descriptor().SafeString(), err,
		)
	}
	if user.ID <= 0 {
		return GitHubIdentity{}, fmt.Errorf(
			"resolve GitHub identity for %s via %s: response lacks a positive numeric user id",
			host, source.Descriptor().SafeString(),
		)
	}
	return GitHubIdentity{
		Key: IdentityKey{
			Host:      normalizedPlatformHost(host),
			Principal: fmt.Sprintf("user:%d", user.ID),
		},
		Login: strings.TrimSpace(user.Login),
	}, nil
}

// InstallationIdentity returns the principal used by GitHub App installation
// access tokens.
func InstallationIdentity(host string, installationID int64) GitHubIdentity {
	return GitHubIdentity{Key: IdentityKey{
		Host:      normalizedPlatformHost(host),
		Principal: fmt.Sprintf("installation:%d", installationID),
	}}
}

func authenticatedUserEndpoint(host string) string {
	if normalizedPlatformHost(host) == "github.com" {
		return "https://api.github.com/user"
	}
	return "https://" + normalizedPlatformHost(host) + "/api/v3/user"
}

func identityHTTPClientForHost(
	host string, source tokenauth.Source,
) *http.Client {
	origin := restAPIOriginForHost(host)
	return &http.Client{Transport: wrapPublicGitHubAPIGuard(tokenauth.AuthTransport{
		Source:              source,
		Base:                http.DefaultTransport,
		SetHeader:           tokenauth.BearerAuthHeader,
		RetryOnUnauthorized: true,
		AllowedOrigin:       origin,
	})}
}

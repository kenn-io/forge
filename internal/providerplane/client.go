package providerplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	gitremote "go.kenn.io/kit/git/remote"
)

const (
	defaultRequestBodyLimit  = 8 << 20
	defaultClientTimeout     = 15 * time.Second
	defaultResponseBodyLimit = 32 << 20
)

var (
	ErrHubUnavailable        = errors.New("federation hub unavailable")
	ErrCredentialUnavailable = errors.New("hub federation credential unavailable")
	ErrInvalidScope          = errors.New("scope is not owned by the provider plane")
	ErrRequestBodyTooLarge   = errors.New("provider request body exceeds limit")
	ErrResponseBodyTooLarge  = errors.New("provider response body exceeds limit")
)

// ResponseError preserves a hub's non-success response for the
// caller's domain adapter to decode into its public error contract.
type ResponseError struct {
	Status int
	Header http.Header
	Body   []byte
}

func (e *ResponseError) Error() string {
	if e == nil {
		return "<nil hub response>"
	}
	return fmt.Sprintf("hub returned HTTP %d", e.Status)
}

// CredentialSource supplies the current outbound bearer. Looking it up for
// every request makes credential rotation and revocation effective without a
// client rebuild.
type CredentialSource interface {
	Outbound(nodeID string) (federationauth.Credential, bool)
}

// Client sends one provider request to the federation hub.
type Client interface {
	Do(context.Context, federationauth.Scope, *http.Request) (*http.Response, error)
}

// WriteAdmitter is the narrow mutation-admission contract shared by HTTP,
// MCP, workspace automation, and deferred work without coupling those
// packages to the gate's concrete implementation.
type WriteAdmitter interface {
	Admit(context.Context) (release func(), err error)
}

// WorkspaceLaunchRequest carries the route and request intent needed for the
// hub to issue an immutable workspace launch specification. The
// provider facts themselves are returned in the specification and are never
// supplied by a spoke.
type WorkspaceLaunchRequest struct {
	Repository      RepositoryRoute `json:"repository"`
	PlatformRepoID  string          `json:"platform_repo_id,omitempty"`
	ItemType        string          `json:"item_type"`
	ItemNumber      int             `json:"item_number"`
	ItemKey         string          `json:"item_key,omitempty"`
	GitHeadRef      string          `json:"git_head_ref,omitempty"`
	IssueBranchSlug bool            `json:"issue_branch_slug,omitempty"`
}

// WorkspaceLaunchSpecResolver is the single authority used to resolve initial
// provider facts and renew an expired source-visibility lease. Hubs
// implement it locally; nodes implement it through their authenticated
// provider-plane client.
type WorkspaceLaunchSpecResolver interface {
	ResolveWorkspaceLaunchSpec(
		context.Context, WorkspaceLaunchRequest,
	) (db.WorkspaceLaunchSpec, error)
	RefreshWorkspaceLaunchSpec(
		context.Context, db.WorkspaceLaunchSpec,
	) (db.WorkspaceLaunchSpec, error)
}

// ValidateWorkspaceLaunchSpecResponse verifies that hub-issued
// launch facts answer the exact request and cannot redirect spoke-local Git
// credentials to a different repository or host.
func ValidateWorkspaceLaunchSpecResponse(
	request WorkspaceLaunchRequest,
	spec db.WorkspaceLaunchSpec,
) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	requestedRoute, err := CanonicalRepositoryRoute(request.Repository)
	if err != nil {
		return err
	}
	specRoute := RepositoryRoute{
		Provider: spec.Repository.Provider, PlatformHost: spec.Repository.PlatformHost,
		Owner: spec.Repository.Owner, Name: spec.Repository.Name,
	}
	canonicalSpecRoute, err := CanonicalRepositoryRoute(specRoute)
	if err != nil {
		return err
	}
	if specRoute != canonicalSpecRoute {
		return errors.New("workspace launch repository route is not canonical")
	}
	stableRepoID := strings.TrimSpace(request.PlatformRepoID)
	if stableRepoID != "" && spec.Repository.PlatformRepoID != stableRepoID {
		return errors.New("workspace launch repository identity does not match the request")
	}
	if canonicalSpecRoute.Provider != requestedRoute.Provider ||
		canonicalSpecRoute.PlatformHost != requestedRoute.PlatformHost {
		return errors.New("workspace launch repository route does not match the request")
	}
	if (stableRepoID == "" &&
		(canonicalSpecRoute.Owner != requestedRoute.Owner ||
			canonicalSpecRoute.Name != requestedRoute.Name)) ||
		spec.ItemType != request.ItemType ||
		spec.ItemNumber != request.ItemNumber {
		return errors.New("workspace launch specification does not match the request")
	}
	if request.ItemKey != "" && spec.ItemKey != request.ItemKey {
		return errors.New("workspace launch item key does not match the request")
	}
	if requestedHead := strings.TrimSpace(request.GitHeadRef); requestedHead != "" &&
		spec.GitHeadRef != requestedHead {
		return errors.New("workspace launch branch does not match the request")
	}
	if err := gitremote.ValidateRemoteIdentity(gitremote.Identity{
		Host:  spec.Repository.PlatformHost,
		Owner: spec.Repository.Owner,
		Name:  spec.Repository.Name,
	}, spec.Repository.CloneURL); err != nil {
		return fmt.Errorf("workspace launch clone URL: %w", err)
	}
	if spec.Pull != nil && spec.Pull.HeadRepoKind == "fork" {
		if err := gitremote.ValidateRemoteHost(
			spec.Repository.PlatformHost, spec.Pull.HeadRepoCloneURL,
		); err != nil {
			return fmt.Errorf("workspace launch fork clone URL: %w", err)
		}
	}
	return nil
}

// ValidateFederationWorkspaceLaunchSpecResponse applies the additional trust
// boundary for clone URLs received from another daemon. Standalone Forge may
// use local Git remotes, but federation never accepts hub-supplied
// filesystem content.
func ValidateFederationWorkspaceLaunchSpecResponse(
	request WorkspaceLaunchRequest, spec db.WorkspaceLaunchSpec,
) error {
	if err := ValidateWorkspaceLaunchSpecResponse(request, spec); err != nil {
		return err
	}
	if err := validateFederationNetworkRemote(spec.Repository.CloneURL); err != nil {
		return fmt.Errorf("workspace launch clone URL: %w", err)
	}
	if spec.Pull != nil && spec.Pull.HeadRepoKind == "fork" {
		if err := validateFederationNetworkRemote(spec.Pull.HeadRepoCloneURL); err != nil {
			return fmt.Errorf("workspace launch fork clone URL: %w", err)
		}
		if _, err := federationRemoteRepositoryRoute(
			spec.Repository.Provider, spec.Repository.PlatformHost,
			spec.Pull.HeadRepoCloneURL,
		); err != nil {
			return fmt.Errorf("workspace launch fork clone URL: %w", err)
		}
	}
	return nil
}

func validateFederationNetworkRemote(remoteURL string) error {
	if gitremote.RemoteHost(remoteURL) == "" || gitremote.RemoteRepoPath(remoteURL) == "" {
		return errors.New("federation clone URL must be a hosted network remote")
	}
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		if !strings.Contains(remoteURL, "://") {
			// Git's host:path spelling is an SSH remote, not a URL.
			return nil
		}
		return errors.New("federation clone URL must use HTTPS or SSH")
	}
	if parsed.Host == "" {
		// Git's host:path spelling is an SSH remote, not a URL scheme.
		return nil
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https", "ssh", "git+ssh", "ssh+git":
		return nil
	case "http":
		hostname := parsed.Hostname()
		if strings.EqualFold(strings.TrimSuffix(hostname, "."), "localhost") {
			return nil
		}
		if ip := net.ParseIP(hostname); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return errors.New("federation clone URL must use HTTPS or SSH; HTTP is allowed only for loopback")
}

func federationRemoteRepositoryRoute(
	provider, host, remoteURL string,
) (RepositoryRoute, error) {
	repoPath := gitremote.RemoteRepoPath(remoteURL)
	lastSlash := strings.LastIndex(repoPath, "/")
	if lastSlash <= 0 || lastSlash == len(repoPath)-1 {
		return RepositoryRoute{}, errors.New("federation clone URL must identify a repository owner and name")
	}
	return CanonicalRepositoryRoute(RepositoryRoute{
		Provider: provider, PlatformHost: host,
		Owner: repoPath[:lastSlash], Name: repoPath[lastSlash+1:],
	})
}

// ReadJSON performs one bounded provider-plane exchange. Successful bodies
// decode into target; non-success bodies are returned unchanged in a
// ResponseError so the server layer can retain the hub problem code.
func ReadJSON(
	ctx context.Context,
	client Client,
	scope federationauth.Scope,
	request *http.Request,
	target any,
) error {
	if client == nil {
		return ErrHubUnavailable
	}
	response, err := client.Do(ctx, scope, request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, defaultResponseBodyLimit+1))
	if err != nil {
		return fmt.Errorf("read hub response: %w", err)
	}
	if len(body) > defaultResponseBodyLimit {
		return ErrResponseBodyTooLarge
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return &ResponseError{
			Status: response.StatusCode,
			Header: response.Header.Clone(),
			Body:   body,
		}
	}
	if target == nil || response.StatusCode == http.StatusNoContent || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode hub response: %w", err)
	}
	return nil
}

// Options configures a hub client.
type Options struct {
	LocalNodeID string
	Hub         Hub
	Credentials CredentialSource
	HTTPClient  *http.Client
	BodyLimit   int64
}

type hubClient struct {
	localNodeID  string
	hub          Hub
	credentials  CredentialSource
	httpClient   *http.Client
	streamClient *http.Client
	bodyLimit    int64
	origin       *url.URL
}

// NewClient constructs an origin-bound, redirect-refusing provider client.
func NewClient(options Options) (Client, error) {
	localNodeID := strings.TrimSpace(options.LocalNodeID)
	if !federation.ValidNodeID(localNodeID) {
		return nil, fmt.Errorf("local node ID is invalid")
	}
	hub, err := options.Hub.validate()
	if err != nil {
		return nil, err
	}
	origin, err := url.Parse(hub.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse hub origin: %w", err)
	}
	bodyLimit := options.BodyLimit
	if bodyLimit <= 0 {
		bodyLimit = defaultRequestBodyLimit
	}
	return &hubClient{
		localNodeID:  localNodeID,
		hub:          hub,
		credentials:  options.Credentials,
		httpClient:   hardenedClient(options.HTTPClient),
		streamClient: hardenedStreamingClient(options.HTTPClient),
		bodyLimit:    bodyLimit,
		origin:       origin,
	}, nil
}

func (c *hubClient) Do(
	ctx context.Context,
	scope federationauth.Scope,
	request *http.Request,
) (*http.Response, error) {
	if scope != federationauth.ScopeProviderRead &&
		scope != federationauth.ScopeProviderWrite &&
		scope != federationauth.ScopeProviderHandoff &&
		scope != federationauth.ScopeEventsRead {
		return nil, ErrInvalidScope
	}
	if request == nil || request.URL == nil {
		return nil, errors.New("provider request is required")
	}
	if !strings.HasPrefix(request.URL.Path, "/api/v1/") {
		return nil, fmt.Errorf("provider request path must be under /api/v1")
	}
	if c.credentials == nil {
		return nil, ErrCredentialUnavailable
	}
	credential, ok := c.credentials.Outbound(c.hub.NodeID)
	if !ok || !slices.Contains(credential.Scopes, scope) {
		return nil, ErrCredentialUnavailable
	}

	body, err := readBoundedRequestBody(request.Body, c.bodyLimit)
	if err != nil {
		return nil, err
	}
	target := *c.origin
	target.Path = request.URL.Path
	target.RawPath = request.URL.RawPath
	target.RawQuery = request.URL.RawQuery
	proxied := request.Clone(ctx)
	proxied.URL = &target
	proxied.RequestURI = ""
	proxied.Host = ""
	proxied.Header = make(http.Header)
	copySafeRequestHeaders(proxied.Header, request.Header)
	proxied.Header.Set("Authorization", "Bearer "+credential.Token)
	proxied.Header.Set(federationauth.NodeIDHeader, c.localNodeID)
	proxied.Header.Set(ProtocolVersionHeader, ProtocolVersionHeaderValue())
	if request.Body != nil {
		proxied.Body = io.NopCloser(bytes.NewReader(body))
		proxied.ContentLength = int64(len(body))
		proxied.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}

	httpClient := c.httpClient
	if scope == federationauth.ScopeEventsRead {
		httpClient = c.streamClient
	}
	response, err := httpClient.Do(proxied)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHubUnavailable, err)
	}
	return response, nil
}

func readBoundedRequestBody(body io.ReadCloser, limit int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	defer body.Close()
	contents, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read provider request body: %w", err)
	}
	if int64(len(contents)) > limit {
		return nil, ErrRequestBodyTooLarge
	}
	return contents, nil
}

func hardenedClient(base *http.Client) *http.Client {
	client := hardenedClientWithTimeout(base, defaultClientTimeout)
	if base != nil && base.Timeout > 0 && base.Timeout <= defaultClientTimeout {
		client.Timeout = base.Timeout
	}
	return client
}

// hardenedStreamingClient retains connect, TLS, response-header, redirect,
// and header-size bounds while leaving the response body lifetime to the
// caller's context. A whole-request timeout would terminate a healthy SSE
// stream after an arbitrary wall-clock interval.
func hardenedStreamingClient(base *http.Client) *http.Client {
	return hardenedClientWithTimeout(base, 0)
}

func hardenedClientWithTimeout(base *http.Client, timeout time.Duration) *http.Client {
	client := &http.Client{}
	if base != nil {
		*client = *base
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client.Timeout = timeout

	var transport *http.Transport
	switch existing := client.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = existing.Clone()
	}
	if transport == nil {
		return client
	}
	transport.DialContext = (&net.Dialer{
		Timeout: 5 * time.Second, KeepAlive: 30 * time.Second,
	}).DialContext
	if transport.TLSHandshakeTimeout <= 0 || transport.TLSHandshakeTimeout > 5*time.Second {
		transport.TLSHandshakeTimeout = 5 * time.Second
	}
	if transport.ResponseHeaderTimeout <= 0 || transport.ResponseHeaderTimeout > 10*time.Second {
		transport.ResponseHeaderTimeout = 10 * time.Second
	}
	if transport.ExpectContinueTimeout <= 0 || transport.ExpectContinueTimeout > time.Second {
		transport.ExpectContinueTimeout = time.Second
	}
	if transport.MaxResponseHeaderBytes <= 0 || transport.MaxResponseHeaderBytes > 1<<20 {
		transport.MaxResponseHeaderBytes = 1 << 20
	}
	client.Transport = transport
	return client
}

func copySafeRequestHeaders(destination, source http.Header) {
	connectionHeaders := connectionTokens(source)
	for key, values := range source {
		lower := strings.ToLower(key)
		if isHopByHopHeader(lower) || connectionHeaders[lower] ||
			isCallerCredentialHeader(lower) || isCallerProvenanceHeader(lower) {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func connectionTokens(header http.Header) map[string]bool {
	tokens := make(map[string]bool)
	for _, value := range header.Values("Connection") {
		for token := range strings.SplitSeq(value, ",") {
			token = strings.ToLower(strings.TrimSpace(token))
			if token != "" {
				tokens[token] = true
			}
		}
	}
	return tokens
}

func isHopByHopHeader(lower string) bool {
	switch lower {
	case "connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func isCallerCredentialHeader(lower string) bool {
	return lower == "authorization" || lower == "cookie"
}

func isCallerProvenanceHeader(lower string) bool {
	return lower == "origin" || lower == "forwarded" ||
		strings.HasPrefix(lower, "sec-fetch-") ||
		strings.HasPrefix(lower, "x-forwarded-")
}

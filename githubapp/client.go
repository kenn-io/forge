package githubapp

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
)

// APIBaseForHost resolves the REST API base URL for a GitHub host:
// the public host uses api.github.com, GitHub Enterprise hosts serve
// the API under /api/v3.
func APIBaseForHost(host string) string {
	if host == "" || host == platform.DefaultGitHubHost {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

// WebBaseForHost resolves the browser-facing base URL for a host.
func WebBaseForHost(host string) string {
	if host == "" {
		host = platform.DefaultGitHubHost
	}
	return "https://" + host
}

// Client is a minimal GitHub App management client. It speaks only
// the app-scoped endpoints the kenn-forge-github-app CLI and the
// installation token minter need; repository data access stays on the
// main provider clients.
type Client struct {
	host       string
	apiBase    string
	httpClient *http.Client
	initErr    error
}

type ClientOption func(*Client)

// WithAPIBase separates transport configuration from provider instance identity.
func WithAPIBase(apiBase string) ClientOption {
	return func(c *Client) { c.apiBase = strings.TrimRight(apiBase, "/") }
}

// NewClient performs no I/O and uses only the caller's transport. Authentication
// is explicit on each operation; there is no credential discovery or fallback.
func NewClient(host string, httpClient *http.Client, options ...ClientOption) *Client {
	normalized, err := platform.NormalizeHost(platform.KindGitHub, host)
	c := &Client{host: normalized, apiBase: APIBaseForHost(normalized), httpClient: httpClient, initErr: err}
	for _, option := range options {
		option(c)
	}
	return c
}

// AppCredentials is the manifest conversion response: everything the
// new app needs to authenticate, returned exactly once by GitHub.
type AppCredentials struct {
	ID            int64   `json:"id"`
	Slug          string  `json:"slug"`
	Name          string  `json:"name"`
	ClientID      string  `json:"client_id"`
	ClientSecret  string  `json:"client_secret"`
	WebhookSecret *string `json:"webhook_secret"`
	PEM           string  `json:"pem"`
	HTMLURL       string  `json:"html_url"`
	Owner         Account `json:"owner"`
}

type Account struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

// App is the GET /app response subset kenn-forge cares about.
type App struct {
	ID      int64   `json:"id"`
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	HTMLURL string  `json:"html_url"`
	Owner   Account `json:"owner"`
}

// Installation is one account the app is installed on.
type Installation struct {
	ID                  int64      `json:"id"`
	AppID               int64      `json:"app_id"`
	Account             Account    `json:"account"`
	RepositorySelection string     `json:"repository_selection"`
	SuspendedAt         *time.Time `json:"suspended_at"`
}

// InstallationToken is a minted installation access token.
type InstallationToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RateLimit is the core REST rate budget for a credential.
type RateLimit struct {
	Limit     int   `json:"limit"`
	Remaining int   `json:"remaining"`
	Reset     int64 `json:"reset"`
}

// ConvertManifest exchanges a manifest flow code for the new app's
// credentials. The exchange needs no authentication and each code
// works exactly once, expiring after one hour.
func (c *Client) ConvertManifest(ctx context.Context, code string, meter *platform.Meter) (*AppCredentials, error) {
	if code == "" {
		return nil, fmt.Errorf("manifest conversion code is required")
	}
	var creds AppCredentials
	err := c.do(ctx, http.MethodPost,
		"/app-manifests/"+code+"/conversions", "", nil, &creds, meter)
	if err != nil {
		return nil, fmt.Errorf("converting app manifest code: %w", err)
	}
	return &creds, nil
}

// GetApp returns the authenticated app for an app JWT.
func (c *Client) GetApp(ctx context.Context, appJWT string, meter *platform.Meter) (*App, error) {
	var app App
	if err := c.do(ctx, http.MethodGet, "/app", appJWT, nil, &app, meter); err != nil {
		return nil, fmt.Errorf("getting app: %w", err)
	}
	return &app, nil
}

// CreateInstallationToken mints an installation access token. Tokens
// expire after one hour.
func (c *Client) CreateInstallationToken(
	ctx context.Context, appJWT string, installationID int64,
	scope TokenScope, meter *platform.Meter,
) (*InstallationToken, error) {
	body, err := scope.request()
	if err != nil {
		return nil, err
	}
	if installationID <= 0 {
		return nil, fmt.Errorf("installation ID must be positive")
	}
	var token InstallationToken
	err = c.do(ctx, http.MethodPost,
		fmt.Sprintf("/app/installations/%d/access_tokens", installationID),
		appJWT, body, &token, meter)
	if err != nil {
		return nil, fmt.Errorf(
			"creating installation token for installation %d: %w", installationID, err,
		)
	}
	return &token, nil
}

// DeleteInstallation uninstalls the app from an account.
func (c *Client) DeleteInstallation(
	ctx context.Context, appJWT string, installationID int64, meter *platform.Meter,
) error {
	if c.httpClient == nil {
		return &platform.Error{Code: platform.ErrCodeInvalidArgument, Provider: platform.KindGitHub, PlatformHost: c.host, Field: "http_client"}
	}
	err := c.do(ctx, http.MethodDelete,
		fmt.Sprintf("/app/installations/%d", installationID), appJWT, nil, nil, meter)
	if err != nil {
		return fmt.Errorf("deleting installation %d: %w", installationID, err)
	}
	return nil
}

// CoreRateLimit reports the core REST budget for a token.
func (c *Client) CoreRateLimit(ctx context.Context, token string, meter *platform.Meter) (*RateLimit, error) {
	var out struct {
		Resources struct {
			Core RateLimit `json:"core"`
		} `json:"resources"`
	}
	if err := c.do(ctx, http.MethodGet, "/rate_limit", token, nil, &out, meter); err != nil {
		return nil, fmt.Errorf("reading rate limit: %w", err)
	}
	return &out.Resources.Core, nil
}

// StatusError is a non-2xx API response.
type StatusError struct {
	StatusCode int
	Header     http.Header
	Body       string
}

func (e *StatusError) Error() string {
	msg := strings.TrimSpace(e.Body)
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	return fmt.Sprintf("github api status %d: %s", e.StatusCode, msg)
}

// RetryDeadline returns the server-provided time when a rate-limited request
// may be retried. Retry-After takes precedence over the primary rate-limit
// reset header when both are present.
func (e *StatusError) RetryDeadline(now time.Time) time.Time {
	if e == nil {
		return time.Time{}
	}
	if value := strings.TrimSpace(e.Header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
			return now.Add(time.Duration(seconds) * time.Second)
		}
		if at, err := http.ParseTime(value); err == nil {
			return at
		}
	}
	rateExhausted := strings.TrimSpace(e.Header.Get("X-RateLimit-Remaining")) == "0"
	reset := strings.TrimSpace(e.Header.Get("X-RateLimit-Reset"))
	if (e.StatusCode == http.StatusTooManyRequests || rateExhausted) &&
		reset != "" {
		if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil {
			return time.Unix(epoch, 0).UTC()
		}
	}
	return time.Time{}
}

// IsStatus reports whether err is a StatusError with the given code.
func IsStatus(err error, code int) bool {
	var se *StatusError
	if !errors.As(err, &se) || se == nil {
		return false
	}
	return se.StatusCode == code
}

func (c *Client) do(
	ctx context.Context, method, path, bearer string, body, out any, meter *platform.Meter,
) error {
	if meter == nil {
		return c.pageError(platform.ErrCodeInvalidArgument, "budget")
	}
	if err := meter.Records(1); err != nil {
		return err
	}
	_, data, err := c.request(ctx, method, path, bearer, body, meter)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return meter.CheckOutput(out)
}

func (c *Client) request(ctx context.Context, method, path, bearer string, body any, meter *platform.Meter) (http.Header, []byte, error) {
	if c.initErr != nil {
		return nil, nil, c.initErr
	}
	if c.httpClient == nil || meter == nil {
		return nil, nil, c.pageError(platform.ErrCodeInvalidArgument, "client_or_budget")
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, nil, c.pageError(platform.ErrCodeInvalidArgument, "deadline")
	}
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("encoding request body: %w", err)
		}
		if err := meter.Bytes(int64(len(data))); err != nil {
			return nil, nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, reqBody)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, data, err := meter.ReadHTTP(ctx, c.httpClient, req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		failure := &StatusError{
			StatusCode: resp.StatusCode,
			Header:     resp.Header.Clone(),
			Body:       string(data),
		}
		var code platform.PlatformErrorCode
		switch {
		case resp.StatusCode == http.StatusUnauthorized && bearer != "":
			code = platform.ErrCodeCredentialRejected
		case resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusForbidden && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "")):
			code = platform.ErrCodeRateLimited
		case resp.StatusCode == http.StatusForbidden:
			code = platform.ErrCodePermissionDenied
		case resp.StatusCode == http.StatusNotFound:
			code = platform.ErrCodeNotFound
		default:
			return nil, nil, failure
		}
		scope := "installation_token"
		if path == "/app" || strings.HasPrefix(path, "/app/") {
			scope = "app"
		}
		if bearer == "" {
			scope = "none"
		}
		return nil, nil, &platform.Error{Code: code, Provider: platform.KindGitHub, PlatformHost: c.host, Details: map[string]string{"credential_scope": scope}, Err: failure}
	}
	return resp.Header, data, nil
}

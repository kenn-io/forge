package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	platformpkg "go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/tokenauth"
)

const (
	defaultGitHubTokenEnv              = "MIDDLEMAN_GITHUB_TOKEN"
	defaultForgejoTokenEnv             = "MIDDLEMAN_FORGEJO_TOKEN"
	defaultGiteaTokenEnv               = "MIDDLEMAN_GITEA_TOKEN"
	defaultSyncInterval                = "5m"
	defaultHost                        = "127.0.0.1"
	defaultPort                        = 8091
	defaultViewMode                    = "threaded"
	defaultTimeRange                   = "7d"
	defaultBasePath                    = "/"
	defaultSyncBudgetPerHour           = 500
	defaultBranchActivityRetentionDays = 90
	defaultBranchActivityMaxCommits    = 5000
	defaultPlatform                    = "github"
	defaultPlatformHost                = platformpkg.DefaultGitHubHost
	defaultSSEBufferSize               = 256
	minSSEBufferSize                   = 16
	maxSSEBufferSize                   = 16384
)

const (
	// IssueWorkspaceBranchStyleSlug appends a slug derived from the
	// issue title onto middleman/issue-<n>, producing recognizable
	// branch names that match common GitHub workflow conventions.
	IssueWorkspaceBranchStyleSlug = "slug"
	// IssueWorkspaceBranchStyleBare keeps the original
	// middleman/issue-<n> form with no title slug appended.
	IssueWorkspaceBranchStyleBare = "bare"

	defaultIssueWorkspaceBranchStyle = IssueWorkspaceBranchStyleSlug
)

type Repo struct {
	Owner        string `toml:"owner" json:"owner"`
	Name         string `toml:"name" json:"name"`
	RepoPath     string `toml:"repo_path,omitempty" json:"repo_path,omitempty"`
	Platform     string `toml:"platform,omitempty" json:"platform,omitempty"`
	PlatformHost string `toml:"platform_host,omitempty" json:"platform_host,omitempty"`
	TokenEnv     string `toml:"token_env,omitempty" json:"token_env,omitempty"`
	TokenFile    string `toml:"token_file,omitempty" json:"token_file,omitempty"`
}

// DocFolder names a markdown folder registered for docs mode. Path
// normalization and existence checks are handled by the docs registry
// when the folder is used or edited.
type DocFolder struct {
	ID     string `toml:"id" json:"id"`
	Name   string `toml:"name" json:"name"`
	Path   string `toml:"path" json:"path"`
	Daemon string `toml:"daemon,omitempty" json:"daemon,omitempty"`
}

type PlatformConfig struct {
	Type      string `toml:"type" json:"type"`
	Host      string `toml:"host" json:"host"`
	TokenEnv  string `toml:"token_env,omitempty" json:"token_env,omitempty"`
	TokenFile string `toml:"token_file,omitempty" json:"token_file,omitempty"`
}

func (r Repo) FullName() string {
	return r.Owner + "/" + r.Name
}

func (r Repo) HasNameGlob() bool {
	return strings.ContainsAny(r.Name, "*?[")
}

// PlatformHostOrDefault returns the configured platform host,
// defaulting to the provider's public host when empty.
func (r Repo) PlatformHostOrDefault() string {
	if r.PlatformHost == "" {
		if host, ok := platformpkg.DefaultHost(platformpkg.Kind(r.PlatformOrDefault())); ok {
			return host
		}
		return defaultPlatformHost
	}
	return r.PlatformHost
}

func (r Repo) PlatformOrDefault() string {
	if r.Platform == "" {
		return defaultPlatform
	}
	return r.Platform
}

// ResolveToken returns the token for this repo. When TokenEnv is
// set, it reads from that env var. Falls back to globalToken if
// the env var is empty or TokenEnv is not set.
func (r Repo) ResolveToken(globalToken string) string {
	if r.TokenEnv != "" {
		if tok := os.Getenv(r.TokenEnv); tok != "" {
			return tok
		}
	}
	return globalToken
}

// normalize cleans up a Repo entry, extracting platform, host,
// owner, and name from provider URLs or SSH addresses if the user
// pasted one into either field. It also strips a trailing .git suffix.
func (r *Repo) normalize(defaultGitHubHost string) error {
	hadPlatformHost := strings.TrimSpace(r.PlatformHost) != ""
	platform, err := normalizePlatform(r.Platform)
	if err != nil {
		return err
	}
	r.Platform = platform

	// Check if either field contains a full GitHub URL or SSH
	// address. If so, extract owner/name from it.
	for _, raw := range []string{r.Owner, r.Name} {
		ref, err := parseRepoRef(raw, r.Platform)
		if err != nil {
			return err
		}
		if ref.owner != "" {
			r.Platform = ref.platform
			if !hadPlatformHost {
				r.PlatformHost = ref.host
				hadPlatformHost = true
			}
			r.Owner = ref.owner
			r.Name = ref.name
			r.RepoPath = ref.owner + "/" + ref.name
			break
		}
	}

	r.RepoPath = cleanPath(strings.TrimSpace(r.RepoPath))
	if r.RepoPath != "" && (strings.TrimSpace(r.Owner) == "" || strings.TrimSpace(r.Name) == "") {
		if platformpkg.AllowsNestedOwner(platformpkg.Kind(r.Platform)) {
			owner, name, err := splitGitLabPath("repo_path", r.RepoPath)
			if err != nil {
				return err
			}
			r.Owner = owner
			r.Name = name
		} else {
			owner, name, err := splitGitHubPath("repo_path", r.RepoPath)
			if err != nil {
				return err
			}
			r.Owner = owner
			r.Name = name
		}
	}
	r.Name = strings.TrimSuffix(r.Name, ".git")
	if r.Owner == "" || r.Name == "" {
		return errors.New("must have owner and name")
	}
	if platformpkg.LowercaseRepoNames(platformpkg.Kind(r.Platform)) {
		r.Owner = strings.ToLower(r.Owner)
		r.Name = strings.ToLower(r.Name)
		if r.RepoPath != "" {
			r.RepoPath = strings.ToLower(r.RepoPath)
		}
	}
	r.PlatformHost, err = normalizePlatformHost(r.Platform, r.PlatformHost)
	if err != nil {
		return err
	}
	if r.Platform == defaultPlatform && !hadPlatformHost {
		r.PlatformHost = defaultGitHubHost
	}
	if r.Platform == defaultPlatform &&
		r.PlatformHost == defaultPlatformHost &&
		defaultGitHubHost == defaultPlatformHost &&
		!hadPlatformHost {
		r.PlatformHost = ""
	}
	return nil
}

func (r Repo) ownerHasGlob() bool {
	return strings.ContainsAny(r.Owner, "*?[")
}

type parsedRepoRef struct {
	platform string
	host     string
	owner    string
	name     string
}

func parseRepoRef(raw, configuredPlatform string) (parsedRepoRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return parsedRepoRef{}, nil
	}

	platform, err := normalizePlatform(configuredPlatform)
	if err != nil {
		return parsedRepoRef{}, err
	}

	var host, path string
	switch {
	case strings.HasPrefix(raw, "ssh://"):
		u, err := url.Parse(raw)
		if err != nil {
			return parsedRepoRef{}, fmt.Errorf("invalid SSH URI %q: %w", raw, err)
		}
		host = strings.ToLower(u.Hostname())
		path = strings.TrimPrefix(u.Path, "/")
	case strings.HasPrefix(raw, "http://") ||
		strings.HasPrefix(raw, "https://"):
		u, err := url.Parse(raw)
		if err != nil {
			return parsedRepoRef{}, fmt.Errorf("invalid repository URL %q: %w", raw, err)
		}
		host = strings.ToLower(u.Host)
		path = strings.TrimPrefix(u.Path, "/")
	default:
		if m := scpRepoRe.FindStringSubmatch(raw); m != nil {
			host = strings.ToLower(m[1])
			path = m[2]
		} else if m := bareHostRepoRe.FindStringSubmatch(raw); m != nil {
			host = strings.ToLower(m[1])
			path = m[2]
		} else {
			return parsedRepoRef{}, nil
		}
	}

	if host == "" {
		return parsedRepoRef{}, nil
	}
	refPlatform, ok := platformForRepoRefHost(host, platform)
	if !ok {
		return parsedRepoRef{}, nil
	}

	path = cleanPath(path)
	if platformpkg.AllowsNestedOwner(platformpkg.Kind(refPlatform)) {
		owner, name, err := splitGitLabPath(raw, path)
		if err != nil {
			return parsedRepoRef{}, err
		}
		return parsedRepoRef{
			platform: refPlatform,
			host:     normalizePublicHost(host),
			owner:    owner,
			name:     name,
		}, nil
	}
	{
		owner, name, err := splitGitHubPath(raw, path)
		if err != nil {
			return parsedRepoRef{}, err
		}
		return parsedRepoRef{
			platform: refPlatform,
			host:     normalizePublicHost(host),
			owner:    owner,
			name:     name,
		}, nil
	}
}

func splitGitHubPath(raw, path string) (string, string, error) {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf(
			"incomplete GitHub reference %q: expected owner/repo", raw,
		)
	}
	return parts[0], parts[1], nil
}

func splitGitLabPath(raw, path string) (string, string, error) {
	parts := stripGitLabWebUISuffix(strings.Split(path, "/"))
	if len(parts) < 2 || parts[0] == "" || parts[len(parts)-1] == "" {
		return "", "", fmt.Errorf(
			"incomplete GitLab reference %q: expected namespace/repo", raw,
		)
	}
	return strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1], nil
}

func stripGitLabWebUISuffix(parts []string) []string {
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] != "-" {
			continue
		}
		switch parts[i+1] {
		case "merge_requests", "issues", "tree", "blob", "commit", "commits":
			return parts[:i]
		}
	}
	return parts
}

func platformForRepoRefHost(host, configuredPlatform string) (string, bool) {
	host = normalizePublicHost(host)
	matchHost := hostNameForMatch(host)
	if configuredPlatform != defaultPlatform {
		return configuredPlatform, true
	}
	if configuredPlatform == defaultPlatform {
		if matchHost == defaultPlatformHost {
			return defaultPlatform, true
		}
		if matchHost == platformpkg.DefaultForgejoHost {
			return string(platformpkg.KindForgejo), true
		}
		if matchHost == platformpkg.DefaultGiteaHost {
			return string(platformpkg.KindGitea), true
		}
		return "", false
	}
	return "", false
}

func hostNameForMatch(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

func normalizePublicHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if before, ok := strings.CutSuffix(host, ":443"); ok {
		return before
	}
	return host
}

func normalizePlatform(raw string) (string, error) {
	kind, err := platformpkg.NormalizeKind(raw)
	if err != nil {
		return "", err
	}
	return string(kind), nil
}

// NormalizePlatformHost normalizes a configured provider host and rejects
// URL authority forms that could redirect provider tokens through userinfo or
// malformed host parsing.
func NormalizePlatformHost(platform, raw string) (string, error) {
	return normalizePlatformHost(platform, raw)
}

func normalizePlatformHost(platform, raw string) (string, error) {
	platform, err := normalizePlatform(platform)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" {
		if defaultHost, ok := platformpkg.DefaultHost(platformpkg.Kind(platform)); ok {
			return defaultHost, nil
		}
		return "", fmt.Errorf("platform_host is required for platform %q", platform)
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		u, err := url.Parse(host)
		if err != nil {
			return "", fmt.Errorf("invalid_repo_ref: invalid platform_host %q: %w", raw, err)
		}
		if u.User != nil {
			return "", fmt.Errorf("invalid_repo_ref: platform_host %q must not include userinfo", raw)
		}
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf(
				"invalid_repo_ref: platform_host %q must be a host; subpath installs are not supported",
				raw,
			)
		}
		host = u.Host
	} else {
		host = strings.TrimRight(host, "/")
		if strings.Contains(host, "/") {
			return "", fmt.Errorf(
				"invalid_repo_ref: platform_host %q must be a host; subpath installs are not supported",
				raw,
			)
		}
	}
	return normalizePlatformHostAuthority(raw, host)
}

func normalizePlatformHostAuthority(raw, host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", fmt.Errorf("invalid_repo_ref: platform_host %q is empty", raw)
	}
	if strings.Contains(host, "@") {
		return "", fmt.Errorf("invalid_repo_ref: platform_host %q must not include userinfo", raw)
	}
	if strings.ContainsFunc(host, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) {
		return "", fmt.Errorf("invalid_repo_ref: platform_host %q contains invalid characters", raw)
	}
	if err := validatePlatformHostPort(raw, host); err != nil {
		return "", err
	}

	parsed, err := url.Parse("//" + host)
	if err != nil {
		return "", fmt.Errorf("invalid_repo_ref: invalid platform_host %q: %w", raw, err)
	}
	if parsed.User != nil || parsed.Hostname() == "" || parsed.Path != "" {
		return "", fmt.Errorf("invalid_repo_ref: platform_host %q must be a host", raw)
	}
	return normalizePublicHost(host), nil
}

func validatePlatformHostPort(raw, host string) error {
	if strings.HasPrefix(host, "[") {
		closing := strings.LastIndex(host, "]")
		if closing == -1 {
			return fmt.Errorf("invalid_repo_ref: invalid platform_host %q", raw)
		}
		if closing == len(host)-1 {
			return nil
		}
		if host[closing+1] != ':' {
			return fmt.Errorf("invalid_repo_ref: invalid platform_host %q", raw)
		}
		return validatePlatformHostPortNumber(raw, host[closing+2:])
	}

	colonCount := strings.Count(host, ":")
	switch colonCount {
	case 0:
		return nil
	case 1:
		_, port, _ := strings.Cut(host, ":")
		return validatePlatformHostPortNumber(raw, port)
	default:
		return fmt.Errorf("invalid_repo_ref: platform_host %q must bracket IPv6 literals", raw)
	}
}

func validatePlatformHostPortNumber(raw, port string) error {
	if port == "" {
		return fmt.Errorf("invalid_repo_ref: platform_host %q has an empty port", raw)
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return fmt.Errorf("invalid_repo_ref: platform_host %q has a non-numeric port", raw)
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber > 65535 {
		return fmt.Errorf("invalid_repo_ref: platform_host %q has an invalid port", raw)
	}
	return nil
}

// cleanPath strips query strings, fragments, trailing slashes,
// and an optional .git suffix from a GitHub ref path.
func cleanPath(path string) string {
	if idx := strings.IndexAny(path, "?#"); idx != -1 {
		path = path[:idx]
	}
	path = strings.TrimRight(path, "/")
	path = strings.TrimSuffix(path, ".git")
	return path
}

type Activity struct {
	ViewMode                   string `toml:"view_mode" json:"view_mode" enum:"flat,threaded"`
	TimeRange                  string `toml:"time_range" json:"time_range" enum:"24h,7d,30d,90d"`
	HideClosed                 bool   `toml:"hide_closed" json:"hide_closed"`
	HideBots                   bool   `toml:"hide_bots" json:"hide_bots"`
	CollapseThreads            bool   `toml:"collapse_threads" json:"collapse_threads"`
	DefaultBranchRetentionDays int    `toml:"default_branch_retention_days" json:"default_branch_retention_days"`
	DefaultBranchMaxCommits    int    `toml:"default_branch_max_commits" json:"default_branch_max_commits"`
}

const (
	TerminalRendererXterm   = "xterm"
	TerminalRendererGhostty = "ghostty-web"

	DefaultTerminalFontSize      = 14
	DefaultTerminalScrollback    = 1000
	DefaultTerminalLineHeight    = 1.0
	DefaultTerminalLetterSpacing = 0
	DefaultTerminalCursorBlink   = true
	DefaultTerminalFontLigatures = false
)

type Terminal struct {
	FontFamily    string  `toml:"font_family,omitempty" json:"font_family"`
	FontSize      int     `toml:"font_size,omitempty" json:"font_size"`
	Scrollback    int     `toml:"scrollback,omitempty" json:"scrollback"`
	LineHeight    float64 `toml:"line_height,omitempty" json:"line_height"`
	LetterSpacing int     `toml:"letter_spacing,omitempty" json:"letter_spacing"`
	CursorBlink   *bool   `toml:"cursor_blink,omitempty" json:"cursor_blink" nullable:"false"`
	FontLigatures bool    `toml:"font_ligatures,omitempty" json:"font_ligatures"`
	Renderer      string  `toml:"renderer,omitempty" json:"renderer" enum:"xterm,ghostty-web"`
}

type Agent struct {
	Key     string   `toml:"key" json:"key"`
	Label   string   `toml:"label,omitempty" json:"label"`
	Command []string `toml:"command,omitempty" json:"command,omitempty" nullable:"false"`
	Enabled *bool    `toml:"enabled,omitempty" json:"enabled,omitempty"`
}

func (a Agent) EnabledOrDefault() bool {
	return a.Enabled == nil || *a.Enabled
}

type Roborev struct {
	Endpoint string `toml:"endpoint,omitempty"`
}

// Msgvault configures the external msgvault server used by the Messages UI.
// Secrets are resolved from APIKeyEnv at runtime and are never stored on the
// serializable config object.
type Msgvault struct {
	URL       string `toml:"url,omitempty" json:"url,omitempty"`
	APIKeyEnv string `toml:"api_key_env,omitempty" json:"api_key_env,omitempty"`
}

// ModeVisibility controls which top-level app modes are shown. Nil booleans
// mean the mode uses its default visibility.
type ModeVisibility struct {
	Activity   *bool `toml:"activity,omitempty" json:"activity" nullable:"false"`
	Repos      *bool `toml:"repos,omitempty" json:"repos" nullable:"false"`
	Kata       *bool `toml:"kata,omitempty" json:"kata" nullable:"false"`
	Docs       *bool `toml:"docs,omitempty" json:"docs" nullable:"false"`
	Messages   *bool `toml:"messages,omitempty" json:"messages" nullable:"false"`
	Pulls      *bool `toml:"pulls,omitempty" json:"pulls" nullable:"false"`
	Issues     *bool `toml:"issues,omitempty" json:"issues" nullable:"false"`
	Board      *bool `toml:"board,omitempty" json:"board" nullable:"false"`
	Reviews    *bool `toml:"reviews,omitempty" json:"reviews" nullable:"false"`
	Workspaces *bool `toml:"workspaces,omitempty" json:"workspaces" nullable:"false"`
}

func DefaultModeVisibility() ModeVisibility {
	return ModeVisibility{
		Activity:   new(true),
		Repos:      new(true),
		Kata:       new(false),
		Docs:       new(false),
		Messages:   new(false),
		Pulls:      new(true),
		Issues:     new(true),
		Board:      new(true),
		Reviews:    new(true),
		Workspaces: new(true),
	}
}

func (m ModeVisibility) WithDefaults() ModeVisibility {
	defaults := DefaultModeVisibility()
	if m.Activity != nil {
		defaults.Activity = m.Activity
	}
	if m.Repos != nil {
		defaults.Repos = m.Repos
	}
	if m.Kata != nil {
		defaults.Kata = m.Kata
	}
	if m.Docs != nil {
		defaults.Docs = m.Docs
	}
	if m.Messages != nil {
		defaults.Messages = m.Messages
	}
	if m.Pulls != nil {
		defaults.Pulls = m.Pulls
	}
	if m.Issues != nil {
		defaults.Issues = m.Issues
	}
	if m.Board != nil {
		defaults.Board = m.Board
	}
	if m.Reviews != nil {
		defaults.Reviews = m.Reviews
	}
	if m.Workspaces != nil {
		defaults.Workspaces = m.Workspaces
	}
	return defaults
}

type MsgvaultState int

const (
	MsgvaultAbsent MsgvaultState = iota
	MsgvaultMisconfigured
	MsgvaultOK
)

type Tmux struct {
	Command       []string `toml:"command,omitempty"`
	AgentSessions *bool    `toml:"agent_sessions,omitempty"`
}

// Shell configures the command middleman runs when ensuring the
// per-workspace plain shell session. Hardened middleman deployments
// (e.g. systemd services with SystemCallFilter=~@privileged) must
// wrap the shell so it escapes the parent's seccomp filter — zsh
// calls setresuid during startup and is killed by SIGSYS otherwise.
// The configured command is invoked with the workspace worktree as
// its working directory; provide a command that propagates that to
// the spawned shell (e.g. `systemd-run --working-directory=...`).
type Shell struct {
	Command []string `toml:"command,omitempty"`
}

type Config struct {
	SyncInterval              string `toml:"sync_interval"`
	GitHubTokenEnv            string `toml:"github_token_env"`
	DefaultPlatformHost       string `toml:"default_platform_host"`
	Host                      string `toml:"host"`
	Port                      int    `toml:"port"`
	BasePath                  string `toml:"base_path"`
	DataDir                   string `toml:"data_dir"`
	SyncBudgetPerHour         int    `toml:"sync_budget_per_hour"`
	SSEBufferSize             int    `toml:"sse_buffer_size"`
	IssueWorkspaceBranchStyle string `toml:"issue_workspace_branch_style"`
	// AllowedHosts is an exact-match allowlist of Host header values
	// beyond the bind address that the Host validation middleware
	// should accept. Loopback synonyms (127.0.0.1 / localhost /
	// [::1]) at the bind port are auto-accepted and do not need to
	// be listed.
	AllowedHosts []string `toml:"allowed_hosts"`
	// TrustReverseProxy enables honoring X-Forwarded-Host and
	// Forwarded RFC 7239 host= for the Public Host validation step.
	// The raw Host header must still pass the allowed_hosts gate
	// before any forwarded header is read.
	TrustReverseProxy bool             `toml:"trust_reverse_proxy"`
	Repos             []Repo           `toml:"repos"`
	Platforms         []PlatformConfig `toml:"platforms"`
	Activity          Activity         `toml:"activity"`
	Terminal          Terminal         `toml:"terminal"`
	Modes             ModeVisibility   `toml:"modes"`
	Agents            []Agent          `toml:"agents"`
	DocFolders        []DocFolder      `toml:"doc_folders"`
	Roborev           Roborev          `toml:"roborev"`
	Msgvault          *Msgvault        `toml:"msgvault"`
	Tmux              Tmux             `toml:"tmux"`
	Shell             Shell            `toml:"shell"`

	// parsedAllowedHosts is the canonicalised form of AllowedHosts,
	// populated by Validate so the server constructor does not have
	// to re-parse on every request setup. Defensive copy via
	// ParsedAllowedHosts.
	parsedAllowedHosts []HostKey
	// parsedBindKey is the canonical (Host, Port) key for the bind
	// address, populated by Validate.
	parsedBindKey HostKey
}

// SSEBufferSizeOrDefault returns the configured SSE replay ring size,
// falling back to the package default. A nil receiver is treated as
// fully default-configured so tests that pass cfg = nil into the
// server still get a working ring size.
func (c *Config) SSEBufferSizeOrDefault() int {
	if c == nil || c.SSEBufferSize == 0 {
		return defaultSSEBufferSize
	}
	return c.SSEBufferSize
}

// IsLoopbackHostname reports whether a URL hostname (no port, no
// brackets) is localhost or a loopback IP literal. Hostnames that
// merely resolve to loopback are not recognized.
func IsLoopbackHostname(hostname string) bool {
	if strings.EqualFold(strings.TrimSuffix(hostname, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

// MsgvaultState classifies the optional msgvault config and resolves the API
// key from the configured environment variable when possible.
func (c *Config) MsgvaultState() (state MsgvaultState, canonicalURL, apiKey string, err error) {
	if c == nil || c.Msgvault == nil {
		return MsgvaultAbsent, "", "", nil
	}
	mv := c.Msgvault
	rawURL := strings.TrimSpace(mv.URL)
	apiKeyEnv := strings.TrimSpace(mv.APIKeyEnv)
	if rawURL == "" {
		return MsgvaultMisconfigured, "", "", errors.New("[msgvault]: url is required")
	}
	u, perr := url.ParseRequestURI(rawURL)
	if perr != nil {
		return MsgvaultMisconfigured, "", "", fmt.Errorf("[msgvault]: invalid url: %w", perr)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return MsgvaultMisconfigured, "", "", fmt.Errorf("[msgvault]: url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return MsgvaultMisconfigured, "", "", errors.New("[msgvault]: url is missing host")
	}
	if u.Scheme == "http" && !IsLoopbackHostname(u.Hostname()) {
		return MsgvaultMisconfigured, "", "", fmt.Errorf(
			"[msgvault]: http url %q would send the API key in cleartext; use https, or http only with a loopback host (localhost/127.0.0.1)", rawURL)
	}
	if u.User != nil {
		return MsgvaultMisconfigured, "", "", errors.New("[msgvault]: url must not include userinfo")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return MsgvaultMisconfigured, "", "", errors.New("[msgvault]: url must not include query string")
	}
	if u.Fragment != "" {
		return MsgvaultMisconfigured, "", "", errors.New("[msgvault]: url must not include fragment")
	}
	canonicalURL = strings.TrimRight(u.String(), "/")
	if apiKeyEnv == "" {
		return MsgvaultMisconfigured, canonicalURL, "", errors.New("[msgvault]: api_key_env is required")
	}
	apiKey, ok := os.LookupEnv(apiKeyEnv)
	if !ok || strings.TrimSpace(apiKey) == "" {
		return MsgvaultMisconfigured, canonicalURL, "", fmt.Errorf("[msgvault]: env var %s is not set", apiKeyEnv)
	}
	return MsgvaultOK, canonicalURL, apiKey, nil
}

func DefaultConfigPath() string {
	return filepath.Join(baseDir(), "config.toml")
}

func DefaultDataDir() string {
	return baseDir()
}

func baseDir() string {
	if d := os.Getenv("MIDDLEMAN_HOME"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".config", "middleman")
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// EnsureDefault creates a default config file at path if it does not exist.
// The file contains sensible defaults. Repos can be added later through the
// settings UI.
//
// Writes to a temp file first, then hard-links into place so the target
// path is never left empty or partially written.
func EnsureDefault(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return nil
		}
		return fmt.Errorf("creating temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	const defaultConfig = `# middleman configuration
# See https://github.com/wesm/middleman for documentation.

sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
default_platform_host = "github.com"
host = "127.0.0.1"
port = 8091

# Add additional provider hosts when needed.
# [[platforms]]
# type = "gitlab"
# host = "gitlab.com"
# token_env = "MIDDLEMAN_GITLAB_TOKEN"

# Add repositories to monitor (or add them in the Settings UI).
# [[repos]]
# owner = "your-org"
# name = "your-repo"

[activity]
view_mode = "threaded"
time_range = "7d"
collapse_threads = true
default_branch_retention_days = 90
default_branch_max_commits = 5000

[terminal]
renderer = "xterm"

[modes]
activity = true
repos = true
kata = false
docs = false
messages = false
pulls = true
issues = true
board = true
reviews = true
workspaces = true

[tmux]
agent_sessions = true
`
	if _, err := tmp.WriteString(defaultConfig); err != nil {
		tmp.Close()
		return fmt.Errorf("writing default config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("flushing default config: %w", err)
	}

	// Link fails atomically when path already exists, providing
	// both atomic install and race-free existence check.
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		// Hard links may not be supported (FAT/exFAT, network
		// shares, cross-device). Fall back to O_EXCL create +
		// write with cleanup on failure.
		return writeExclusive(tmpPath, path)
	}
	return nil
}

// writeExclusive creates dst with O_EXCL (fails if it exists) and
// copies the content from src. Partial files are removed on failure.
func writeExclusive(src, dst string) error {
	content, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading temp config: %w", err)
	}

	f, err := os.OpenFile(
		dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600,
	)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("creating config %s: %w", dst, err)
	}

	if _, err := f.Write(content); err != nil {
		f.Close()
		os.Remove(dst)
		return fmt.Errorf("writing config %s: %w", dst, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(dst)
		return fmt.Errorf("flushing config %s: %w", dst, err)
	}
	return nil
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		SyncInterval:        defaultSyncInterval,
		GitHubTokenEnv:      defaultGitHubTokenEnv,
		DefaultPlatformHost: defaultPlatformHost,
		Host:                defaultHost,
		Port:                defaultPort,
		Activity: Activity{
			CollapseThreads: true,
		},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	meta, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if err := rejectDeprecatedConfigKeys(meta); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	if cfg.Repos == nil {
		cfg.Repos = []Repo{}
	}
	if cfg.Platforms == nil {
		cfg.Platforms = []PlatformConfig{}
	}
	if cfg.Agents == nil {
		cfg.Agents = []Agent{}
	}
	cfg.Modes = cfg.Modes.WithDefaults()

	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}

	if cfg.Activity.ViewMode == "" {
		cfg.Activity.ViewMode = defaultViewMode
	}
	if cfg.Activity.TimeRange == "" {
		cfg.Activity.TimeRange = defaultTimeRange
	}
	if cfg.Activity.DefaultBranchRetentionDays == 0 {
		cfg.Activity.DefaultBranchRetentionDays = defaultBranchActivityRetentionDays
	}
	if cfg.Activity.DefaultBranchMaxCommits == 0 {
		cfg.Activity.DefaultBranchMaxCommits = defaultBranchActivityMaxCommits
	}

	if cfg.SyncBudgetPerHour == 0 {
		cfg.SyncBudgetPerHour = defaultSyncBudgetPerHour
	}

	if strings.TrimSpace(cfg.IssueWorkspaceBranchStyle) == "" {
		cfg.IssueWorkspaceBranchStyle = defaultIssueWorkspaceBranchStyle
	}

	if cfg.SSEBufferSize == 0 {
		cfg.SSEBufferSize = defaultSSEBufferSize
	}

	if cfg.BasePath == "" {
		cfg.BasePath = defaultBasePath
	} else {
		bp := "/" + strings.Trim(cfg.BasePath, "/")
		if bp != "/" {
			bp += "/"
		}
		cfg.BasePath = bp
	}

	cfg.normalizeTokenFilePaths(filepath.Dir(path))
	return cfg, cfg.Validate()
}

func rejectDeprecatedConfigKeys(meta toml.MetaData) error {
	for _, key := range meta.Undecoded() {
		if len(key) == 2 && key[0] == "msgvault" && key[1] == "api_key" {
			return errors.New("[msgvault]: api_key is not supported; use api_key_env")
		}
		if len(key) >= 1 && (key[0] == "notebooks" || key[0] == "vaults") {
			return fmt.Errorf("[[%s]] is not supported; use [[doc_folders]]", key[0])
		}
	}
	return nil
}

func (c *Config) Validate() error {
	var err error
	c.DefaultPlatformHost, err = normalizePlatformHost(
		defaultPlatform, c.DefaultPlatformHost,
	)
	if err != nil {
		return fmt.Errorf("config: default_platform_host: %w", err)
	}
	if c.DefaultPlatformHost == defaultPlatformHost {
		c.DefaultPlatformHost = defaultPlatformHost
	}

	for i := range c.Platforms {
		p := &c.Platforms[i]
		p.Type, err = normalizePlatform(p.Type)
		if err != nil {
			return fmt.Errorf("config: platforms[%d]: %w", i, err)
		}
		p.Host, err = normalizePlatformHost(p.Type, p.Host)
		if err != nil {
			return fmt.Errorf("config: platforms[%d]: %w", i, err)
		}
		p.TokenEnv = strings.TrimSpace(p.TokenEnv)
		p.TokenFile = strings.TrimSpace(p.TokenFile)
	}
	if err := c.validatePlatforms(); err != nil {
		return err
	}
	if err := c.canonicalizeDocFolders(); err != nil {
		return err
	}
	c.Modes = c.Modes.WithDefaults()

	for i := range c.Repos {
		if c.Repos[i].ownerHasGlob() {
			return fmt.Errorf(
				"config: repos[%d]: glob syntax in owner is not supported", i,
			)
		}
		if err := c.Repos[i].normalize(c.DefaultPlatformHost); err != nil {
			return fmt.Errorf("config: repos[%d]: %w", i, err)
		}
		c.Repos[i].TokenEnv = strings.TrimSpace(c.Repos[i].TokenEnv)
		c.Repos[i].TokenFile = strings.TrimSpace(c.Repos[i].TokenFile)
	}

	// Reject duplicate repository identities.
	seen := make(map[string]string, len(c.Repos))
	for _, r := range c.Repos {
		key := repoIdentityKey(r)
		display := repoIdentityDisplay(r)
		if prev, ok := seen[key]; ok {
			return fmt.Errorf(
				"config: duplicate repo %q", prev,
			)
		}
		seen[key] = display
	}

	// Reject conflicting token source descriptors for the same host.
	// Empty repo-level fields mean "use platform/default token sources".
	hostToken := make(map[string]tokenauth.Descriptor, len(c.Repos))
	for _, r := range c.Repos {
		key := r.PlatformOrDefault() + "\x00" + r.PlatformHostOrDefault()
		effective := c.ResolveRepoTokenSource(r)
		if prev, ok := hostToken[key]; ok {
			if !prev.EqualSource(effective) {
				return fmt.Errorf(
					"config: conflicting token source for %s host %q (conflicting token_env): %s vs %s",
					r.PlatformOrDefault(), r.PlatformHostOrDefault(), prev.SafeString(), effective.SafeString(),
				)
			}
		} else {
			hostToken[key] = effective
		}
	}

	if _, err := time.ParseDuration(c.SyncInterval); err != nil {
		return fmt.Errorf("config: invalid sync_interval %q: %w", c.SyncInterval, err)
	}

	if ip := net.ParseIP(c.Host); ip == nil {
		return fmt.Errorf("config: invalid host %q", c.Host)
	} else if !ip.IsLoopback() {
		return fmt.Errorf("config: host %q is not loopback; only loopback addresses are supported", c.Host)
	}

	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("config: invalid port %d", c.Port)
	}

	bindKey, err := ParseHostKey(net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		return fmt.Errorf("config: invalid host %q: %w", c.Host, err)
	}
	c.parsedBindKey = bindKey

	c.parsedAllowedHosts = c.parsedAllowedHosts[:0]
	for _, entry := range c.AllowedHosts {
		key, err := ParseHostKey(entry)
		if err != nil {
			return fmt.Errorf("config: invalid allowed_hosts entry %q: %w", entry, err)
		}
		c.parsedAllowedHosts = append(c.parsedAllowedHosts, key)
	}

	if c.SyncBudgetPerHour != 0 && c.SyncBudgetPerHour < 50 {
		return fmt.Errorf(
			"config: sync_budget_per_hour must be >= 50 or omitted, got %d",
			c.SyncBudgetPerHour,
		)
	}

	if c.SSEBufferSize != 0 &&
		(c.SSEBufferSize < minSSEBufferSize || c.SSEBufferSize > maxSSEBufferSize) {
		return fmt.Errorf(
			"config: sse_buffer_size must be between %d and %d or omitted, got %d",
			minSSEBufferSize, maxSSEBufferSize, c.SSEBufferSize,
		)
	}

	if !validBasePathRe.MatchString(c.BasePath) {
		return fmt.Errorf("config: invalid base_path %q: must be / or /path/ using only alphanumerics, hyphens, underscores, dots, and tildes", c.BasePath)
	}
	for seg := range strings.SplitSeq(strings.Trim(c.BasePath, "/"), "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("config: invalid base_path %q: dot segments are not allowed", c.BasePath)
		}
	}

	validViewModes := map[string]bool{
		"flat": true, "threaded": true,
	}
	if !validViewModes[c.Activity.ViewMode] {
		return fmt.Errorf(
			"config: invalid activity view_mode %q",
			c.Activity.ViewMode,
		)
	}
	validTimeRanges := map[string]bool{
		"24h": true, "7d": true, "30d": true, "90d": true,
	}
	if !validTimeRanges[c.Activity.TimeRange] {
		return fmt.Errorf(
			"config: invalid activity time_range %q",
			c.Activity.TimeRange,
		)
	}
	if c.Activity.DefaultBranchRetentionDays == 0 {
		c.Activity.DefaultBranchRetentionDays = defaultBranchActivityRetentionDays
	}
	if c.Activity.DefaultBranchRetentionDays < 0 {
		return fmt.Errorf(
			"config: activity.default_branch_retention_days must be positive or omitted, got %d",
			c.Activity.DefaultBranchRetentionDays,
		)
	}
	if c.Activity.DefaultBranchMaxCommits == 0 {
		c.Activity.DefaultBranchMaxCommits = defaultBranchActivityMaxCommits
	}
	if c.Activity.DefaultBranchMaxCommits < 0 {
		return fmt.Errorf(
			"config: activity.default_branch_max_commits must be positive or omitted, got %d",
			c.Activity.DefaultBranchMaxCommits,
		)
	}

	c.IssueWorkspaceBranchStyle = strings.TrimSpace(c.IssueWorkspaceBranchStyle)
	if c.IssueWorkspaceBranchStyle == "" {
		c.IssueWorkspaceBranchStyle = defaultIssueWorkspaceBranchStyle
	}
	switch c.IssueWorkspaceBranchStyle {
	case IssueWorkspaceBranchStyleSlug, IssueWorkspaceBranchStyleBare:
	default:
		return fmt.Errorf(
			"config: invalid issue_workspace_branch_style %q: must be %q or %q",
			c.IssueWorkspaceBranchStyle,
			IssueWorkspaceBranchStyleSlug,
			IssueWorkspaceBranchStyleBare,
		)
	}

	c.Terminal.FontFamily = strings.TrimSpace(c.Terminal.FontFamily)
	if c.Terminal.FontSize == 0 {
		c.Terminal.FontSize = DefaultTerminalFontSize
	}
	if c.Terminal.FontSize < 8 || c.Terminal.FontSize > 32 {
		return fmt.Errorf(
			"config: invalid terminal.font_size %d: must be between 8 and 32",
			c.Terminal.FontSize,
		)
	}
	if c.Terminal.Scrollback == 0 {
		c.Terminal.Scrollback = DefaultTerminalScrollback
	}
	if c.Terminal.Scrollback < 100 || c.Terminal.Scrollback > 100000 {
		return fmt.Errorf(
			"config: invalid terminal.scrollback %d: must be between 100 and 100000",
			c.Terminal.Scrollback,
		)
	}
	if c.Terminal.LineHeight == 0 {
		c.Terminal.LineHeight = DefaultTerminalLineHeight
	}
	if c.Terminal.LineHeight < 0.8 || c.Terminal.LineHeight > 2 {
		return fmt.Errorf(
			"config: invalid terminal.line_height %.2f: must be between 0.8 and 2",
			c.Terminal.LineHeight,
		)
	}
	if c.Terminal.LetterSpacing < -2 || c.Terminal.LetterSpacing > 8 {
		return fmt.Errorf(
			"config: invalid terminal.letter_spacing %d: must be between -2 and 8",
			c.Terminal.LetterSpacing,
		)
	}
	if c.Terminal.CursorBlink == nil {
		cursorBlink := DefaultTerminalCursorBlink
		c.Terminal.CursorBlink = &cursorBlink
	}
	c.Terminal.Renderer = strings.TrimSpace(c.Terminal.Renderer)
	if c.Terminal.Renderer == "" {
		c.Terminal.Renderer = TerminalRendererXterm
	}
	if c.Terminal.Renderer != TerminalRendererXterm &&
		c.Terminal.Renderer != TerminalRendererGhostty {
		return fmt.Errorf(
			"config: invalid terminal.renderer %q: must be %q or %q",
			c.Terminal.Renderer,
			TerminalRendererXterm,
			TerminalRendererGhostty,
		)
	}

	if err := c.validateAgents(); err != nil {
		return err
	}

	if len(c.Tmux.Command) > 0 &&
		strings.TrimSpace(c.Tmux.Command[0]) == "" {
		return fmt.Errorf(
			"config: invalid tmux.command: first element must be non-empty",
		)
	}

	if len(c.Shell.Command) > 0 &&
		strings.TrimSpace(c.Shell.Command[0]) == "" {
		return fmt.Errorf(
			"config: invalid shell.command: first element must be non-empty",
		)
	}

	return nil
}

// docFolderIDPattern constrains a docs folder id to characters that
// survive as a single URL path segment, matching docs.ValidateFolderID.
// Duplicated here because internal/docs imports this package.
var docFolderIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (c *Config) canonicalizeDocFolders() error {
	seen := make(map[string]struct{}, len(c.DocFolders))
	for i := range c.DocFolders {
		folder := &c.DocFolders[i]
		folder.ID = strings.TrimSpace(folder.ID)
		if folder.ID == "" {
			return fmt.Errorf("config: doc_folders[%d]: id is required", i)
		}
		if folder.ID == "." || folder.ID == ".." || !docFolderIDPattern.MatchString(folder.ID) {
			return fmt.Errorf("config: doc_folders[%q]: id may contain only letters, digits, '.', '_' or '-'", folder.ID)
		}
		if _, dup := seen[folder.ID]; dup {
			return fmt.Errorf("config: doc_folders: duplicate id %q", folder.ID)
		}
		seen[folder.ID] = struct{}{}

		folder.Path = strings.TrimSpace(folder.Path)
		if folder.Path == "" {
			return fmt.Errorf("config: doc_folders[%q]: path is required", folder.ID)
		}
		expanded, err := expandTilde(folder.Path)
		if err != nil {
			return fmt.Errorf("config: doc_folders[%q]: %w", folder.ID, err)
		}
		resolved, err := filepath.Abs(expanded)
		if err != nil {
			return fmt.Errorf("config: doc_folders[%q]: resolve path: %w", folder.ID, err)
		}
		folder.Path = resolved

		folder.Name = strings.TrimSpace(folder.Name)
		if folder.Name == "" {
			folder.Name = filepath.Base(resolved)
		}
		folder.Daemon = strings.TrimSpace(folder.Daemon)
	}
	return nil
}

func expandTilde(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home := homeDir()
		if home == "" {
			return "", errors.New("home directory is not set")
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func (c *Config) validatePlatforms() error {
	seen := make(map[string]tokenauth.Descriptor, len(c.Platforms))
	for _, p := range c.Platforms {
		key := p.Type + "\x00" + p.Host
		display := p.Type + "/" + p.Host
		desc := tokenauth.Descriptor{Key: tokenauth.Key{Platform: p.Type, Host: p.Host}}
		if p.TokenFile != "" {
			desc.Candidates = append(desc.Candidates, tokenauth.Candidate{
				Kind:     tokenauth.SourceKindFile,
				FilePath: p.TokenFile,
			})
		}
		if p.TokenEnv != "" {
			desc.Candidates = append(desc.Candidates, tokenauth.Candidate{
				Kind:    tokenauth.SourceKindEnv,
				EnvName: p.TokenEnv,
			})
		}
		if prev, ok := seen[key]; ok {
			if prev.EqualSource(desc) {
				return fmt.Errorf("config: duplicate platform %q", display)
			}
			return fmt.Errorf(
				"config: conflicting token source for platform %q (conflicting token_env): %s vs %s",
				display, prev.SafeString(), desc.SafeString(),
			)
		}
		seen[key] = desc
	}
	return nil
}

func (c *Config) validateAgents() error {
	seen := make(map[string]struct{}, len(c.Agents))
	for i := range c.Agents {
		agent := &c.Agents[i]
		agent.Key = strings.ToLower(strings.TrimSpace(agent.Key))
		agent.Label = strings.TrimSpace(agent.Label)
		if agent.Key == "" {
			return fmt.Errorf("config: agents[%d]: key is required", i)
		}
		if agent.Label == "" {
			agent.Label = agent.Key
		}
		if reservedSystemLaunchTargetKeys[agent.Key] {
			return fmt.Errorf(
				"config: agents[%d]: key %q is a reserved system launch target",
				i, agent.Key,
			)
		}
		if _, ok := seen[agent.Key]; ok {
			return fmt.Errorf(
				"config: duplicate agent %q", agent.Key,
			)
		}
		seen[agent.Key] = struct{}{}

		if !agent.EnabledOrDefault() {
			continue
		}
		if len(agent.Command) == 0 {
			return fmt.Errorf(
				"config: agents[%d]: command is required when enabled", i,
			)
		}
		if strings.TrimSpace(agent.Command[0]) == "" {
			return fmt.Errorf(
				"config: agents[%d]: command first element must be non-empty", i,
			)
		}
	}
	return nil
}

func repoIdentityKey(r Repo) string {
	return strings.Join([]string{
		r.PlatformOrDefault(),
		r.PlatformHostOrDefault(),
		strings.ToLower(repoPathOrFullName(r)),
	}, "\x00")
}

func repoIdentityDisplay(r Repo) string {
	platform := r.PlatformOrDefault()
	host := r.PlatformHostOrDefault()
	if platform == defaultPlatform && host == defaultPlatformHost {
		return repoPathOrFullName(r)
	}
	return platform + "/" + host + "/" + repoPathOrFullName(r)
}

func repoPathOrFullName(r Repo) string {
	if strings.TrimSpace(r.RepoPath) != "" {
		return strings.TrimSpace(r.RepoPath)
	}
	return r.Owner + "/" + r.Name
}

var reservedSystemLaunchTargetKeys = map[string]bool{
	"tmux":        true,
	"plain_shell": true,
	"shell":       true,
}

var (
	validBasePathRe = regexp.MustCompile(`^/([a-zA-Z0-9._~-]+/)*$`)
	// Without scheme: require / so bare "github.com" (a valid repo
	// name) is not falsely matched.
	bareHostRepoRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9.-]*(?:\.[A-Za-z0-9.-]+|:[0-9]+))/(.*)$`)
	// SCP-style only (git@host:path); ssh:// URIs use net/url.
	scpRepoRe = regexp.MustCompile(`^[^@]+@([^:]+):(.*)$`)
)

func (c *Config) SyncDuration() time.Duration {
	d, _ := time.ParseDuration(c.SyncInterval)
	return d
}

func (c *Config) BranchActivityRetention() time.Duration {
	if c == nil || c.Activity.DefaultBranchRetentionDays <= 0 {
		return time.Duration(defaultBranchActivityRetentionDays) * 24 * time.Hour
	}
	return time.Duration(c.Activity.DefaultBranchRetentionDays) * 24 * time.Hour
}

func (c *Config) GitHubToken() string {
	return c.gitHubTokenForHost(platformpkg.DefaultGitHubHost)
}

// gitHubTokenForHost resolves a github token for a specific host. The
// configured GitHubTokenEnv env var wins when non-empty, but only for
// github.com — it holds the public-GitHub token and must not leak to
// Enterprise/self-hosted hosts. Every host falls back to the
// host-scoped `gh auth token --hostname <host>`. Internal callers go
// through GitHubToken() or TokenForPlatformHost.
func (c *Config) gitHubTokenForHost(host string) string {
	if host == platformpkg.DefaultGitHubHost {
		if token := os.Getenv(c.GitHubTokenEnv); token != "" {
			return token
		}
	}
	return ghAuthTokenForHost(host)
}

func (c *Config) TokenForPlatformHost(platform, host, repoTokenEnv string) string {
	if c == nil {
		return ""
	}
	if repoTokenEnv != "" {
		if token := os.Getenv(repoTokenEnv); token != "" {
			return token
		}
	}
	p, err := normalizePlatform(platform)
	if err != nil {
		return ""
	}
	h, err := normalizePlatformHost(p, host)
	if err != nil {
		return ""
	}
	for _, pc := range c.Platforms {
		if pc.Type == p && pc.Host == h && pc.TokenEnv != "" {
			return os.Getenv(pc.TokenEnv)
		}
	}
	if defaultTokenEnv, ok := defaultTokenEnvForPlatformHost(p, h); ok {
		return os.Getenv(defaultTokenEnv)
	}
	if p == defaultPlatform {
		return c.gitHubTokenForHost(h)
	}
	return ""
}

func (c *Config) ResolveRepoToken(r Repo) string {
	if c == nil {
		return r.ResolveToken("")
	}
	return c.TokenForPlatformHost(
		r.PlatformOrDefault(), r.PlatformHostOrDefault(), r.TokenEnv,
	)
}

func (c *Config) ResolveRepoTokenSource(r Repo) tokenauth.Descriptor {
	if c == nil {
		return tokenauth.Descriptor{}
	}
	return c.TokenSourceForPlatformHost(
		r.PlatformOrDefault(), r.PlatformHostOrDefault(), r.TokenEnv, r.TokenFile,
	)
}

type ProviderTokenSource struct {
	Descriptor tokenauth.Descriptor
	Required   bool
}

func (c *Config) ProviderTokenSources() []ProviderTokenSource {
	if c == nil {
		return nil
	}
	seen := make(map[tokenauth.Key]struct{}, len(c.Repos)+len(c.Platforms)+1)
	out := make([]ProviderTokenSource, 0, len(c.Repos)+len(c.Platforms)+1)
	add := func(desc tokenauth.Descriptor, required bool) {
		if desc.Key.Host == "" {
			return
		}
		if _, ok := seen[desc.Key]; ok {
			return
		}
		// Optional hosts stay in the list even with no candidates: config
		// reload updates live sources from these plans, so dropping a host
		// whose token config was removed would leave its old credential
		// active until restart. Consumers that need a usable credential
		// resolve the source and skip optional misses.
		seen[desc.Key] = struct{}{}
		out = append(out, ProviderTokenSource{
			Descriptor: desc,
			Required:   required,
		})
	}
	for _, repo := range c.Repos {
		add(c.ResolveRepoTokenSource(repo), true)
	}
	for _, p := range c.Platforms {
		add(c.TokenSourceForPlatformHost(p.Type, p.Host, "", ""), false)
	}
	add(c.TokenSourceForPlatformHost(
		defaultPlatform, platformpkg.DefaultGitHubHost, "", "",
	), false)
	return out
}

// CloneTokenDescriptors returns one descriptor per platform host carrying the
// host's effective git clone/fetch credential chain under
// tokenauth.CloneKey(host). Git transport auth is host-scoped: every provider
// sharing a host must use a canonically identical chain (enforced at startup
// and by reload validation), so the host chain is the first non-empty plan
// chain in ProviderTokenSources order. Hosts whose plans are all
// credential-less keep an empty chain so a reload clears a previously tokened
// live clone source instead of leaving the removed credential active.
func (c *Config) CloneTokenDescriptors() []tokenauth.Descriptor {
	plans := c.ProviderTokenSources()
	indexByHost := make(map[string]int, len(plans))
	out := make([]tokenauth.Descriptor, 0, len(plans))
	for _, plan := range plans {
		host := plan.Descriptor.Key.Host
		idx, ok := indexByHost[host]
		if !ok {
			indexByHost[host] = len(out)
			out = append(out, tokenauth.Descriptor{
				Key:        tokenauth.CloneKey(host),
				Candidates: plan.Descriptor.Candidates,
			})
			continue
		}
		if len(out[idx].Candidates) == 0 {
			out[idx].Candidates = plan.Descriptor.Candidates
		}
	}
	return out
}

func (c *Config) TokenSourceForPlatformHost(
	platform, host, repoTokenEnv, repoTokenFile string,
) tokenauth.Descriptor {
	if c == nil {
		return tokenauth.Descriptor{}
	}
	p, err := normalizePlatform(platform)
	if err != nil {
		return tokenauth.Descriptor{}
	}
	h, err := normalizePlatformHost(p, host)
	if err != nil {
		return tokenauth.Descriptor{}
	}
	desc := tokenauth.Descriptor{Key: tokenauth.Key{Platform: p, Host: h}}
	if repoTokenFile != "" {
		desc.Candidates = append(desc.Candidates, tokenauth.Candidate{
			Kind:     tokenauth.SourceKindFile,
			FilePath: repoTokenFile,
		})
	}
	if repoTokenEnv != "" {
		desc.Candidates = append(desc.Candidates, tokenauth.Candidate{
			Kind:    tokenauth.SourceKindEnv,
			EnvName: repoTokenEnv,
		})
	}
	for _, pc := range c.Platforms {
		if pc.Type == p && pc.Host == h {
			if pc.TokenFile != "" {
				desc.Candidates = append(desc.Candidates, tokenauth.Candidate{
					Kind:     tokenauth.SourceKindFile,
					FilePath: pc.TokenFile,
				})
			}
			if pc.TokenEnv != "" {
				desc.Candidates = append(desc.Candidates, tokenauth.Candidate{
					Kind:    tokenauth.SourceKindEnv,
					EnvName: pc.TokenEnv,
				})
			}
			break
		}
	}
	if defaultTokenEnv, ok := defaultTokenEnvForPlatformHost(p, h); ok {
		desc.Candidates = append(desc.Candidates, tokenauth.Candidate{
			Kind:    tokenauth.SourceKindEnv,
			EnvName: defaultTokenEnv,
		})
	}
	if p == defaultPlatform {
		// github_token_env is a github.com-only default, mirroring the
		// other public-host defaults. Appending it for Enterprise or
		// self-hosted GitHub hosts would send the public-GitHub token to
		// whatever host the config names; those hosts must configure
		// repo/platform token_env or token_file, or rely on gh's
		// host-scoped credential below.
		if c.GitHubTokenEnv != "" && h == platformpkg.DefaultGitHubHost {
			desc.Candidates = append(desc.Candidates, tokenauth.Candidate{
				Kind:    tokenauth.SourceKindEnv,
				EnvName: c.GitHubTokenEnv,
			})
		}
		desc.Candidates = append(desc.Candidates, tokenauth.Candidate{
			Kind: tokenauth.SourceKindGitHubCLI,
			Host: h,
		})
	}
	return desc
}

func defaultTokenEnvForPlatformHost(platform, host string) (string, bool) {
	switch platform {
	case string(platformpkg.KindForgejo):
		return defaultForgejoTokenEnv, host == platformpkg.DefaultForgejoHost
	case string(platformpkg.KindGitea):
		return defaultGiteaTokenEnv, host == platformpkg.DefaultGiteaHost
	default:
		return "", false
	}
}

// TokenEnvNames returns every env var name that may hold a provider
// token according to this config. Used by the runtime sanitizer to
// strip tokens from launched session environments.
func (c *Config) TokenEnvNames() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, 1+len(c.Repos)+len(c.Platforms))
	if c.GitHubTokenEnv != "" {
		names = appendTokenEnvName(names, c.GitHubTokenEnv)
	}
	for _, p := range c.Platforms {
		names = appendTokenEnvNamesFromDescriptor(
			names,
			c.TokenSourceForPlatformHost(p.Type, p.Host, "", ""),
		)
	}
	for _, r := range c.Repos {
		names = appendTokenEnvNamesFromDescriptor(
			names,
			c.TokenSourceForPlatformHost(
				r.PlatformOrDefault(),
				r.PlatformHostOrDefault(),
				"",
				"",
			),
		)
	}
	for _, r := range c.Repos {
		names = appendTokenEnvNamesFromDescriptor(names, c.ResolveRepoTokenSource(r))
	}
	if c.Msgvault != nil && c.Msgvault.APIKeyEnv != "" {
		names = appendTokenEnvName(names, strings.TrimSpace(c.Msgvault.APIKeyEnv))
	}
	return names
}

func appendTokenEnvNamesFromDescriptor(
	names []string,
	desc tokenauth.Descriptor,
) []string {
	for _, candidate := range desc.Candidates {
		if candidate.Kind == tokenauth.SourceKindEnv {
			names = appendTokenEnvName(names, candidate.EnvName)
		}
	}
	return names
}

func (c *Config) normalizeTokenFilePaths(configDir string) {
	for i := range c.Platforms {
		c.Platforms[i].TokenFile = normalizeTokenFilePath(configDir, c.Platforms[i].TokenFile)
	}
	for i := range c.Repos {
		c.Repos[i].TokenFile = normalizeTokenFilePath(configDir, c.Repos[i].TokenFile)
	}
}

func normalizeTokenFilePath(configDir, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if raw == "~" {
		return homeDir()
	}
	if suffix, ok := strings.CutPrefix(raw, "~/"); ok {
		return filepath.Join(homeDir(), suffix)
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	return filepath.Clean(filepath.Join(configDir, raw))
}

func appendTokenEnvName(names []string, name string) []string {
	if name == "" || slices.Contains(names, name) {
		return names
	}
	return append(names, name)
}

var execCommand = procutil.CommandContext

// ghAuthExecTimeout bounds each gh subprocess invocation. gh auth
// token is a local lookup and returns in milliseconds; 5s is generous
// and prevents a hung gh from stalling startup. A var rather than a
// const only so tests driving fake gh scripts can relax it: under a
// fully loaded parallel suite run, spawning the fake can exceed 5s and
// the kill then masquerades as gh behavior.
var ghAuthExecTimeout = 5 * time.Second

// ghAuthTokenForHost returns the token gh has stored for host, or "".
// Older gh versions that do not recognize --hostname trigger a fallback
// to bare `gh auth token` only when host is the default github.com.
// Any other host returns empty without retry so the caller surfaces a
// missing-token error rather than the wrong host's token.
func ghAuthTokenForHost(host string) string {
	ctx, cancel := context.WithTimeout(context.Background(), ghAuthExecTimeout)
	defer cancel()
	token, _ := GitHubCLITokenForHost(ctx, host)
	return token
}

func GitHubCLITokenForHost(ctx context.Context, host string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ghAuthExecTimeout)
		defer cancel()
	}
	out, stderr, err := runGHAuthToken(ctx, "--hostname", host)
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	if host == platformpkg.DefaultGitHubHost &&
		isUnsupportedHostnameFlag(err, stderr) {
		out, _, err = runGHAuthToken(ctx)
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "", nil
}

// runGHAuthToken invokes `gh auth token` with the given extra args
// under ctx. stderr is captured explicitly so the caller can inspect
// the rejection text from older gh versions (cmd.Output() only fills
// *ExitError.Stderr when cmd.Stderr is unset).
func runGHAuthToken(ctx context.Context, extraArgs ...string) ([]byte, []byte, error) {
	args := append([]string{"auth", "token"}, extraArgs...)
	cmd := execCommand(ctx, "gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return out, stderr.Bytes(), err
}

// isUnsupportedHostnameFlag reports whether the gh invocation failed
// specifically because the installed gh does not recognize the
// --hostname flag (cobra/pflag rejection text). Missing-binary,
// context-deadline, auth-failure, and unrelated nonzero exits all
// return false so the caller does not retry bare.
func isUnsupportedHostnameFlag(err error, stderr []byte) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	text := string(stderr)
	return strings.Contains(text, "unknown flag: --hostname") ||
		strings.Contains(text, "unknown shorthand flag")
}

func (c *Config) BudgetPerHour() int {
	return c.SyncBudgetPerHour
}

func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// BindHostKey returns the canonical (Host, Port) key for the bind
// address, populated by Validate. The zero HostKey is returned for
// configs that were not validated (e.g. test literals that omit
// Host/Port); callers should use HostKey.Valid() to gate behavior.
func (c *Config) BindHostKey() HostKey {
	return c.parsedBindKey
}

// ParsedAllowedHosts returns the canonicalised allowlist, populated
// by Validate. The returned slice is a defensive copy.
func (c *Config) ParsedAllowedHosts() []HostKey {
	return append([]HostKey(nil), c.parsedAllowedHosts...)
}

func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "middleman.db")
}

// RoborevEndpoint returns the configured roborev daemon endpoint,
// falling back to the default localhost address.
func (c *Config) RoborevEndpoint() string {
	if c.Roborev.Endpoint != "" {
		return c.Roborev.Endpoint
	}
	return "http://127.0.0.1:7373"
}

// TmuxCommand returns the command + argv prefix used to invoke
// tmux. Defaults to ["tmux"] when c is nil or the setting is
// unconfigured. The returned slice is a copy, safe to append to.
func (c *Config) TmuxCommand() []string {
	if c == nil || len(c.Tmux.Command) == 0 {
		return []string{"tmux"}
	}
	return slices.Clone(c.Tmux.Command)
}

// ShellCommand returns the configured shell command + argv prefix
// used when ensuring a workspace's plain shell session, or nil when
// unset. nil means the runtime falls back to the user's $SHELL (or
// /bin/sh). The returned slice is a copy, safe to append to.
func (c *Config) ShellCommand() []string {
	if c == nil || len(c.Shell.Command) == 0 {
		return nil
	}
	return slices.Clone(c.Shell.Command)
}

// TmuxAgentSessionsEnabled reports whether runtime agent launches
// should prefer tmux-backed sessions. Defaults to true so agent
// activity is visible to tmux-based workspace fingerprinting.
func (c *Config) TmuxAgentSessionsEnabled() bool {
	return c == nil ||
		c.Tmux.AgentSessions == nil ||
		*c.Tmux.AgentSessions
}

// IssueWorkspaceBranchSlugEnabled reports whether new issue
// workspaces should derive a title slug onto their branch name.
// Defaults to true (the "slug" style); returns false for "bare".
func (c *Config) IssueWorkspaceBranchSlugEnabled() bool {
	if c == nil {
		return true
	}
	switch strings.TrimSpace(c.IssueWorkspaceBranchStyle) {
	case "", IssueWorkspaceBranchStyleSlug:
		return true
	case IssueWorkspaceBranchStyleBare:
		return false
	default:
		return true
	}
}

func reposForSave(repos []Repo) []Repo {
	if repos == nil {
		return nil
	}
	out := make([]Repo, len(repos))
	copy(out, repos)
	for i := range out {
		if out[i].Platform == defaultPlatform {
			out[i].Platform = ""
		}
		if out[i].PlatformOrDefault() == defaultPlatform &&
			out[i].PlatformHost == defaultPlatformHost {
			out[i].PlatformHost = ""
		}
	}
	return out
}

// configFile is the subset of Config written to disk.
type configFile struct {
	SyncInterval              string           `toml:"sync_interval"`
	GitHubTokenEnv            string           `toml:"github_token_env"`
	DefaultPlatformHost       string           `toml:"default_platform_host,omitempty"`
	Host                      string           `toml:"host"`
	Port                      int              `toml:"port"`
	SyncBudgetPerHour         int              `toml:"sync_budget_per_hour,omitempty"`
	SSEBufferSize             int              `toml:"sse_buffer_size,omitempty"`
	BasePath                  string           `toml:"base_path,omitempty"`
	DataDir                   string           `toml:"data_dir,omitempty"`
	IssueWorkspaceBranchStyle string           `toml:"issue_workspace_branch_style,omitempty"`
	AllowedHosts              []string         `toml:"allowed_hosts,omitempty"`
	TrustReverseProxy         bool             `toml:"trust_reverse_proxy,omitempty"`
	Repos                     []Repo           `toml:"repos"`
	Platforms                 []PlatformConfig `toml:"platforms,omitempty"`
	Activity                  Activity         `toml:"activity"`
	Terminal                  Terminal         `toml:"terminal,omitempty"`
	Modes                     ModeVisibility   `toml:"modes,omitempty"`
	Agents                    []Agent          `toml:"agents,omitempty"`
	DocFolders                []DocFolder      `toml:"doc_folders,omitempty"`
	Roborev                   Roborev          `toml:"roborev,omitempty"`
	Msgvault                  *Msgvault        `toml:"msgvault,omitempty"`
	Tmux                      Tmux             `toml:"tmux,omitempty"`
	Shell                     Shell            `toml:"shell,omitempty"`
}

// Save writes the current config to the given path.
func (c *Config) Save(path string) error {
	cfg := c.copyForSave()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validating config: %w", err)
	}
	f := configFile{
		SyncInterval:        cfg.SyncInterval,
		GitHubTokenEnv:      cfg.GitHubTokenEnv,
		DefaultPlatformHost: cfg.DefaultPlatformHost,
		Host:                cfg.Host,
		Port:                cfg.Port,
		AllowedHosts:        slices.Clone(cfg.AllowedHosts),
		TrustReverseProxy:   cfg.TrustReverseProxy,
		Repos:               reposForSave(cfg.Repos),
		Platforms:           cfg.Platforms,
		Activity:            cfg.Activity,
		Terminal:            cfg.Terminal,
		Modes:               cfg.Modes,
		Agents:              cfg.Agents,
		DocFolders:          cfg.DocFolders,
		Roborev:             cfg.Roborev,
		Msgvault:            cfg.Msgvault,
		Tmux:                cfg.Tmux,
		Shell:               cfg.Shell,
	}
	if cfg.DefaultPlatformHost == defaultPlatformHost {
		f.DefaultPlatformHost = ""
	}
	if cfg.SyncBudgetPerHour != defaultSyncBudgetPerHour {
		f.SyncBudgetPerHour = cfg.SyncBudgetPerHour
	}
	if cfg.SSEBufferSize != 0 && cfg.SSEBufferSize != defaultSSEBufferSize {
		f.SSEBufferSize = cfg.SSEBufferSize
	}
	if cfg.BasePath != defaultBasePath {
		f.BasePath = cfg.BasePath
	}
	if cfg.DataDir != DefaultDataDir() {
		f.DataDir = cfg.DataDir
	}
	if cfg.IssueWorkspaceBranchStyle != defaultIssueWorkspaceBranchStyle {
		f.IssueWorkspaceBranchStyle = cfg.IssueWorkspaceBranchStyle
	}

	savePath := path
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolving config symlink: %w", err)
		}
		savePath = resolved
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("checking config path: %w", err)
	}

	dir := filepath.Dir(savePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating config directory: %w", err)
		}
	}

	tmp, err := os.CreateTemp(dir, ".middleman-config-*.toml")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(f); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp config: %w", err)
	}
	if err := os.Rename(tmpPath, savePath); err != nil {
		return fmt.Errorf("renaming temp config: %w", err)
	}
	return nil
}

func (c *Config) copyForSave() Config {
	if c == nil {
		return Config{}
	}
	cfg := *c
	cfg.Repos = slices.Clone(c.Repos)
	cfg.Platforms = slices.Clone(c.Platforms)
	cfg.AllowedHosts = slices.Clone(c.AllowedHosts)
	cfg.DocFolders = slices.Clone(c.DocFolders)
	cfg.Agents = slices.Clone(c.Agents)
	if cfg.SyncInterval == "" {
		cfg.SyncInterval = defaultSyncInterval
	}
	if cfg.DefaultPlatformHost == "" {
		cfg.DefaultPlatformHost = defaultPlatformHost
	}
	if cfg.Host == "" {
		cfg.Host = defaultHost
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	cfg.Modes = cfg.Modes.WithDefaults()
	if cfg.Activity.ViewMode == "" {
		cfg.Activity.ViewMode = defaultViewMode
	}
	if cfg.Activity.TimeRange == "" {
		cfg.Activity.TimeRange = defaultTimeRange
	}
	if cfg.Activity.DefaultBranchRetentionDays == 0 {
		cfg.Activity.DefaultBranchRetentionDays = defaultBranchActivityRetentionDays
	}
	if cfg.Activity.DefaultBranchMaxCommits == 0 {
		cfg.Activity.DefaultBranchMaxCommits = defaultBranchActivityMaxCommits
	}
	if cfg.SyncBudgetPerHour == 0 {
		cfg.SyncBudgetPerHour = defaultSyncBudgetPerHour
	}
	if cfg.SSEBufferSize == 0 {
		cfg.SSEBufferSize = defaultSSEBufferSize
	}
	if cfg.BasePath == "" {
		cfg.BasePath = defaultBasePath
	}
	if strings.TrimSpace(cfg.IssueWorkspaceBranchStyle) == "" {
		cfg.IssueWorkspaceBranchStyle = defaultIssueWorkspaceBranchStyle
	}
	return cfg
}

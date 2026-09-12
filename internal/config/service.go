package config

import (
	"errors"
	"net/url"
	"slices"
	"strings"

	platformpkg "go.kenn.io/forge/platform"
)

// Service opts one deployment into GitHub App user authorization.
type Service struct {
	Enabled                bool   `toml:"enabled,omitempty" json:"enabled"`
	GitHubUserID           int64  `toml:"github_user_id,omitempty" json:"github_user_id"`
	GitHubClientID         string `toml:"github_client_id,omitempty" json:"github_client_id"`
	GitHubClientSecretFile string `toml:"github_client_secret_file,omitempty" json:"-"`
	BaseURL                string `toml:"base_url,omitempty" json:"base_url"`
}

func (c *Config) validateService() error {
	if !c.Service.Enabled {
		return nil
	}
	s := &c.Service
	s.GitHubClientID = strings.TrimSpace(s.GitHubClientID)
	s.GitHubClientSecretFile = strings.TrimSpace(s.GitHubClientSecretFile)
	if s.GitHubUserID <= 0 || s.GitHubClientID == "" || s.GitHubClientSecretFile == "" {
		return errors.New("config: service requires github_user_id, github_client_id, and github_client_secret_file")
	}
	u, err := url.Parse(s.BaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return errors.New("config: service.base_url must be an HTTPS URL without credentials, query, or fragment")
	}
	if strings.TrimRight(u.Path, "/") != strings.TrimRight(c.BasePath, "/") {
		return errors.New("config: service.base_url path must match base_path")
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	if c.API.TailscaleServe.Enabled || c.Fleet.Enabled {
		return errors.New("config: service mode uses GitHub login and does not support fleet or api.tailscale_serve identity authentication")
	}
	if len(c.GitHubApps) != 0 || len(c.GitHubOwnerTokens) != 0 || c.HasExplicitGitHubTokenEnv() {
		return errors.New("config: service mode cannot configure other GitHub credentials")
	}
	if c.DefaultPlatformHost != "" && c.DefaultPlatformHost != platformpkg.DefaultGitHubHost {
		return errors.New("config: service mode requires github.com")
	}
	for _, p := range c.Platforms {
		if p.Type != defaultPlatform || (p.Host != "" && p.Host != platformpkg.DefaultGitHubHost) || p.TokenEnv != "" || p.TokenFile != "" || p.BaseURL != "" || p.AllowInsecure {
			return errors.New("config: service mode platforms must use github.com without other credentials")
		}
	}
	for _, r := range c.Repos {
		if r.PlatformOrDefault() != defaultPlatform || r.PlatformHostOrDefault() != platformpkg.DefaultGitHubHost || r.TokenEnv != "" || r.TokenFile != "" {
			return errors.New("config: service mode repositories must use github.com without other credentials")
		}
	}
	c.API.RequireAuth = true
	if !slices.Contains(c.AllowedHosts, u.Host) {
		c.AllowedHosts = append(c.AllowedHosts, u.Host)
	}
	return nil
}

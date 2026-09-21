package config

import (
	"errors"
	"net/mail"
	"net/url"
	"path/filepath"
	"strings"
)

// ExecutionWorker is an explicit startup role. Its Git authority comes only
// from the local account-bound broker, never from ordinary provider settings.
type ExecutionWorker struct {
	Enabled      bool   `toml:"enabled,omitempty"`
	BrokerSocket string `toml:"broker_socket,omitempty"`
	UID          uint32 `toml:"uid,omitempty"`
	GitHubUserID int64  `toml:"github_user_id,omitempty"`
	CommitName   string `toml:"commit_name,omitempty"`
	CommitEmail  string `toml:"commit_email,omitempty"`
	WorktreeDir  string `toml:"worktree_dir,omitempty"`
}

type Devboxes struct {
	RegistryURL string `toml:"registry_url,omitempty"`
}

func (c *Config) validateDevboxes() error {
	if raw := c.Devboxes.RegistryURL; raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("config: devboxes.registry_url must be an HTTPS service URL")
		}
	}
	worker := c.ExecutionWorker
	if !worker.Enabled {
		return nil
	}
	dataDir, err := CanonicalDataDir(c.DataDir)
	if err != nil {
		return err
	}
	defaultDataDir, err := CanonicalDataDir(DefaultDataDir())
	if err != nil {
		return err
	}
	if dataDir == defaultDataDir || !filepath.IsAbs(c.DataDir) {
		return errors.New("config: execution worker requires its own absolute data_dir, separate from the default Forge directory")
	}
	if !c.API.RequireAuth || c.API.TailscaleServe.Enabled || !IsLoopbackHostname(c.Host) ||
		c.Fleet.Enabled || c.Fleet.RoleOrDefault() == FleetRoleSpoke || c.MCP.Enabled || c.BasePath != "/" {
		return errors.New("config: execution worker requires loopback, bearer auth, base_path=/, and no fleet, MCP, or Tailscale header authentication")
	}
	if worker.UID == 0 || worker.GitHubUserID <= 0 || !filepath.IsAbs(worker.BrokerSocket) {
		return errors.New("config: execution worker requires a non-root UID, GitHub user ID, and absolute broker socket")
	}
	if worker.WorktreeDir != "" && !filepath.IsAbs(worker.WorktreeDir) {
		return errors.New("config: execution worker worktree_dir must be an absolute path")
	}
	if strings.TrimSpace(worker.CommitName) == "" || strings.ContainsAny(worker.CommitName, "\r\n<>") {
		return errors.New("config: execution worker requires a valid commit name")
	}
	address, err := mail.ParseAddress(worker.CommitEmail)
	if err != nil || address.Address != worker.CommitEmail || address.Name != "" {
		return errors.New("config: execution worker requires a commit email address")
	}
	if len(c.Platforms) != 0 || len(c.GitHubApps) != 0 || len(c.GitHubOwnerTokens) != 0 || c.Devboxes.RegistryURL != "" {
		return errors.New("config: execution worker cannot configure provider credentials or devbox discovery")
	}
	return nil
}

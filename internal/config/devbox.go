package config

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"path/filepath"
	"strings"

	"go.kenn.io/forge/internal/federation"
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

// NormalizeDefaultExecutionTarget checks target syntax without requiring a
// live connection and returns its canonical form. A target names a devbox
// connection (devbox:<connection-id>), or a fleet member by its HTTPS origin
// or node ID.
func NormalizeDefaultExecutionTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" || federation.ValidNodeID(target) {
		return target, nil
	}
	if id, ok := strings.CutPrefix(target, "devbox:"); ok {
		if id == "" || strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
			return "", errors.New("config: workspaces.default_execution_target devbox:<connection-id> may use only letters, digits, hyphens, or underscores")
		}
		return target, nil
	}
	if strings.HasPrefix(strings.ToLower(target), "https://") {
		origin, err := federation.CanonicalOrigin(target)
		if err != nil {
			return "", fmt.Errorf("config: workspaces.default_execution_target: %w", err)
		}
		return origin, nil
	}
	return "", errors.New("config: workspaces.default_execution_target must be empty, devbox:<connection-id>, a fleet member's HTTPS origin, or its 32-character node ID")
}

func (c *Config) validateDevboxes() error {
	target, err := NormalizeDefaultExecutionTarget(c.Workspaces.DefaultExecutionTarget)
	if err != nil {
		return err
	}
	c.Workspaces.DefaultExecutionTarget = target
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
		c.Fleet.Enabled || c.Fleet.RoleOrDefault() == FleetRoleSpoke || c.MCPEnabled() || c.BasePath != "/" {
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
	// Git stores a bare address, including GitHub bot names containing [bot].
	// Quote its local part for mail's header parser without changing Git identity.
	mailbox := mail.Address{Address: worker.CommitEmail}
	address, err := mail.ParseAddress(mailbox.String())
	if err != nil || address.Address != worker.CommitEmail || address.Name != "" ||
		strings.Count(worker.CommitEmail, "@") != 1 || strings.ContainsAny(worker.CommitEmail, " \t\r\n<>,") {
		return errors.New("config: execution worker requires a commit email address")
	}
	if len(c.Platforms) != 0 || len(c.GitHubApps) != 0 || len(c.GitHubOwnerTokens) != 0 || c.Devboxes.RegistryURL != "" {
		return errors.New("config: execution worker cannot configure provider credentials or devbox discovery")
	}
	return nil
}

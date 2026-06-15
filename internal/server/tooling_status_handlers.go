package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/procutil"
)

// Tooling status reports this host's view of the git/gh/glab CLIs so
// the UI can gate platform-dependent surfaces (Connect GitHub, the New
// Worktree sheet) and render specific recovery copy when a tool is
// missing or unauthenticated. Probes shell out with short per-command
// timeouts and the assembled status is cached briefly so rapid UI
// refreshes do not spawn subprocess storms.

// toolingProbeTimeout bounds each probe subprocess. A hung credential
// helper inside `gh api user` must not stall the status response.
const toolingProbeTimeout = 3 * time.Second

// toolingStatusCacheTTL is how long an assembled status is served
// without re-probing. Tool availability and auth state change rarely
// (an install or `gh auth login`), so a short TTL keeps the UI fresh
// enough while absorbing refresh bursts.
const toolingStatusCacheTTL = 30 * time.Second

type toolingGitStatus struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
}

// toolingCLIStatus reports a platform CLI (gh, glab): availability,
// authentication against the probed platform host, and the
// authenticated login when resolvable.
type toolingCLIStatus struct {
	Available     bool   `json:"available"`
	Authenticated bool   `json:"authenticated"`
	User          string `json:"user,omitempty"`
	Host          string `json:"host,omitempty"`
}

type toolingStatusBody struct {
	Git  toolingGitStatus `json:"git"`
	Gh   toolingCLIStatus `json:"gh"`
	Glab toolingCLIStatus `json:"glab"`
}

type toolingStatusOutput struct {
	Body toolingStatusBody
}

// toolingRunner runs one probe command and returns its stdout.
// Injectable so tests can simulate availability and auth states
// without real binaries on PATH.
type toolingRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func defaultToolingRunner(
	ctx context.Context, name string, args ...string,
) ([]byte, error) {
	cmd := procutil.CommandContext(ctx, name, args...)
	return procutil.Output(ctx, cmd, "tooling status probe")
}

// toolingStatusCache memoizes the assembled status for a short TTL.
type toolingStatusCache struct {
	mu      sync.Mutex
	body    *toolingStatusBody
	probedA time.Time
}

func (s *Server) getToolingStatus(
	ctx context.Context, _ *struct{},
) (*toolingStatusOutput, error) {
	out := &toolingStatusOutput{}
	out.Body = s.toolingStatusCached(ctx)
	return out, nil
}

// toolingStatusCached returns the cached status when fresh, probing
// otherwise. The probe runs under the cache lock: concurrent first
// requests coalesce into one subprocess sweep instead of racing.
func (s *Server) toolingStatusCached(ctx context.Context) toolingStatusBody {
	c := &s.toolingStatus
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.now()
	if c.body != nil && now.Sub(c.probedA) < toolingStatusCacheTTL {
		return *c.body
	}
	body := s.probeToolingStatus(ctx)
	c.body = &body
	c.probedA = now
	return body
}

func (s *Server) probeToolingStatus(ctx context.Context) toolingStatusBody {
	run := s.toolingRun
	if run == nil {
		run = defaultToolingRunner
	}
	githubHost, gitlabHost := s.toolingPlatformHosts()
	return toolingStatusBody{
		Git: probeToolingGit(ctx, run),
		Gh: probeToolingCLI(ctx, run, "gh", githubHost,
			func(ctx context.Context) bool {
				_, err := runToolingProbe(ctx, run,
					"gh", "auth", "token", "--hostname", githubHost)
				return err == nil
			},
			func(ctx context.Context) string {
				out, err := runToolingProbe(ctx, run,
					"gh", "api", "user",
					"--hostname", githubHost, "--jq", ".login")
				if err != nil {
					return ""
				}
				return strings.TrimSpace(string(out))
			},
		),
		Glab: probeToolingCLI(ctx, run, "glab", gitlabHost,
			func(ctx context.Context) bool {
				_, err := runToolingProbe(ctx, run,
					"glab", "auth", "status", "--hostname", gitlabHost)
				return err == nil
			},
			func(ctx context.Context) string {
				out, err := runToolingProbe(ctx, run,
					"glab", "api", "user", "--hostname", gitlabHost)
				if err != nil {
					return ""
				}
				var user struct {
					Username string `json:"username"`
				}
				if json.Unmarshal(out, &user) != nil {
					return ""
				}
				return user.Username
			},
		),
	}
}

// toolingPlatformHosts picks the platform host each CLI is probed
// against: the first configured repo or [[platforms]] entry for the
// provider, falling back to the provider's public host. Self-hosted
// and GHE deployments are probed where the user actually
// authenticates instead of a hardcoded public host.
func (s *Server) toolingPlatformHosts() (githubHost, gitlabHost string) {
	githubHost = "github.com"
	gitlabHost = "gitlab.com"

	// Config reloads mutate Repos/Platforms under cfgMu, so the
	// whole walk stays inside the lock.
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	cfg := s.cfg
	if cfg == nil {
		return githubHost, gitlabHost
	}
	if cfg.DefaultPlatformHost != "" {
		githubHost = cfg.DefaultPlatformHost
	}
	pick := func(platform, fallback string) string {
		for _, repo := range cfg.Repos {
			if repo.PlatformOrDefault() == platform &&
				repo.PlatformHostOrDefault() != "" {
				return repo.PlatformHostOrDefault()
			}
		}
		for _, p := range cfg.Platforms {
			if p.Type == platform && p.Host != "" {
				return p.Host
			}
		}
		return fallback
	}
	return pick("github", githubHost), pick("gitlab", gitlabHost)
}

// runToolingProbe wraps one probe command in the per-command timeout
// so a stalled subprocess cannot stall the sweep; the caller's
// context still propagates cancellation.
func runToolingProbe(
	ctx context.Context, run toolingRunner, name string, args ...string,
) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, toolingProbeTimeout)
	defer cancel()
	return run(probeCtx, name, args...)
}

func probeToolingGit(ctx context.Context, run toolingRunner) toolingGitStatus {
	out, err := runToolingProbe(ctx, run, "git", "--version")
	if err != nil {
		return toolingGitStatus{}
	}
	return toolingGitStatus{
		Available: true,
		Version:   parseToolingGitVersion(string(out)),
	}
}

// parseToolingGitVersion extracts the X.Y.Z portion of
// "git version X.Y.Z (...)", returning "" on unexpected output.
func parseToolingGitVersion(out string) string {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return ""
	}
	return fields[2]
}

// probeToolingCLI probes one platform CLI: availability via
// `<cli> --version`, then authentication and the authenticated user
// only when available. The user lookup is best-effort — a failure
// leaves the user empty without flipping authenticated.
func probeToolingCLI(
	ctx context.Context,
	run toolingRunner,
	cli string,
	host string,
	authenticated func(context.Context) bool,
	user func(context.Context) string,
) toolingCLIStatus {
	if _, err := runToolingProbe(ctx, run, cli, "--version"); err != nil {
		return toolingCLIStatus{}
	}
	status := toolingCLIStatus{Available: true, Host: host}
	if !authenticated(ctx) {
		return status
	}
	status.Authenticated = true
	status.User = user(ctx)
	return status
}

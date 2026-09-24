package devbox

import (
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/server/httpapi"
)

const Protocol = 1

type WorkerIdentity struct {
	NodeID       string `json:"node_id" toml:"node_id"`
	UID          uint32 `json:"uid" toml:"uid"`
	GitHubUserID int64  `json:"github_user_id" toml:"github_user_id"`
	Role         string `json:"role" toml:"role"`
	Protocol     int    `json:"protocol" toml:"protocol"`
}

type Assignment struct {
	WorkerIdentity
	HostID             string `json:"host_id" toml:"host_id"`
	Name               string `json:"name" toml:"name"`
	URL                string `json:"url" toml:"url"`
	Account            string `json:"account" toml:"account"`
	SSHAddress         string `json:"ssh_address" toml:"ssh_address"`
	SSHHostFingerprint string `json:"ssh_host_fingerprint" toml:"ssh_host_fingerprint"`
	Maintenance        bool   `json:"maintenance" toml:"maintenance"`
	Enabled            bool   `json:"-" toml:"enabled"`
	TokenFile          string `json:"-" toml:"token_file"`
}

type Developer struct {
	GitHubUserID      int64    `toml:"github_user_id"`
	TailscaleUserID   int64    `toml:"tailscale_user_id"`
	ControllerNodeIDs []string `toml:"controller_node_ids"`
	Enabled           bool     `toml:"enabled"`
}

type RegistryConfig struct {
	Socket      string       `toml:"socket"`
	RegistryID  string       `toml:"registry_id"`
	Revision    string       `toml:"revision"`
	Developers  []Developer  `toml:"developers"`
	Assignments []Assignment `toml:"assignments"`
}

type Discovery struct {
	RegistryID   string       `json:"registry_id"`
	Revision     string       `json:"revision"`
	Protocol     int          `json:"protocol"`
	GitHubUserID int64        `json:"github_user_id"`
	Devboxes     []Assignment `json:"devboxes"`
	// RegistryURL is the address the controller queried. The registry itself
	// leaves it empty; the controller fills it so clients can show which
	// registry answered without guessing from configuration.
	RegistryURL string `json:"registry_url,omitempty"`
}

// Profile is exchanged only between native services. Browser responses must use Assignment.
type Profile struct {
	Assignment
	RegistryID string `json:"registry_id"`
	Revision   string `json:"revision"`
	Token      string `json:"token"`
}

type tailnetPeer struct {
	Node struct {
		StableID string
		Tags     []string
	}
	UserProfile struct{ ID int64 }
}

func whoIs(ctx context.Context, address string) (tailnetPeer, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := procutil.CommandContext(ctx, "tailscale", "whois", "--json", address).Output()
	if err != nil {
		return tailnetPeer{}, errors.New("tailscale identity lookup failed")
	}
	var peer tailnetPeer
	if err := json.Unmarshal(out, &peer); err != nil {
		return peer, errors.New("invalid Tailscale identity response")
	}
	return peer, nil
}

func (c RegistryConfig) validate() error {
	if !filepath.IsAbs(c.Socket) || c.RegistryID == "" || c.Revision == "" {
		return errors.New("registry requires an absolute socket path, registry_id and revision")
	}
	githubIDs, userIDs, nodes := map[int64]bool{}, map[int64]bool{}, map[string]bool{}
	for _, developer := range c.Developers {
		if developer.GitHubUserID <= 0 || githubIDs[developer.GitHubUserID] || developer.TailscaleUserID < 0 ||
			(developer.TailscaleUserID == 0 && len(developer.ControllerNodeIDs) == 0) ||
			(developer.TailscaleUserID != 0 && userIDs[developer.TailscaleUserID]) {
			return errors.New("registry developer identities must be unique and complete")
		}
		githubIDs[developer.GitHubUserID], userIDs[developer.TailscaleUserID] = true, true
		for _, node := range developer.ControllerNodeIDs {
			if strings.TrimSpace(node) == "" || nodes[node] {
				return errors.New("registry controller node assignments must be unique")
			}
			nodes[node] = true
		}
	}
	assignments, workers := map[string]bool{}, map[string]bool{}
	for _, assignment := range c.Assignments {
		key := fmt.Sprintf("%d/%s", assignment.GitHubUserID, assignment.HostID)
		if !githubIDs[assignment.GitHubUserID] || assignment.HostID == "" || strings.ContainsAny(assignment.HostID, "/?# ") || assignments[key] || workers[assignment.NodeID] {
			return errors.New("registry assignments must have an enrolled developer and unique host/account and worker identities")
		}
		if err := validateAssignment(assignment); err != nil {
			return err
		}
		if !filepath.IsAbs(assignment.TokenFile) {
			return errors.New("worker token_file must be absolute")
		}
		assignments[key], workers[assignment.NodeID] = true, true
	}
	return nil
}

func validateAssignment(a Assignment) error {
	decoded, err := hex.DecodeString(a.NodeID)
	if err != nil || len(decoded) != 16 || a.UID == 0 || a.GitHubUserID <= 0 || a.Role != "execution" || a.Protocol != Protocol || a.Account == "" || a.Name == "" {
		return errors.New("worker assignment has invalid identity or protocol")
	}
	u, err := url.Parse(a.URL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		(u.Scheme != "http" && u.Scheme != "https") || !strings.HasSuffix(u.Hostname(), ".ts.net") {
		return errors.New("worker URL must name its complete Tailscale DNS endpoint")
	}
	return nil
}

func (c RegistryConfig) developer(peer tailnetPeer) (int64, error) {
	var userMatch, nodeMatch *Developer
	for i := range c.Developers {
		developer := &c.Developers[i]
		if len(peer.Node.Tags) == 0 && peer.UserProfile.ID != 0 && developer.TailscaleUserID == peer.UserProfile.ID {
			userMatch = developer
		}
		if peer.Node.StableID != "" && slices.Contains(developer.ControllerNodeIDs, peer.Node.StableID) {
			nodeMatch = developer
		}
	}
	if userMatch != nil && nodeMatch != nil && userMatch.GitHubUserID != nodeMatch.GitHubUserID {
		return 0, errors.New("controller node assignment conflicts with the Tailscale user")
	}
	if nodeMatch != nil {
		userMatch = nodeMatch
	}
	if userMatch == nil || !userMatch.Enabled {
		return 0, errors.New("this Tailscale identity is not enrolled for devboxes")
	}
	return userMatch.GitHubUserID, nil
}

type registryPeerKey struct{}

func registryHandler(c RegistryConfig, tokens map[string]string, lookup func(context.Context, string) (tailnetPeer, error)) http.Handler {
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("Devbox discovery", "1")
	cfg.OpenAPIPath, cfg.DocsPath, cfg.SchemasPath = "", "", ""
	api := humago.New(mux, cfg)
	registerRegistryRoutes(api, c, tokens, lookup)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Only Caddy can reach this Unix socket. These headers are never trusted on TCP.
		address := net.JoinHostPort(r.Header.Get("X-Devbox-Peer-IP"), r.Header.Get("X-Devbox-Peer-Port"))
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), registryPeerKey{}, address)))
	})
}

func registerRegistryRoutes(api huma.API, c RegistryConfig, tokens map[string]string, lookup func(context.Context, string) (tailnetPeer, error)) {
	identify := func(ctx context.Context) (int64, error) {
		address, _ := ctx.Value(registryPeerKey{}).(string)
		if _, err := netip.ParseAddrPort(address); err != nil {
			return 0, httpapi.NewProblem(http.StatusUnauthorized, httpapi.CodeUnauthorized, "trusted proxy peer address is missing", nil)
		}
		peer, err := lookup(ctx, address)
		if err != nil {
			return 0, httpapi.ServiceUnavailable(err.Error())
		}
		id, err := c.developer(peer)
		if err != nil {
			return 0, httpapi.Forbidden(err.Error(), nil)
		}
		return id, nil
	}
	huma.Get(api, "/healthz", func(context.Context, *struct{}) (*struct {
		Body struct {
			Status string `json:"status"`
		}
	}, error,
	) {
		out := &struct {
			Body struct {
				Status string `json:"status"`
			}
		}{}
		out.Body.Status = "ok"
		return out, nil
	})
	huma.Get(api, "/api/v1/devboxes", func(ctx context.Context, _ *struct{}) (*struct{ Body Discovery }, error) {
		id, err := identify(ctx)
		if err != nil {
			return nil, err
		}
		out := Discovery{RegistryID: c.RegistryID, Revision: c.Revision, Protocol: Protocol, GitHubUserID: id, Devboxes: []Assignment{}}
		for _, assignment := range c.Assignments {
			if assignment.Enabled && assignment.GitHubUserID == id {
				out.Devboxes = append(out.Devboxes, assignment)
			}
		}
		return &struct{ Body Discovery }{Body: out}, nil
	})
	huma.Post(api, "/api/v1/devboxes/{host_id}/connect", func(ctx context.Context, input *struct {
		HostID string `path:"host_id"`
	},
	) (*struct{ Body Profile }, error) {
		id, err := identify(ctx)
		if err != nil {
			return nil, err
		}
		for _, assignment := range c.Assignments {
			if assignment.Enabled && assignment.GitHubUserID == id && assignment.HostID == input.HostID {
				return &struct{ Body Profile }{Body: Profile{Assignment: assignment, RegistryID: c.RegistryID, Revision: c.Revision, Token: tokens[assignment.NodeID]}}, nil
			}
		}
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "devbox account is not assigned or is disabled", nil)
	})
}

func ServeRegistry(ctx context.Context, c RegistryConfig) error {
	if err := c.validate(); err != nil {
		return err
	}
	tokens := map[string]string{}
	for _, assignment := range c.Assignments {
		if !assignment.Enabled {
			continue
		}
		raw, err := os.ReadFile(assignment.TokenFile)
		if err != nil {
			return fmt.Errorf("read worker token: %w", err)
		}
		token := strings.TrimSpace(string(raw))
		if len(token) < 32 || strings.ContainsAny(token, " \r\n\t") {
			return errors.New("worker token must have at least 32 non-whitespace characters")
		}
		tokens[assignment.NodeID] = token
	}
	return serveUnix(ctx, c.Socket, 0o660, &http.Server{Handler: registryHandler(c, tokens, whoIs), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: time.Minute})
}

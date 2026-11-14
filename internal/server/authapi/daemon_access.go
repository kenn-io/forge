package authapi

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"mime"
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
)

// DaemonAccessOptions configures startup-bound daemon authentication and proof.
type DaemonAccessOptions struct {
	Token                       string
	RequireAPIAuth              bool
	ProofHandler                http.Handler
	TailscaleServeEnabled       bool
	TailscaleServeUsers         []string
	TailscaleServeAppCapability string
}

type DaemonRequestPolicy struct {
	Token                       string
	RequireAPIAuth              bool
	proof                       http.Handler
	tailscaleServeEnabled       bool
	tailscaleServeUsers         map[string]struct{}
	tailscaleServeAppCapability string
}

type daemonRequestAdmission struct {
	BypassProxyHostCheck bool
	Handled              bool
}

func NewDaemonRequestPolicy(options DaemonAccessOptions) DaemonRequestPolicy {
	users := make(map[string]struct{}, len(options.TailscaleServeUsers))
	for _, login := range options.TailscaleServeUsers {
		users[login] = struct{}{}
	}
	return DaemonRequestPolicy{
		Token:                       options.Token,
		RequireAPIAuth:              options.RequireAPIAuth,
		proof:                       options.ProofHandler,
		tailscaleServeEnabled:       options.TailscaleServeEnabled,
		tailscaleServeUsers:         users,
		tailscaleServeAppCapability: options.TailscaleServeAppCapability,
	}
}

func (p DaemonRequestPolicy) TailscaleServeEnabled() bool {
	return p.tailscaleServeEnabled
}

// AcceptsTailscaleServeUser reports whether a loopback request proxied by
// Tailscale Serve carries an allowlisted login or the configured app
// capability. Either grants the full local-user principal.
func (p DaemonRequestPolicy) AcceptsTailscaleServeUser(r *http.Request) bool {
	if !p.tailscaleServeEnabled || !IsLoopbackRemoteAddr(r.RemoteAddr) {
		return false
	}
	return p.allowsTailscaleLogin(r.Header) || p.hasTailscaleAppCapability(r.Header)
}

func (p DaemonRequestPolicy) allowsTailscaleLogin(header http.Header) bool {
	values := header.Values("Tailscale-User-Login")
	if len(values) != 1 {
		return false
	}
	login, err := config.NormalizeTailscaleLogin(values[0])
	if err != nil {
		return false
	}
	_, allowed := p.tailscaleServeUsers[login]
	return allowed
}

// hasTailscaleAppCapability reports whether Serve's capability header, a JSON
// object keyed by capability name that Serve RFC 2047 Q-encodes when it holds
// non-ASCII text, contains the configured capability.
func (p DaemonRequestPolicy) hasTailscaleAppCapability(header http.Header) bool {
	if p.tailscaleServeAppCapability == "" {
		return false
	}
	values := header.Values("Tailscale-App-Capabilities")
	if len(values) != 1 {
		return false
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(values[0])
	if err != nil {
		return false
	}
	var capabilities map[string]jsontext.Value
	if err := json.Unmarshal([]byte(decoded), &capabilities); err != nil {
		return false
	}
	_, granted := capabilities[p.tailscaleServeAppCapability]
	return granted
}

func (p DaemonRequestPolicy) Admit(
	w http.ResponseWriter,
	r *http.Request,
	hostOpts HostCheckOptions,
	gatedAPIRequest bool,
) daemonRequestAdmission {
	direct := isDirectLoopbackListenerRequest(r, hostOpts)
	if r.URL.Path == daemonruntime.ProofPingPath {
		if r.Method != http.MethodGet || !direct {
			RejectHost(w, r, "daemon_proof_not_direct", r.Host, "")
			return daemonRequestAdmission{Handled: true}
		}
		if p.proof != nil {
			p.proof.ServeHTTP(w, r)
			return daemonRequestAdmission{Handled: true}
		}
		return daemonRequestAdmission{BypassProxyHostCheck: true}
	}
	return daemonRequestAdmission{
		BypassProxyHostCheck: gatedAPIRequest &&
			HasValidBearer(r, p.Token) && direct,
	}
}

func isDirectLoopbackListenerRequest(r *http.Request, opts HostCheckOptions) bool {
	bind := opts.Bind
	bind.Host = config.LoopbackHostForBind(strings.Trim(bind.Host, "[]"))
	if strings.Contains(bind.Host, ":") {
		bind.Host = "[" + bind.Host + "]"
	}
	if hasForwardingHeaders(r.Header) || !IsLoopbackRemoteAddr(r.RemoteAddr) ||
		!config.IsLoopbackHostname(strings.Trim(bind.Host, "[]")) {
		return false
	}
	key, err := config.ParseHostKey(r.Host)
	return err == nil && key.Equal(bind)
}

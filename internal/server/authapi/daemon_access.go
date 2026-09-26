package authapi

import (
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
)

// DaemonAccessOptions configures startup-bound daemon authentication and proof.
type DaemonAccessOptions struct {
	Token                 string
	RequireAPIAuth        bool
	ProofHandler          http.Handler
	TailscaleServeEnabled bool
	TailscaleServeUsers   []string
}

type DaemonRequestPolicy struct {
	Token                 string
	RequireAPIAuth        bool
	proof                 http.Handler
	tailscaleServeEnabled bool
	tailscaleServeUsers   map[string]struct{}
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
		Token:                 options.Token,
		RequireAPIAuth:        options.RequireAPIAuth,
		proof:                 options.ProofHandler,
		tailscaleServeEnabled: options.TailscaleServeEnabled,
		tailscaleServeUsers:   users,
	}
}

func (p DaemonRequestPolicy) TailscaleServeEnabled() bool {
	return p.tailscaleServeEnabled
}

func (p DaemonRequestPolicy) AcceptsTailscaleServeUser(r *http.Request) bool {
	if !p.tailscaleServeEnabled || !IsLoopbackRemoteAddr(r.RemoteAddr) {
		return false
	}
	values := r.Header.Values("Tailscale-User-Login")
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
	if hasForwardingHeaders(r.Header) || !IsLoopbackRemoteAddr(r.RemoteAddr) ||
		!config.IsLoopbackHostname(strings.Trim(opts.Bind.Host, "[]")) {
		return false
	}
	key, err := config.ParseHostKey(r.Host)
	return err == nil && key.Equal(opts.Bind)
}

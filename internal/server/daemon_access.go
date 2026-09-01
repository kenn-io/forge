package server

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

type daemonRequestPolicy struct {
	token                 string
	requireAPIAuth        bool
	proof                 http.Handler
	tailscaleServeEnabled bool
	tailscaleServeUsers   map[string]struct{}
}

type daemonRequestAdmission struct {
	bypassProxyHostCheck bool
	handled              bool
}

func newDaemonRequestPolicy(options DaemonAccessOptions) daemonRequestPolicy {
	users := make(map[string]struct{}, len(options.TailscaleServeUsers))
	for _, login := range options.TailscaleServeUsers {
		users[login] = struct{}{}
	}
	return daemonRequestPolicy{
		token:                 options.Token,
		requireAPIAuth:        options.RequireAPIAuth,
		proof:                 options.ProofHandler,
		tailscaleServeEnabled: options.TailscaleServeEnabled,
		tailscaleServeUsers:   users,
	}
}

func (p daemonRequestPolicy) acceptsTailscaleServeUser(r *http.Request) bool {
	if !p.tailscaleServeEnabled || !isLoopbackRemoteAddr(r.RemoteAddr) {
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

func (p daemonRequestPolicy) admit(
	w http.ResponseWriter,
	r *http.Request,
	hostOpts HostCheckOptions,
	gatedAPIRequest bool,
) daemonRequestAdmission {
	direct := isDirectLoopbackListenerRequest(r, hostOpts)
	if r.URL.Path == daemonruntime.ProofPingPath {
		if r.Method != http.MethodGet || !direct {
			rejectHost(w, r, "daemon_proof_not_direct", r.Host, "")
			return daemonRequestAdmission{handled: true}
		}
		if p.proof != nil {
			p.proof.ServeHTTP(w, r)
			return daemonRequestAdmission{handled: true}
		}
		return daemonRequestAdmission{bypassProxyHostCheck: true}
	}
	return daemonRequestAdmission{
		bypassProxyHostCheck: gatedAPIRequest &&
			hasValidBearer(r, p.token) && direct,
	}
}

func isDirectLoopbackListenerRequest(r *http.Request, opts HostCheckOptions) bool {
	if hasForwardingHeaders(r.Header) || !isLoopbackRemoteAddr(r.RemoteAddr) ||
		!config.IsLoopbackHostname(strings.Trim(opts.Bind.Host, "[]")) {
		return false
	}
	key, err := config.ParseHostKey(r.Host)
	return err == nil && key.Equal(opts.Bind)
}

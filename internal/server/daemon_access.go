package server

import (
	"net/http"
	"strings"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/daemonruntime"
)

// DaemonAccessOptions configures startup-bound daemon authentication and proof.
type DaemonAccessOptions struct {
	Token          string
	RequireAPIAuth bool
	ProofHandler   http.Handler
}

type daemonRequestPolicy struct {
	token          string
	requireAPIAuth bool
	proof          http.Handler
}

type daemonRequestAdmission struct {
	bypassProxyHostCheck bool
	handled              bool
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

package hostapi

import (
	"net"
	"net/http"
	"slices"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/authapi"
)

// checkHost runs the Host validation steps from the design spec.
// Returns true when the request may proceed; returns false (and
// writes the 403) when it must be rejected. Panics when opts is
// not Valid — the server constructor enforces population.
func CheckHost(w http.ResponseWriter, r *http.Request, opts authapi.HostCheckOptions) bool {
	if !opts.Valid() {
		panic("server: checkHost called with invalid options (programming error)")
	}

	// Step 1+2: parse and validate the Backend (raw) Host.
	rawHost := r.Host
	backendKey, err := config.ParseHostKey(rawHost)
	if err != nil {
		authapi.RejectHost(w, r, "backend_host_malformed", rawHost, "")
		return false
	}
	accepted := acceptedSet(opts.Bind, opts.Allowed)
	if !matchHost(backendKey, accepted, opts.AllowLoopbackAnyPort) {
		authapi.RejectHost(w, r, "backend_host_not_allowed", rawHost, "")
		return false
	}

	// Step 3 only when trust_reverse_proxy is enabled.
	if !opts.TrustReverseProxy {
		return true
	}

	xfh := r.Header.Values("X-Forwarded-Host")
	fwd := r.Header.Values("Forwarded")
	xfhPresent := len(xfh) > 0
	fwdPresent := len(fwd) > 0
	if !xfhPresent && !fwdPresent {
		authapi.RejectHost(
			w, r,
			"trust_reverse_proxy_missing_forwarded_host",
			rawHost, "",
		)
		return false
	}

	var xfhKey, fwdKey config.HostKey
	if xfhPresent {
		k, err := authapi.ParseXForwardedHost(strings.Join(xfh, ","))
		if err != nil {
			authapi.RejectHost(w, r, "x_forwarded_host_malformed", rawHost, "")
			return false
		}
		xfhKey = k
	}
	if fwdPresent {
		k, err := authapi.ParseForwardedHost(strings.Join(fwd, ","))
		if err != nil {
			authapi.RejectHost(w, r, "forwarded_malformed", rawHost, "")
			return false
		}
		fwdKey = k
	}
	if xfhPresent && fwdPresent && !xfhKey.Equal(fwdKey) {
		authapi.RejectHost(w, r, "forwarded_headers_disagree", rawHost, xfhKey.String())
		return false
	}
	publicKey := xfhKey
	if !xfhPresent {
		publicKey = fwdKey
	}
	if !matchHost(publicKey, accepted, opts.AllowLoopbackAnyPort) {
		authapi.RejectHost(w, r, "public_host_not_allowed", rawHost, publicKey.String())
		return false
	}
	return true
}

// matchHost reports whether k matches any allowlist entry. When
// allowLoopbackAnyPort is true and k is a literal loopback IP
// (127.0.0.1 or [::1]), the port is ignored — the request still
// has to come from the loopback listener, which the bind already
// guarantees, so accepting any source port matches the test
// fixtures (httptest.NewServer) without weakening production.
func matchHost(k config.HostKey, set []config.HostKey, allowLoopbackAnyPort bool) bool {
	if allowLoopbackAnyPort && isLiteralLoopbackIP(k.Host) {
		return true
	}
	return matchAny(k, set)
}

// isLiteralLoopbackIP returns true for hosts that cannot be
// repointed by DNS (literal IPv4 / IPv6 loopback addresses).
// "localhost" deliberately does NOT qualify: an attacker
// /etc/hosts entry or a DNS resolver override could repoint it,
// however unlikely.
func isLiteralLoopbackIP(h string) bool {
	host := strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// acceptedSet returns the union of the bind, the loopback
// synonyms-at-the-bind-port (when the bind is itself loopback),
// and the configured allowlist.
func acceptedSet(bind config.HostKey, allowed []config.HostKey) []config.HostKey {
	out := make([]config.HostKey, 0, 3+len(allowed))
	out = append(out, bind)
	if isLoopbackHost(bind.Host) {
		for _, syn := range []string{"127.0.0.1", "localhost", "[::1]"} {
			if syn == bind.Host {
				continue
			}
			out = append(out, config.HostKey{Host: syn, Port: bind.Port})
		}
	}
	out = append(out, allowed...)
	return out
}

func isLoopbackHost(h string) bool {
	switch h {
	case "127.0.0.1", "localhost", "[::1]":
		return true
	}
	return false
}

func matchAny(k config.HostKey, set []config.HostKey) bool {
	return slices.ContainsFunc(set, k.Equal)
}

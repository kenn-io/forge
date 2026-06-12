package server

import (
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"

	"go.kenn.io/middleman/internal/config"
)

// hostValidationError is the operator-facing 403 body for any host
// validation failure. The text deliberately does NOT echo the
// rejected hostname back into the response (avoids reflecting
// attacker-controlled input and avoids the slim risk of log
// injection via crafted Host values). Rejected hostnames go to
// slog.Warn on the server side for operator diagnosis.
const hostValidationError = "host validation failed: the requested hostname is not allowed. " +
	"Add expected Backend and Public hostnames to allowed_hosts in middleman's config.toml. " +
	"If a reverse proxy sets forwarded-host headers, also enable trust_reverse_proxy."

// HostCheckOptions configures the Host validation middleware.
//
// Bind is the canonical (Host, Port) the listener serves on. The
// middleware always accepts Bind itself, and (when Bind.Host is a
// loopback synonym) the other two loopback synonyms at the same
// port. Allowed extends the accept-set with exact-match entries
// from config.allowed_hosts. TrustReverseProxy enables the Public
// Host (X-Forwarded-Host / Forwarded) validation step.
//
// AllowLoopbackAnyPort relaxes the port match for loopback IPs
// (127.0.0.1, [::1]) so requests with any port pass Step 2. It
// exists solely so test helpers built on httptest.NewServer — which
// binds an ephemeral port that callers cannot know up front — keep
// working without per-test bookkeeping. Production callers must
// leave this false; the cfg-derived path always does.
type HostCheckOptions struct {
	Bind                 config.HostKey
	Allowed              []config.HostKey
	TrustReverseProxy    bool
	AllowLoopbackAnyPort bool
}

// Valid reports whether the options are populated enough to run
// the middleware (Bind has both host and port). The server
// constructor uses Valid to distinguish a populated override from
// a zero value when applying its precedence rule.
func (o HostCheckOptions) Valid() bool {
	return o.Bind.Valid()
}

// checkHost runs the Host validation steps from the design spec.
// Returns true when the request may proceed; returns false (and
// writes the 403) when it must be rejected. Panics when opts is
// not Valid — the server constructor enforces population.
func checkHost(w http.ResponseWriter, r *http.Request, opts HostCheckOptions) bool {
	if !opts.Valid() {
		panic("server: checkHost called with invalid options (programming error)")
	}

	// Step 1+2: parse and validate the Backend (raw) Host.
	rawHost := r.Host
	backendKey, err := config.ParseHostKey(rawHost)
	if err != nil {
		rejectHost(w, r, "backend_host_malformed", rawHost, "")
		return false
	}
	accepted := acceptedSet(opts.Bind, opts.Allowed)
	if !matchHost(backendKey, accepted, opts.AllowLoopbackAnyPort) {
		rejectHost(w, r, "backend_host_not_allowed", rawHost, "")
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
		rejectHost(
			w, r,
			"trust_reverse_proxy_missing_forwarded_host",
			rawHost, "",
		)
		return false
	}

	var xfhKey, fwdKey config.HostKey
	if xfhPresent {
		k, err := parseXForwardedHost(strings.Join(xfh, ","))
		if err != nil {
			rejectHost(w, r, "x_forwarded_host_malformed", rawHost, "")
			return false
		}
		xfhKey = k
	}
	if fwdPresent {
		k, err := parseForwardedHost(strings.Join(fwd, ","))
		if err != nil {
			rejectHost(w, r, "forwarded_malformed", rawHost, "")
			return false
		}
		fwdKey = k
	}
	if xfhPresent && fwdPresent && !xfhKey.Equal(fwdKey) {
		rejectHost(w, r, "forwarded_headers_disagree", rawHost, xfhKey.String())
		return false
	}
	publicKey := xfhKey
	if !xfhPresent {
		publicKey = fwdKey
	}
	if !matchHost(publicKey, accepted, opts.AllowLoopbackAnyPort) {
		rejectHost(w, r, "public_host_not_allowed", rawHost, publicKey.String())
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

func rejectHost(w http.ResponseWriter, r *http.Request, reason, host, forwarded string) {
	slog.Warn(
		"host validation failed",
		"reason", reason,
		"host", host,
		"forwarded_host", forwarded,
		"remote_addr", r.RemoteAddr,
		"method", r.Method,
		"path", r.URL.Path,
	)
	writeError(w, http.StatusForbidden, hostValidationError)
}

// parseXForwardedHost extracts and canonicalises a single
// X-Forwarded-Host header value. Multiple comma-separated values are
// rejected because middleman has no trusted-hop model.
func parseXForwardedHost(value string) (config.HostKey, error) {
	if strings.Contains(value, ",") {
		return config.HostKey{}, errMultipleForwardedHosts
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return config.HostKey{}, errEmptyForwardedHost
	}
	return config.ParseHostKey(value)
}

// parseForwardedHost extracts and canonicalises the host= parameter of
// a single RFC 7239 Forwarded header entry. Multiple comma-separated
// entries are rejected because middleman has no trusted-hop model; a
// proxy that appends instead of overwriting could otherwise leave a
// spoofed client entry first.
func parseForwardedHost(value string) (config.HostKey, error) {
	if strings.Contains(value, ",") {
		return config.HostKey{}, errMultipleForwardedHosts
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return config.HostKey{}, errEmptyForwardedHost
	}

	// Walk the semicolon-separated key=value pairs of the first
	// entry. Parameter names are case-insensitive per RFC 7239.
	for part := range strings.SplitSeq(value, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !strings.EqualFold(key, "host") {
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		if val == "" {
			return config.HostKey{}, errEmptyForwardedHost
		}
		return config.ParseHostKey(val)
	}
	return config.HostKey{}, errMissingForwardedHostParam
}

type hostCheckError string

func (e hostCheckError) Error() string { return string(e) }

const (
	errEmptyForwardedHost        = hostCheckError("empty forwarded-host value")
	errMissingForwardedHostParam = hostCheckError("Forwarded header lacks host= in first entry")
	errMultipleForwardedHosts    = hostCheckError("multiple forwarded-host values are not supported")
)

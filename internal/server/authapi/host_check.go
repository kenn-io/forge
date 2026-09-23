package authapi

import (
	"log/slog"
	"net"
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/config"
)

// hostValidationError is the operator-facing 403 body for any host
// validation failure. The text deliberately does NOT echo the
// rejected hostname back into the response (avoids reflecting
// attacker-controlled input and avoids the slim risk of log
// injection via crafted Host values). Rejected hostnames go to
// slog.Warn on the server side for operator diagnosis.
const hostValidationError = "host validation failed: the requested hostname is not allowed. " +
	"Add expected Backend and Public hostnames to allowed_hosts in kenn-forge's config.toml. " +
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

func hasForwardingHeaders(header http.Header) bool {
	for name := range header {
		if strings.EqualFold(name, "Forwarded") ||
			strings.HasPrefix(strings.ToLower(name), "x-forwarded-") {
			return true
		}
	}
	return false
}

func ListenerHostKey(ln net.Listener) (config.HostKey, bool) {
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return config.HostKey{}, false
	}
	key, err := config.ParseHostKey(net.JoinHostPort(host, port))
	return key, err == nil
}

func RejectHost(w http.ResponseWriter, r *http.Request, reason, host, forwarded string) {
	slog.Warn(
		"host validation failed",
		"reason", reason,
		"host", host,
		"forwarded_host", forwarded,
		"remote_addr", r.RemoteAddr,
		"method", r.Method,
		"path", r.URL.Path,
	)
	WriteError(w, http.StatusForbidden, hostValidationError)
}

// parseXForwardedHost extracts and canonicalises a single
// X-Forwarded-Host header value. Multiple comma-separated values are
// rejected because kenn-forge has no trusted-hop model.
func ParseXForwardedHost(value string) (config.HostKey, error) {
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
// entries are rejected because kenn-forge has no trusted-hop model; a
// proxy that appends instead of overwriting could otherwise leave a
// spoofed client entry first.
func ParseForwardedHost(value string) (config.HostKey, error) {
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

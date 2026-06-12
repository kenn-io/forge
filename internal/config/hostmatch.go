package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// HostKey is the canonical representation of a host header value
// (or a configured allowed_hosts entry). Host is lowercased; for
// IPv6 literals it keeps the surrounding brackets so the textual
// form roundtrips. Port is the digits-only port string, or "" when
// the source value had no explicit port. ParseHostKey is the only
// constructor; downstream code should treat the struct as
// effectively immutable after parsing.
type HostKey struct {
	Host string
	Port string
}

// ParseHostKey canonicalises a host header value or
// configuration entry. Accepted shapes:
//
//   - bare host:      "mm.local", "LOCALHOST" (case-folded)
//   - host with port: "mm.local:8091", "127.0.0.1:8091"
//   - bracketed IPv6: "[::1]", "[::1]:8091"
//
// Rejected: empty / whitespace-only input, port-only ("·:8091"),
// unbracketed IPv6 literals ("::1", "::1:8091"), ports outside
// 1-65535.
func ParseHostKey(s string) (HostKey, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return HostKey{}, fmt.Errorf("host: empty")
	}

	// Bracketed IPv6 path: [ipv6] or [ipv6]:port.
	if strings.HasPrefix(s, "[") {
		closing := strings.IndexByte(s, ']')
		if closing < 0 {
			return HostKey{}, fmt.Errorf("host: missing closing bracket")
		}
		host := s[1:closing]
		// A bracketed value must parse as an IP literal and the
		// textual form must contain a colon (IPv6 textual form
		// always does; dotted-quad IPv4 never does). This rejects
		// "[127.0.0.1]" while accepting IPv4-mapped IPv6 like
		// "[::ffff:7f00:1]".
		if ip := net.ParseIP(host); ip == nil || !strings.ContainsRune(host, ':') {
			return HostKey{}, fmt.Errorf(
				"host: bracketed value %q is not an IPv6 literal", host,
			)
		}
		rest := s[closing+1:]
		var port string
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return HostKey{}, fmt.Errorf(
					"host: unexpected trailing data %q after bracketed host", rest,
				)
			}
			p, err := parsePort(rest[1:])
			if err != nil {
				return HostKey{}, err
			}
			port = p
		}
		return HostKey{Host: "[" + strings.ToLower(host) + "]", Port: port}, nil
	}

	// host:port (last colon) or bare host. Splitting on the last
	// colon catches unbracketed IPv6 ("::1", "::1:8091") because
	// the host part after the split still contains a `:`; we
	// reject that explicitly below.
	if idx := strings.LastIndexByte(s, ':'); idx >= 0 {
		host := s[:idx]
		portStr := s[idx+1:]
		if host == "" {
			return HostKey{}, fmt.Errorf("host: port-only input %q", s)
		}
		if strings.ContainsRune(host, ':') {
			return HostKey{}, fmt.Errorf(
				"host: unbracketed IPv6 literal %q (use [ipv6]:port instead)", s,
			)
		}
		port, err := parsePort(portStr)
		if err != nil {
			return HostKey{}, err
		}
		return HostKey{Host: strings.ToLower(host), Port: port}, nil
	}

	// Bare host, no port.
	return HostKey{Host: strings.ToLower(s), Port: ""}, nil
}

func parsePort(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("host: empty port")
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return "", fmt.Errorf("host: invalid port %q: %w", p, err)
	}
	if n < 1 || n > 65535 {
		return "", fmt.Errorf("host: port %d out of range", n)
	}
	return p, nil
}

// String renders the canonical form. Bracketed IPv6 hosts already
// carry their brackets in Host, so the renderer just joins.
func (k HostKey) String() string {
	if k.Port == "" {
		return k.Host
	}
	return k.Host + ":" + k.Port
}

// Equal reports whether the keys match. Hosts are already
// lowercased by ParseHostKey, so this is an exact-string compare
// on both fields.
func (k HostKey) Equal(other HostKey) bool {
	return k.Host == other.Host && k.Port == other.Port
}

// Valid reports whether the key carries both host and port. The
// server constructor uses this to distinguish a populated bind
// key from the zero value when deciding between caller override,
// cfg-derived, and the cfg=nil test-friendly default.
func (k HostKey) Valid() bool {
	return k.Host != "" && k.Port != ""
}

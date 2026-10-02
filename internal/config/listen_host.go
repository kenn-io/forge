package config

import "net"

// LoopbackHostForBind gives local clients a concrete address for wildcard listeners.
// Specific listener addresses are preserved.
func LoopbackHostForBind(host string) string {
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsUnspecified() {
		return host
	}
	if ip.To4() != nil {
		return "127.0.0.1"
	}
	return "::1"
}

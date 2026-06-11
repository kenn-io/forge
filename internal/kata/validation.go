package kata

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// RedactURL strips credentials and token-like URL components before a daemon
// target is shown to clients or logs.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	httpPath := (u.Scheme == "http" || u.Scheme == "https") && (u.Path != "" || u.RawPath != "")
	if u.User == nil && u.RawQuery == "" && u.Fragment == "" && !httpPath {
		return raw
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	if httpPath {
		u.Path = ""
		u.RawPath = ""
	}
	return u.String()
}

// ValidateTarget enforces the same posture as Kata's secure/private target
// guard: plain http to public or hostname targets is refused unless
// AllowInsecure is set.
func ValidateTarget(d Daemon) error {
	if d.URL == "" {
		return nil
	}
	u, err := url.Parse(d.URL)
	if err != nil {
		return fmt.Errorf("daemon %q: invalid url", d.ID)
	}
	switch u.Scheme {
	case "https":
		if strings.TrimSpace(u.Hostname()) == "" {
			return fmt.Errorf("daemon %q: https url must include a host", d.ID)
		}
		return nil
	case "unix":
		if strings.TrimSpace(u.Path) == "" {
			return fmt.Errorf("daemon %q: unix url must include a socket path", d.ID)
		}
		return nil
	case "http":
		if strings.TrimSpace(u.Hostname()) == "" {
			return fmt.Errorf("daemon %q: http url must include a host", d.ID)
		}
		if d.AllowInsecure || isPrivateOrLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("daemon %q: refusing plain http to public target %q; use https or set allow_insecure",
			d.ID, u.Hostname())
	default:
		return fmt.Errorf("daemon %q: url scheme must be http, https, or unix, got %q", d.ID, u.Scheme)
	}
}

// ValidateLocalTarget restricts a resolved local daemon to unix sockets or
// loopback http(s) URLs.
func ValidateLocalTarget(d Daemon) error {
	if d.URL == "" {
		return nil
	}
	if !isLocalDaemonAddress(d.URL) {
		return fmt.Errorf("daemon %q: local daemon target must be unix or loopback http(s), got %q", d.ID, RedactURL(d.URL))
	}
	return nil
}

// ResolveDaemon resolves TokenEnv and validates the daemon target.
func ResolveDaemon(d Daemon) (Daemon, error) {
	if d.TokenEnv != "" {
		v := strings.TrimSpace(os.Getenv(d.TokenEnv))
		if v == "" {
			return d, fmt.Errorf("daemon %q: token_env %q is unset or empty", d.ID, d.TokenEnv)
		}
		d.Token = v
	}
	var err error
	if d.Local {
		err = ValidateLocalTarget(d)
	} else {
		err = ValidateTarget(d)
	}
	if err != nil {
		return d, err
	}
	return d, nil
}

func isPrivateOrLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSuffix(host, ".")) {
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 0x40 {
		return true
	}
	return false
}

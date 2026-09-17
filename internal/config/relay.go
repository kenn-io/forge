package config

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// Relay is an optional activity source. Changing it requires a daemon restart.
type Relay struct {
	URL string `toml:"url,omitempty"`
}

func (r *Relay) Validate() error {
	if r.URL == "" {
		return nil
	}
	endpoint, err := url.Parse(r.URL)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		(endpoint.Path != "" && endpoint.Path != "/") {
		return errors.New("config: relay.url must be an origin without credentials, path, query, or fragment")
	}
	if endpoint.Scheme != "https" && (endpoint.Scheme != "http" || !net.ParseIP(endpoint.Hostname()).IsLoopback()) {
		return errors.New("config: relay.url requires HTTPS, except on a loopback address")
	}
	r.URL = strings.TrimSuffix(r.URL, "/")
	return nil
}

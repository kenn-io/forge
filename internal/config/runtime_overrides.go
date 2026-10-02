package config

import (
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Overrides contains explicitly supplied foreground listener flags.
type Overrides struct {
	Host *string
	Port *int
}

func (o Overrides) clone() Overrides {
	if o.Host != nil {
		o.Host = new(*o.Host)
	}
	if o.Port != nil {
		o.Port = new(*o.Port)
	}
	return o
}

type runtimeOverlay struct {
	flags   Overrides
	sources map[string]string
	file    runtimeValues
}

type runtimeValues struct {
	Host              string   `toml:"host"`
	Port              int      `toml:"port"`
	DataDir           string   `toml:"data_dir"`
	AllowedHosts      []string `toml:"allowed_hosts"`
	TrustReverseProxy bool     `toml:"trust_reverse_proxy"`
	API               API      `toml:"api"`
	Roborev           Roborev  `toml:"roborev"`
}

var runtimeEnvNames = []string{
	"KENN_FORGE_HOST", "KENN_FORGE_PORT", "KENN_FORGE_DATA_DIR", "KENN_FORGE_ALLOWED_HOSTS",
	"KENN_FORGE_TRUST_REVERSE_PROXY", "KENN_FORGE_REQUIRE_AUTH", "KENN_FORGE_ROBOREV_ENDPOINT",
}

var runtimeKeys = [][]string{
	{"host"},
	{"port"},
	{"data_dir"},
	{"allowed_hosts"},
	{"trust_reverse_proxy"},
	{"api", "require_auth"},
	{"roborev", "endpoint"},
}

// RuntimeOverrides returns an owned copy of the flags retained for reloads.
func (c *Config) RuntimeOverrides() Overrides {
	if c == nil || c.runtime == nil {
		return Overrides{}
	}
	return c.runtime.flags.clone()
}

// RuntimeSources identifies the winning source of each runtime setting.
func (c *Config) RuntimeSources() map[string]string {
	if c == nil || c.runtime == nil {
		return nil
	}
	return maps.Clone(c.runtime.sources)
}

func parseRuntimePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("KENN_FORGE_PORT must be an integer between 1 and 65535")
	}
	return port, nil
}

func (c *Config) applyRuntimeOverrides(meta toml.MetaData, flags Overrides) error {
	c.runtime = &runtimeOverlay{
		flags: flags.clone(), sources: make(map[string]string),
		file: runtimeValues{
			Host: c.Host, Port: c.Port, DataDir: c.DataDir, AllowedHosts: slices.Clone(c.AllowedHosts),
			TrustReverseProxy: c.TrustReverseProxy, API: c.API, Roborev: c.Roborev,
		},
	}
	for i, name := range runtimeEnvNames {
		key := strings.Join(runtimeKeys[i], ".")
		c.runtime.sources[key] = "default"
		if meta.IsDefined(runtimeKeys[i]...) {
			c.runtime.sources[key] = "file"
		}
		if (name == "KENN_FORGE_HOST" && flags.Host != nil) || (name == "KENN_FORGE_PORT" && flags.Port != nil) {
			c.runtime.sources[key] = "flag"
			continue
		}
		value, present := os.LookupEnv(name)
		if !present {
			continue
		}
		c.runtime.sources[key] = "env"
		if err := c.applyRuntimeEnv(name, value); err != nil {
			return err
		}
	}
	if flags.Host != nil {
		c.Host = *flags.Host
	}
	if flags.Port != nil {
		c.Port = *flags.Port
	}
	return nil
}

func (c *Config) applyRuntimeEnv(name, value string) error {
	switch name {
	case "KENN_FORGE_HOST":
		c.Host = value
	case "KENN_FORGE_PORT":
		port, err := parseRuntimePort(value)
		if err != nil {
			return err
		}
		c.Port = port
	case "KENN_FORGE_DATA_DIR":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
		c.DataDir = value
	case "KENN_FORGE_ALLOWED_HOSTS":
		c.AllowedHosts = nil
		if strings.TrimSpace(value) != "" {
			for entry := range strings.SplitSeq(value, ",") {
				entry = strings.TrimSpace(entry)
				if entry == "" {
					return fmt.Errorf("%s contains an empty authority", name)
				}
				c.AllowedHosts = append(c.AllowedHosts, entry)
			}
		}
	case "KENN_FORGE_REQUIRE_AUTH", "KENN_FORGE_TRUST_REVERSE_PROXY":
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s must be a boolean", name)
		}
		if name == "KENN_FORGE_REQUIRE_AUTH" {
			c.API.RequireAuth = enabled
		} else {
			c.TrustReverseProxy = enabled
		}
	case "KENN_FORGE_ROBOREV_ENDPOINT":
		endpoint, err := url.Parse(value)
		if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return fmt.Errorf("%s must be an absolute HTTP or HTTPS URL", name)
		}
		c.Roborev.Endpoint = value
	}
	return nil
}

// restoreRuntimeFileValues runs after effective validation, before serialization.
// Read the current file to preserve edits that have not reached the watcher yet.
func (c *Config) restoreRuntimeFileValues(path string) error {
	if c.runtime == nil {
		return nil
	}
	overridden := false
	for _, source := range c.runtime.sources {
		if source == "env" || source == "flag" {
			overridden = true
			break
		}
	}
	if !overridden {
		return nil
	}
	file := c.runtime.file
	contents, err := os.ReadFile(path)
	if err == nil {
		file = runtimeValues{Host: defaultHost, Port: defaultPort}
		if _, err := toml.Decode(string(contents), &file); err != nil {
			return fmt.Errorf("read runtime settings for save: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read runtime settings for save: %w", err)
	}
	for key, source := range c.runtime.sources {
		if source != "env" && source != "flag" {
			continue
		}
		switch key {
		case "host":
			c.Host = file.Host
		case "port":
			c.Port = file.Port
		case "data_dir":
			c.DataDir = file.DataDir
		case "allowed_hosts":
			c.AllowedHosts = slices.Clone(file.AllowedHosts)
		case "trust_reverse_proxy":
			c.TrustReverseProxy = file.TrustReverseProxy
		case "api.require_auth":
			c.API.RequireAuth = file.API.RequireAuth
		case "roborev.endpoint":
			c.Roborev.Endpoint = file.Roborev.Endpoint
		}
	}
	return nil
}

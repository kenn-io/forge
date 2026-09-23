package main

import (
	"os"
	"path/filepath"
	"runtime"

	"go.yaml.in/yaml/v3"
)

// Match go-gh's auth.DefaultHost: GH_HOST, the sole configured host, github.com.
// Only host names are retained; credentials are never forwarded to the daemon.
func defaultGHHost() string {
	if host := os.Getenv("GH_HOST"); host != "" {
		return host
	}
	dir := os.Getenv("GH_CONFIG_DIR")
	if dir == "" {
		switch {
		case os.Getenv("XDG_CONFIG_HOME") != "":
			dir = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "gh")
		case runtime.GOOS == "windows" && os.Getenv("AppData") != "":
			dir = filepath.Join(os.Getenv("AppData"), "GitHub CLI")
		default:
			home, err := os.UserHomeDir()
			if err != nil {
				return "github.com"
			}
			dir = filepath.Join(home, ".config", "gh")
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "hosts.yml"))
	if err != nil {
		return "github.com"
	}
	var hosts map[string]yaml.Node
	if yaml.Unmarshal(data, &hosts) == nil && len(hosts) == 1 {
		for host := range hosts {
			return host
		}
	}
	return "github.com"
}

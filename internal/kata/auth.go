package kata

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

const authTokenEnv = "KATA_AUTH_TOKEN"

type authFile struct {
	Auth authConfig `toml:"auth"`
}

type authConfig struct {
	Token string `toml:"token"`
}

// ResolveLocalAuthToken mirrors Kata's local daemon auth source. Local daemon
// entries stay tokenless in the daemon catalog; the local process auth token is
// configured globally for the Kata home.
func ResolveLocalAuthToken() (string, error) {
	if token := strings.TrimSpace(os.Getenv(authTokenEnv)); token != "" {
		return token, nil
	}

	path, err := CatalogPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path derives from KATA_HOME, not request input.
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read kata config %s: %w", path, err)
	}

	var cfg authFile
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return "", fmt.Errorf("parse kata config %s: %w", path, err)
	}
	return strings.TrimSpace(cfg.Auth.Token), nil
}

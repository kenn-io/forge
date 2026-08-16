package kata

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"go.kenn.io/forge/internal/config"
)

const catalogSource = "kata catalog"

// catalogFile mirrors the top-level daemon catalog in Kata's config.toml.
// Unknown tables are ignored by toml.Decode.
type catalogFile struct {
	ActiveDaemon string         `toml:"active_daemon"`
	Daemons      []catalogEntry `toml:"daemon"`
}

type catalogEntry struct {
	Name          string `toml:"name"`
	Local         bool   `toml:"local"`
	URL           string `toml:"url"`
	Token         string `toml:"token"`
	TokenEnv      string `toml:"token_env"`
	AllowInsecure bool   `toml:"allow_insecure"`
}

// LoadCatalog reads Kata's shared daemon catalog. Per-daemon auth fields are
// retained only for remote daemons; local entries are tokenless at the catalog
// boundary and pick up Kata's global local auth during resolution.
func LoadCatalog() (Catalog, error) {
	path, err := CatalogPath()
	if err != nil {
		return Catalog{}, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path derives from KATA_HOME, not request input.
	if errors.Is(err, os.ErrNotExist) {
		return Catalog{}, nil
	}
	if err != nil {
		return Catalog{}, fmt.Errorf("read kata config %s: %w", path, err)
	}
	var cat catalogFile
	if _, err := toml.Decode(string(data), &cat); err != nil {
		return Catalog{}, fmt.Errorf("parse kata config %s: %w", path, err)
	}
	trimCatalog(&cat)
	// declaredOnly carries the decoded token_env names through
	// validation rejections: a decoded-but-invalid catalog's declared
	// credentials must still reach terminal-environment stripping, so
	// every post-decode error returns it alongside the error. Callers
	// treat any error as failure and must not use these daemons.
	declaredOnly := Catalog{}
	for _, e := range cat.Daemons {
		// Collect every decoded token_env, including entries that
		// validation will reject (for example local=true with a URL):
		// a declared credential name must reach stripping regardless.
		if e.TokenEnv != "" {
			declaredOnly.Daemons = append(
				declaredOnly.Daemons, Daemon{TokenEnv: e.TokenEnv},
			)
		}
	}
	seen := make(map[string]struct{}, len(cat.Daemons))
	out := make([]Daemon, 0, len(cat.Daemons))
	for i, e := range cat.Daemons {
		if e.Name == "" {
			return declaredOnly, fmt.Errorf("kata catalog daemon %d: name is required", i)
		}
		if _, dup := seen[e.Name]; dup {
			return declaredOnly, fmt.Errorf("kata catalog: duplicate daemon name %q", e.Name)
		}
		seen[e.Name] = struct{}{}
		if e.Local == (e.URL != "") {
			return declaredOnly, fmt.Errorf(
				"kata catalog daemon %q: exactly one of local or url is required", e.Name)
		}
		daemon := Daemon{
			ID:            e.Name,
			URL:           e.URL,
			AllowInsecure: e.AllowInsecure,
			Local:         e.Local,
			Default:       cat.ActiveDaemon != "" && e.Name == cat.ActiveDaemon,
		}
		if !e.Local {
			if e.Token != "" && e.TokenEnv != "" {
				return declaredOnly, fmt.Errorf(
					"kata catalog daemon %q: token and token_env are mutually exclusive", e.Name)
			}
			if config.IsTmuxNonSecretEnvVar(e.TokenEnv) {
				return declaredOnly, fmt.Errorf(
					"kata catalog daemon %q: token_env %q collides with the "+
						"non-secret environment passed to terminal sessions; "+
						"use a dedicated variable name", e.Name, e.TokenEnv)
			}
			daemon.Token = e.Token
			daemon.TokenEnv = e.TokenEnv
		}
		out = append(out, daemon)
	}
	if cat.ActiveDaemon != "" {
		if _, ok := seen[cat.ActiveDaemon]; !ok {
			return declaredOnly, fmt.Errorf(
				"kata catalog: active_daemon %q is not in the catalog", cat.ActiveDaemon)
		}
	}
	return Catalog{Daemons: out, Source: catalogSource}, nil
}

// TokenEnvNames returns the non-empty daemon token_env names in the
// catalog; they name credentials the daemon may resolve and must be
// stripped from terminal environments.
func (c Catalog) TokenEnvNames() []string {
	names := make([]string, 0, len(c.Daemons))
	for _, d := range c.Daemons {
		if d.TokenEnv != "" {
			names = append(names, d.TokenEnv)
		}
	}
	return names
}

func trimCatalog(cat *catalogFile) {
	cat.ActiveDaemon = strings.TrimSpace(cat.ActiveDaemon)
	for i := range cat.Daemons {
		cat.Daemons[i].Name = strings.TrimSpace(cat.Daemons[i].Name)
		cat.Daemons[i].URL = strings.TrimSpace(cat.Daemons[i].URL)
		cat.Daemons[i].Token = strings.TrimSpace(cat.Daemons[i].Token)
		cat.Daemons[i].TokenEnv = strings.TrimSpace(cat.Daemons[i].TokenEnv)
	}
}

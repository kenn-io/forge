package kata

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
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

// LoadCatalog reads Kata's shared daemon catalog. token_env is intentionally
// not resolved here; ResolveDaemon applies that uniformly across daemon
// sources.
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
	seen := make(map[string]struct{}, len(cat.Daemons))
	out := make([]Daemon, 0, len(cat.Daemons))
	for i, e := range cat.Daemons {
		if e.Name == "" {
			return Catalog{}, fmt.Errorf("kata catalog daemon %d: name is required", i)
		}
		if _, dup := seen[e.Name]; dup {
			return Catalog{}, fmt.Errorf("kata catalog: duplicate daemon name %q", e.Name)
		}
		seen[e.Name] = struct{}{}
		if e.Local == (e.URL != "") {
			return Catalog{}, fmt.Errorf(
				"kata catalog daemon %q: exactly one of local or url is required", e.Name)
		}
		if e.Token != "" && e.TokenEnv != "" {
			return Catalog{}, fmt.Errorf(
				"kata catalog daemon %q: token and token_env are mutually exclusive", e.Name)
		}
		out = append(out, Daemon{
			ID:            e.Name,
			URL:           e.URL,
			Token:         e.Token,
			TokenEnv:      e.TokenEnv,
			AllowInsecure: e.AllowInsecure,
			Local:         e.Local,
			Default:       cat.ActiveDaemon != "" && e.Name == cat.ActiveDaemon,
		})
	}
	if cat.ActiveDaemon != "" {
		if _, ok := seen[cat.ActiveDaemon]; !ok {
			return Catalog{}, fmt.Errorf(
				"kata catalog: active_daemon %q is not in the catalog", cat.ActiveDaemon)
		}
	}
	return Catalog{Daemons: out, Source: catalogSource}, nil
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

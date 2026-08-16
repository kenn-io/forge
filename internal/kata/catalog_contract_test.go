package kata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCatalogRejectsTokenEnvCollidingWithTerminalVars mirrors the
// config-side contract for externally cataloged daemon credentials.
func TestCatalogRejectsTokenEnvCollidingWithTerminalVars(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KATA_HOME", dir)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.toml"), []byte(`
[[daemon]]
name = "prod"
url = "https://kata.example.com"
token_env = "EDITOR"
`), 0o600))
	_, err := LoadCatalog()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides with the non-secret environment")
}

func TestCatalogTokenEnvNames(t *testing.T) {
	cat := Catalog{Daemons: []Daemon{
		{TokenEnv: "KATA_PROD_TOKEN"},
		{TokenEnv: ""},
		{TokenEnv: "KATA_STAGING_TOKEN"},
	}}
	assert.Equal(t,
		[]string{"KATA_PROD_TOKEN", "KATA_STAGING_TOKEN"},
		cat.TokenEnvNames(),
	)
}

// TestLoadCatalogCarriesDeclaredNamesThroughRejection pins that a
// decoded-but-invalid catalog still exposes its declared token_env
// names so stripping covers a rejected catalog's credentials.
func TestLoadCatalogCarriesDeclaredNamesThroughRejection(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	t.Setenv("KATA_HOME", dir)
	require.NoError(os.WriteFile(
		filepath.Join(dir, "config.toml"), []byte(`
[[daemon]]
name = "prod"
url = "https://kata.example.com"
token_env = "KATA_REJECTED_TOKEN"

[[daemon]]
name = "prod"
url = "https://kata2.example.com"

[[daemon]]
name = "confused"
local = true
url = "https://kata3.example.com"
token_env = "KATA_LOCAL_CONFUSED_TOKEN"
`), 0o600))
	cat, err := LoadCatalog()
	require.Error(err)
	assert.Contains(err.Error(), "duplicate daemon name")
	assert.Equal(
		[]string{"KATA_REJECTED_TOKEN", "KATA_LOCAL_CONFUSED_TOKEN"},
		cat.TokenEnvNames(),
		"declared names must include entries validation rejects",
	)
}

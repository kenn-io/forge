package kata

import (
	"os"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCatalogMapsEntries(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_PROD_TOKEN", "prod-secret")
	const cfg = `
active_daemon = "prod"

[[daemon]]
name = "local"
local = true

[[daemon]]
name = "prod"
url = "https://kata.example.com"
token_env = "KATA_PROD_TOKEN"
allow_insecure = true
`
	writeCatalog(t, home, cfg)

	catalog, err := LoadCatalog()
	require.NoError(err)

	assert.Equal("kata catalog", catalog.Source)
	require.Len(catalog.Daemons, 2)
	assert.Equal(Daemon{ID: "local", Local: true}, catalog.Daemons[0])
	assert.Equal(Daemon{
		ID:            "prod",
		URL:           "https://kata.example.com",
		TokenEnv:      "KATA_PROD_TOKEN",
		Default:       true,
		AllowInsecure: true,
	}, catalog.Daemons[1])
}

func TestLoadCatalogAbsentReturnsEmpty(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	t.Setenv("KATA_HOME", t.TempDir())

	catalog, err := LoadCatalog()
	require.NoError(err)

	assert.Empty(catalog.Source)
	assert.Empty(catalog.Daemons)
}

func TestLoadCatalogRejectsDuplicateNames(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	body := "[[daemon]]\nname=\"x\"\nlocal=true\n[[daemon]]\nname=\"x\"\nlocal=true\n"
	writeCatalog(t, home, body)

	_, err := LoadCatalog()

	require.Error(err)
	assert.Contains(err.Error(), "duplicate")
}

func TestLoadCatalogRejectsMissingName(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	body := "[[daemon]]\nlocal = true\n"
	writeCatalog(t, home, body)

	_, err := LoadCatalog()

	require.Error(err)
	assert.Contains(err.Error(), "name is required")
}

func TestLoadCatalogRejectsWhitespaceOnlyName(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	body := "[[daemon]]\nname = \"   \"\nlocal = true\n"
	writeCatalog(t, home, body)

	_, err := LoadCatalog()

	require.Error(err)
	assert.Contains(err.Error(), "name is required")
}

func TestLoadCatalogTrimsDaemonFields(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeCatalog(t, home, `
active_daemon = "  remote  "

[[daemon]]
name = "  remote  "
url = "  https://kata.example.test  "
token = "  target-token  "
`)

	catalog, err := LoadCatalog()
	require.NoError(err)
	require.Len(catalog.Daemons, 1)

	assert.Equal("remote", catalog.Daemons[0].ID)
	assert.Equal("https://kata.example.test", catalog.Daemons[0].URL)
	assert.Equal("target-token", catalog.Daemons[0].Token)
	assert.True(catalog.Daemons[0].Default)
}

func TestLoadCatalogTrimsDaemonTokenEnv(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_WORK_TOKEN", "secret-from-env")
	writeCatalog(t, home, `
[[daemon]]
name = "work"
url = "https://kata.example.test"
token_env = "  KATA_WORK_TOKEN  "
`)

	catalog, err := LoadCatalog()
	require.NoError(err)
	require.Len(catalog.Daemons, 1)

	assert.Empty(catalog.Daemons[0].Token)
	assert.Equal("KATA_WORK_TOKEN", catalog.Daemons[0].TokenEnv)
}

func TestLoadCatalogRejectsNeitherLocalNorURL(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	body := "[[daemon]]\nname = \"x\"\n"
	writeCatalog(t, home, body)

	_, err := LoadCatalog()

	require.Error(err)
	assert.Contains(err.Error(), "local or url")
}

func TestLoadCatalogRejectsBothLocalAndURL(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	body := "[[daemon]]\nname = \"x\"\nlocal = true\nurl = \"https://x.example\"\n"
	writeCatalog(t, home, body)

	_, err := LoadCatalog()

	require.Error(err)
	assert.Contains(err.Error(), "local or url")
}

func TestLoadCatalogRejectsTokenAndTokenEnv(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	body := "[[daemon]]\nname = \"x\"\nurl = \"https://x.example\"\ntoken = \"t\"\ntoken_env = \"E\"\n"
	writeCatalog(t, home, body)

	_, err := LoadCatalog()

	require.Error(err)
	assert.Contains(err.Error(), "mutually exclusive")
}

func TestLoadCatalogRejectsActiveNotInCatalog(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	body := "active_daemon=\"missing\"\n[[daemon]]\nname=\"x\"\nlocal=true\n"
	writeCatalog(t, home, body)

	_, err := LoadCatalog()

	require.Error(err)
	assert.Contains(err.Error(), "active_daemon")
}

func writeCatalog(t *testing.T, home string, body string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600))
}

package main

import (
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/githubapp/ui"
)

// TestFlowServerServesEmbeddedAssetsAndFlowContract pins the loopback
// server's browser surface: the setup page and flow contract are only
// reachable through the per-flow setup URL, and the hand-off contract
// only exists once a create flow has been prepared.
func TestFlowServerServesEmbeddedAssetsAndFlowContract(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	flow, err := newFlowServer(io.Discard)
	require.NoError(err)
	t.Cleanup(flow.Close)

	// The root always answers with a usable HTML page: the built
	// Svelte app when the dist was embedded by `make build`, or an
	// explicit "rebuild with make build" explanation when only the
	// committed stub is present (plain go test / go build).
	resp, err := http.Get(flow.localBase + "/")
	require.NoError(err)
	_ = resp.Body.Close()
	assert.Equal(http.StatusNotFound, resp.StatusCode)

	resp, err = http.Get(flow.setupURL())
	require.NoError(err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(err)
	assert.Equal(http.StatusOK, resp.StatusCode)
	assert.Contains(resp.Header.Get("Content-Type"), "text/html")
	if ui.HasBuiltApp() {
		assert.Contains(string(body), `<div id="app">`)
	} else {
		assert.Contains(string(body), "make build")
	}

	// Before a flow is prepared there is nothing to hand to GitHub.
	resp, err = http.Get(flow.localBase + "/flow.json")
	require.NoError(err)
	_ = resp.Body.Close()
	assert.Equal(http.StatusNotFound, resp.StatusCode)

	resp, err = http.Get(flow.setupURL() + "flow.json")
	require.NoError(err)
	_ = resp.Body.Close()
	assert.Equal(http.StatusNotFound, resp.StatusCode)

	flow.setFlow("https://example.test/settings/apps/new?state=s", `{"name":"x"}`, "x", "github.com")
	resp, err = http.Get(flow.localBase + "/flow.json")
	require.NoError(err)
	_ = resp.Body.Close()
	assert.Equal(http.StatusNotFound, resp.StatusCode)

	resp, err = http.Get(flow.setupURL() + "flow.json")
	require.NoError(err)
	defer resp.Body.Close()
	assert.Equal(http.StatusOK, resp.StatusCode)
	assert.Equal("application/json", resp.Header.Get("Content-Type"))

	// A callback with a wrong state must be rejected, and a good one
	// must land the browser on the setup page's done view.
	resp, err = http.Get(flow.localBase + flow.callbackPath + "?code=c&state=wrong")
	require.NoError(err)
	_ = resp.Body.Close()
	assert.Equal(http.StatusBadRequest, resp.StatusCode)

	resp, err = http.Get(flow.localBase + flow.callbackPath + "?code=c")
	require.NoError(err)
	_ = resp.Body.Close()
	assert.Equal(http.StatusBadRequest, resp.StatusCode)

	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err = noRedirect.Get(flow.localBase + flow.callbackPath + "?code=c&state=" + flow.state)
	require.NoError(err)
	_ = resp.Body.Close()
	assert.Equal(http.StatusFound, resp.StatusCode)
	assert.Equal(flow.setupURL()+"?step=done", resp.Header.Get("Location"))
	assert.Equal("c", <-flow.codeCh)

	// Once the callback consumed the flow, re-serving the manifest
	// would let a refreshed create tab auto-submit again and register
	// a second app nothing records.
	resp, err = http.Get(flow.setupURL() + "flow.json")
	require.NoError(err)
	_ = resp.Body.Close()
	assert.Equal(http.StatusNotFound, resp.StatusCode,
		"flow.json must die once the callback consumed the flow")
}

func TestWritePrivateKeyRejectsHostileSlugs(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	// The slug comes from the manifest conversion response; a
	// compromised GHES host must not steer the key write outside the
	// config directory.
	for _, slug := range []string{
		"../evil", "a/b", "a\\b", "..", ".", "", "a..b/../c", "-leading", "trailing-",
	} {
		_, err := writePrivateKey(configPath, "github.com", slug, "pem")
		require.Error(err, "slug %q must be rejected", slug)
	}
	_, err := writePrivateKey(configPath, "../up", "middleman-ok", "pem")
	require.Error(err, "hostile host must be rejected")

	path, err := writePrivateKey(configPath, "github.com", "middleman-3f9a2c", "pem-bytes")
	require.NoError(err)
	assert.True(filepath.IsAbs(path), "key path %q must be absolute", path)
	assert.Equal(filepath.Dir(configPath), filepath.Dir(path))

	// Slugs are only unique per host; same slug on another host must
	// land in a different file instead of overwriting the first key.
	other, err := writePrivateKey(configPath, "ghe.example.com", "middleman-3f9a2c", "pem-2")
	require.NoError(err)
	assert.NotEqual(path, other, "per-host keys must not collide")
}

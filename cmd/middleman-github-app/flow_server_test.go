package main

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlowServerServesEmbeddedAssetsAndFlowContract pins the loopback
// server's browser surface: embedded UI assets are reachable at the
// root, and /flow.json is the manifest hand-off contract that only
// exists once a create flow has been prepared.
func TestFlowServerServesEmbeddedAssetsAndFlowContract(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	flow, err := newFlowServer(io.Discard)
	require.NoError(err)
	t.Cleanup(flow.Close)

	// Embedded dist files are served from the root. Go test builds
	// embed only the committed stub; `make build` swaps in the real
	// Svelte output at the same paths.
	resp, err := http.Get(flow.localBase + "/stub.html")
	require.NoError(err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(err)
	assert.Equal(http.StatusOK, resp.StatusCode)
	assert.Equal("ok\n", string(body))

	// Before a flow is prepared there is nothing to hand to GitHub.
	resp, err = http.Get(flow.localBase + "/flow.json")
	require.NoError(err)
	_ = resp.Body.Close()
	assert.Equal(http.StatusNotFound, resp.StatusCode)

	flow.setFlow("https://example.test/settings/apps/new?state=s", `{"name":"x"}`, "x", "github.com")
	resp, err = http.Get(flow.localBase + "/flow.json")
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

	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err = noRedirect.Get(flow.localBase + flow.callbackPath + "?code=c&state=" + flow.state)
	require.NoError(err)
	_ = resp.Body.Close()
	assert.Equal(http.StatusFound, resp.StatusCode)
	assert.Equal("/?step=done", resp.Header.Get("Location"))
	assert.Equal("c", <-flow.codeCh)
}

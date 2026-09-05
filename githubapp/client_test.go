package githubapp_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/platform"
)

func TestClientUsesExplicitTransportWithoutConstructionIO(t *testing.T) {
	calls := 0
	hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, "Bearer fixture-jwt", req.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":11}`)), Request: req}, nil
	})}
	client := githubapp.NewClient("github.com", hc)
	assert.Zero(t, calls)
	app, err := client.GetApp(appTestContext(t), "fixture-jwt", appTestMeter(t))
	require.NoError(t, err)
	assert.Equal(t, int64(11), app.ID)
}

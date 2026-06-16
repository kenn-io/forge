package e2etest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestSyncRoutesWithoutProviderSyncerE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	srv := server.New(database, nil, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)

	status, err := client.HTTP.GetSyncStatusWithResponse(t.Context())
	require.NoError(err)
	require.Equal(http.StatusOK, status.StatusCode(), string(status.Body))
	require.NotNil(status.JSON200)
	assert.False(status.JSON200.Running)
	assert.Nil(status.JSON200.LastRunAt)
	assert.Nil(status.JSON200.LastError)

	rates, err := client.HTTP.GetRateLimitsWithResponse(t.Context())
	require.NoError(err)
	require.Equal(http.StatusOK, rates.StatusCode(), string(rates.Body))
	require.NotNil(rates.JSON200)
	assert.Empty(rates.JSON200.Hosts)

	trigger, err := client.HTTP.TriggerSyncWithResponse(
		t.Context(),
		nil,
		func(_ context.Context, req *http.Request) error {
			req.Header.Set("Content-Type", "application/json")
			return nil
		},
	)
	require.NoError(err)
	require.Equal(http.StatusServiceUnavailable, trigger.StatusCode(), string(trigger.Body))
	require.NotNil(trigger.ApplicationproblemJSONDefault)
	assert.Equal(generated.ServiceUnavailable, trigger.ApplicationproblemJSONDefault.Code)
	require.NotNil(trigger.ApplicationproblemJSONDefault.Detail)
	assert.Equal("syncer not configured", *trigger.ApplicationproblemJSONDefault.Detail)
}

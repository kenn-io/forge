package settingstest

import (
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/testutil/servertest"
)

func TestACPSettingsPersistPartialUpdates(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath := setupTestServerWithConfig(t)
	client := servertest.SetupTestClientWithBaseURL(t, srv, "http://127.0.0.1:8091")

	initial, err := client.HTTP.GetSettingsWithResponse(t.Context())
	require.NoError(err)
	require.NotNil(initial.JSON200)
	assert.Equal(int64(13), initial.JSON200.Acp.FontSize)
	assert.Empty(initial.JSON200.Acp.FontFamily)

	for _, update := range []generated.ACPSettingsUpdate{
		{FontFamily: new(`  "MesloLGS NF", monospace  `)},
		{FontSize: new(int64(18))},
	} {
		response, err := client.HTTP.UpdateSettingsWithResponse(t.Context(), &generated.UpdateSettingsRequestOptions{
			Body: &generated.UpdateSettingsBody{Acp: &update},
		})
		require.NoError(err)
		require.Equal(http.StatusOK, response.StatusCode, string(response.Body))
	}
	persisted, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal(`"MesloLGS NF", monospace`, persisted.ACP.FontFamily)
	assert.Equal(18, persisted.ACP.FontSize)
	assert.Equal(12, persisted.Terminal.FontSize)
	assert.Empty(persisted.Terminal.FontFamily)

	before, err := os.ReadFile(cfgPath)
	require.NoError(err)
	for _, size := range []int64{0, 7, 33} {
		response, err := client.HTTP.UpdateSettingsWithResponse(t.Context(), &generated.UpdateSettingsRequestOptions{
			Body: &generated.UpdateSettingsBody{Acp: &generated.ACPSettingsUpdate{
				FontFamily: new("rejected"), FontSize: new(size),
			}},
		})
		require.Error(err)
		require.NotNil(response)
		assert.Equal(http.StatusUnprocessableEntity, response.StatusCode, string(response.Body))
	}
	after, err := os.ReadFile(cfgPath)
	require.NoError(err)
	assert.Equal(before, after)
	current, err := client.HTTP.GetSettingsWithResponse(t.Context())
	require.NoError(err)
	require.NotNil(current.JSON200)
	assert.Equal(`"MesloLGS NF", monospace`, current.JSON200.Acp.FontFamily)
	assert.Equal(int64(18), current.JSON200.Acp.FontSize)

	reset, err := client.HTTP.UpdateSettingsWithResponse(t.Context(), &generated.UpdateSettingsRequestOptions{
		Body: &generated.UpdateSettingsBody{Acp: &generated.ACPSettingsUpdate{FontFamily: new("")}},
	})
	require.NoError(err)
	require.NotNil(reset.JSON200)
	assert.Empty(reset.JSON200.Acp.FontFamily)
	assert.Equal(int64(18), reset.JSON200.Acp.FontSize)
}

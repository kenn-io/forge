package settingstest

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/testutil/servertest"
)

func TestActivityFilterSettingsPersistAndRejectInvalidSelections(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath := setupTestServerWithConfig(t)
	client := servertest.SetupTestClientWithBaseURL(t, srv, "http://127.0.0.1:8091")
	activity := generated.Activity{
		ViewMode: "flat", TimeRange: "30d", HideClosed: true,
		ItemTypes: []string{"issue"}, EventTypes: []string{},
		HideNotifications: true, HideDefaultBranch: true, RollUpCommits: true,
	}
	response, err := client.HTTP.UpdateSettingsWithResponse(t.Context(), &generated.UpdateSettingsRequestOptions{
		Body: &generated.UpdateSettingsBody{Activity: &activity},
	})
	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode, string(response.Body))
	require.NotNil(response.JSON200)
	assert.Equal([]string{"issue"}, response.JSON200.Activity.ItemTypes)
	assert.Empty(response.JSON200.Activity.EventTypes)

	persisted, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal([]string{"issue"}, persisted.Activity.ItemTypes)
	assert.Equal([]string{}, persisted.Activity.EventTypes)
	assert.Equal("30d", persisted.Activity.TimeRange)
	assert.True(persisted.Activity.HideClosed)
	assert.True(persisted.Activity.HideNotifications)
	assert.True(persisted.Activity.HideDefaultBranch)
	assert.True(persisted.Activity.RollUpCommits)

	activity.EventTypes = []string{"unknown"}
	response, err = client.HTTP.UpdateSettingsWithResponse(t.Context(), &generated.UpdateSettingsRequestOptions{
		Body: &generated.UpdateSettingsBody{Activity: &activity},
	})
	require.Error(err)
	require.NotNil(response)
	assert.Equal(http.StatusBadRequest, response.StatusCode, string(response.Body))
	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal(persisted.Activity, reloaded.Activity)
}

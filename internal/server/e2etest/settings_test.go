package e2etest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	gh "github.com/google/go-github/v84/github"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/config"
)

func doServerJSON(
	t *testing.T,
	client *http.Client,
	method, rawURL string,
	body any,
) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	var reader io.Reader = http.NoBody
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
		reader = &buf
	}
	req, err := http.NewRequestWithContext(
		t.Context(), method, rawURL, reader,
	)
	require.NoError(t, err)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func TestSettingsAPIE2EReadUpdateAndValidation(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _, cfgPath := setupTestServerWithConfig(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	getResp := doServerJSON(
		t, ts.Client(), http.MethodGet,
		ts.URL+"/api/v1/settings", nil,
	)
	defer getResp.Body.Close()
	require.Equal(http.StatusOK, getResp.StatusCode)

	var settings generated.SettingsResponse
	require.NoError(json.NewDecoder(getResp.Body).Decode(&settings))
	require.NotNil(settings.Repos)
	require.Len(*settings.Repos, 1)
	assert.Equal("acme", (*settings.Repos)[0].Owner)
	assert.Equal("threaded", settings.Activity.ViewMode)
	assert.False(settings.Activity.CollapseThreads)

	invalidResp := doServerJSON(
		t, ts.Client(), http.MethodPut,
		ts.URL+"/api/v1/settings",
		generated.UpdateSettingsRequest{
			Activity: &generated.Activity{
				ViewMode:  "kanban",
				TimeRange: "7d",
			},
		},
	)
	defer invalidResp.Body.Close()
	require.Equal(http.StatusBadRequest, invalidResp.StatusCode)

	cfgAfterInvalid, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal("threaded", cfgAfterInvalid.Activity.ViewMode)

	updateResp := doServerJSON(
		t, ts.Client(), http.MethodPut,
		ts.URL+"/api/v1/settings",
		generated.UpdateSettingsRequest{
			Activity: &generated.Activity{
				ViewMode:        "flat",
				TimeRange:       "30d",
				HideClosed:      true,
				HideBots:        true,
				CollapseThreads: true,
			},
			Terminal: &generated.Terminal{
				FontFamily:    "\"Iosevka Term\", monospace",
				FontSize:      18,
				Scrollback:    5000,
				LineHeight:    1.15,
				FontLigatures: true,
			},
		},
	)
	defer updateResp.Body.Close()
	require.Equal(http.StatusOK, updateResp.StatusCode)

	var updated generated.SettingsResponse
	require.NoError(json.NewDecoder(updateResp.Body).Decode(&updated))
	assert.True(updated.Activity.CollapseThreads)

	cfgAfterUpdate, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal("flat", cfgAfterUpdate.Activity.ViewMode)
	assert.Equal("30d", cfgAfterUpdate.Activity.TimeRange)
	assert.True(cfgAfterUpdate.Activity.HideClosed)
	assert.True(cfgAfterUpdate.Activity.HideBots)
	assert.True(cfgAfterUpdate.Activity.CollapseThreads)
	assert.Equal(
		"\"Iosevka Term\", monospace",
		cfgAfterUpdate.Terminal.FontFamily,
	)
	assert.Equal(18, cfgAfterUpdate.Terminal.FontSize)
	assert.Equal(5000, cfgAfterUpdate.Terminal.Scrollback)
	assert.InDelta(1.15, cfgAfterUpdate.Terminal.LineHeight, 0.001)
	assert.True(cfgAfterUpdate.Terminal.FontLigatures)

	reGetResp := doServerJSON(
		t, ts.Client(), http.MethodGet,
		ts.URL+"/api/v1/settings", nil,
	)
	defer reGetResp.Body.Close()
	require.Equal(http.StatusOK, reGetResp.StatusCode)
	var reGet generated.SettingsResponse
	require.NoError(json.NewDecoder(reGetResp.Body).Decode(&reGet))
	assert.True(reGet.Activity.CollapseThreads)
}

func TestRepoConfigAPIE2EAddDeleteAndErrors(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _, cfgPath := setupTestServerWithConfig(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	duplicateResp := doServerJSON(
		t, ts.Client(), http.MethodPost,
		ts.URL+"/api/v1/repos",
		map[string]string{
			"provider": "github",
			"host":     "github.com",
			"owner":    "acme",
			"name":     "widget",
		},
	)
	defer duplicateResp.Body.Close()
	require.Equal(http.StatusBadRequest, duplicateResp.StatusCode)

	addResp := doServerJSON(
		t, ts.Client(), http.MethodPost,
		ts.URL+"/api/v1/repos",
		map[string]string{
			"provider": "github",
			"host":     "github.com",
			"owner":    "other-org",
			"name":     "other-repo",
		},
	)
	defer addResp.Body.Close()
	require.Equal(http.StatusCreated, addResp.StatusCode)

	cfgAfterAdd, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfgAfterAdd.Repos, 2)
	assert.Equal("other-org", cfgAfterAdd.Repos[1].Owner)
	assert.Equal("other-repo", cfgAfterAdd.Repos[1].Name)

	missingDeleteResp := doServerJSON(
		t, ts.Client(), http.MethodDelete,
		ts.URL+"/api/v1/repo/gh/nope/missing", nil,
	)
	defer missingDeleteResp.Body.Close()
	require.Equal(http.StatusNotFound, missingDeleteResp.StatusCode)

	deleteResp := doServerJSON(
		t, ts.Client(), http.MethodDelete,
		ts.URL+"/api/v1/repo/gh/acme/widget", nil,
	)
	defer deleteResp.Body.Close()
	require.Equal(http.StatusNoContent, deleteResp.StatusCode)

	cfgAfterDelete, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(cfgAfterDelete.Repos, 1)
	assert.Equal("other-org", cfgAfterDelete.Repos[0].Owner)
}

func TestRepoConfigAPIE2ERefreshGlobAndErrors(t *testing.T) {
	assert := Assert.New(t)
	mock := &mockGH{
		listReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			return []*gh.Repository{
				{
					Name:  new("widget-one"),
					Owner: &gh.User{Login: new(owner)},
				},
				{
					Name:  new("tooling"),
					Owner: &gh.User{Login: new(owner)},
				},
			}, nil
		},
	}
	srv, _, _, syncer := setupTestServerWithConfigContentAndSyncer(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget-*"
`, mock)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	refreshPath := "/api/v1/repo/gh/acme/" +
		url.PathEscape("widget-*") + "/refresh"
	refreshResp := doServerJSON(
		t, ts.Client(), http.MethodPost,
		ts.URL+refreshPath, nil,
	)
	defer refreshResp.Body.Close()
	require.Equal(t, http.StatusOK, refreshResp.StatusCode)
	assert.True(syncer.IsTrackedRepo("acme", "widget-one"))
	assert.False(syncer.IsTrackedRepo("acme", "tooling"))

	nonGlob, _, _ := setupTestServerWithConfig(t)
	nonGlobTS := httptest.NewServer(nonGlob)
	defer nonGlobTS.Close()
	nonGlobResp := doServerJSON(
		t, nonGlobTS.Client(), http.MethodPost,
		nonGlobTS.URL+"/api/v1/repo/gh/acme/widget/refresh", nil,
	)
	defer nonGlobResp.Body.Close()
	require.Equal(t, http.StatusBadRequest, nonGlobResp.StatusCode)
}

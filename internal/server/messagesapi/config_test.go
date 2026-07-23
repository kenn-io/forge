package messagesapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
)

func TestConfigureUsesTransactionalConfigSnapshotAfterApplyConfig(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/stats":
			_, _ = w.Write([]byte(`{"total_messages":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("MSGVAULT_CONFIG_TEST_KEY", "secret")

	initial := &config.Config{Host: "127.0.0.1"}
	reloaded := &config.Config{Host: "localhost"}
	var saved *config.Msgvault
	var runtimeSnapshot *config.Config
	h := New(Deps{
		Config: initial,
		SaveConfig: func(next *config.Msgvault) (*config.Config, error) {
			saved = CloneConfig(next)
			out := *reloaded
			out.Msgvault = CloneConfig(next)
			return &out, nil
		},
		UpdateRuntimeStripEnv: func(cfg *config.Config) {
			runtimeSnapshot = cfg
		},
	})
	h.ApplyConfig(reloaded)

	out, err := h.configure(context.Background(), &configureMsgvaultInput{
		ContentType: "application/json",
		RawBody:     []byte(`{"url":"` + upstream.URL + `","api_key_env":"MSGVAULT_CONFIG_TEST_KEY"}`),
	})

	require.NoError(err)
	require.NotNil(out)
	assert.Equal(upstream.URL, saved.URL)
	assert.Equal("MSGVAULT_CONFIG_TEST_KEY", saved.APIKeyEnv)
	require.NotNil(runtimeSnapshot)
	assert.Equal("localhost", runtimeSnapshot.Host)
	assert.Equal(saved, runtimeSnapshot.Msgvault)
}

func TestConfigureWithoutConfigStoreReturnsSettingsUnavailable(t *testing.T) {
	h := New(Deps{Config: &config.Config{}})

	_, err := h.configure(context.Background(), &configureMsgvaultInput{
		ContentType: "application/json",
		RawBody:     []byte(`{"url":"http://127.0.0.1:9","api_key_env":"MSGVAULT_CONFIG_TEST_KEY"}`),
	})

	require.Error(t, err)
}

package syncevents

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/providerplane"
)

func TestFederationKeepsRepositoryChanges(t *testing.T) {
	hub := NewEventHub()
	defer hub.Close()
	h := &Handlers{Hub: &hub, FederationEnabled: func() bool { return true }}
	events, _ := hub.Subscribe(t.Context(), false)
	err := h.ReceiveHubEvent(t.Context(), providerplane.Event{Type: "data_changed", Data: []byte(`{"repositories":[{"provider":"github","platform_host":"github.com","repo_path":"example/project","platform_repo_id":42}]}`)})
	require.NoError(t, err)
	data, err := json.Marshal((<-events).Event.Data)
	require.NoError(t, err)
	require.JSONEq(t, `{"repositories":[{"provider":"github","platform_host":"github.com","repo_path":"example/project","platform_repo_id":42}]}`, string(data))
}

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
	payload := `{"repositories":[{"provider":"github","platform_host":"github.com","repo_path":"example/first","platform_repo_id":41}]}`
	err := h.ReceiveHubEvent(t.Context(), providerplane.Event{Type: "data_changed", Data: []byte(payload)})
	require.NoError(t, err)
	data, err := json.Marshal((<-events).Event.Data)
	require.NoError(t, err)
	require.JSONEq(t, payload, string(data))
}

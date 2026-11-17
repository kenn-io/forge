package syncevents

import (
	"encoding/json/v2"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/providerplane"
)

func TestFederationKeepsRepositoryChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewEventHub()
		defer hub.Close()
		h := &Handlers{Hub: &hub, FederationEnabled: func() bool { return true }}
		events, _ := hub.Subscribe(t.Context(), false)
		err := h.ReceiveHubEvent(t.Context(), providerplane.Event{Type: "data_changed", Data: []byte(`{"repositories":[{"provider":"github","platform_host":"github.com","repo_path":"example/first","platform_repo_id":41}]}`)})
		require.NoError(t, err)
		<-events
		require.NoError(t, h.ReceiveHubEvent(t.Context(), providerplane.Event{Type: "data_changed", Data: []byte(`{"repositories":[{"provider":"github","platform_host":"github.com","repo_path":"example/a","platform_repo_id":42}]}`)}))
		time.Sleep(DataChangedInterval)
		synctest.Wait()
		data, err := json.Marshal((<-events).Event.Data)
		require.NoError(t, err)
		require.JSONEq(t, `{"repositories":[{"provider":"github","platform_host":"github.com","repo_path":"example/a","platform_repo_id":42}]}`, string(data))
	})
}

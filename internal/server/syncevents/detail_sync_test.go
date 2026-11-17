package syncevents

import (
	"context"
	"encoding/json/v2"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/platform"
)

func TestRepositoryDetailSyncBroadcastsOnlyChanges(t *testing.T) {
	hub := NewEventHub()
	t.Cleanup(hub.Close)
	inFlight := make(map[string]struct{})
	pending := make(map[string]DetailSyncJob)
	var mu sync.Mutex
	h := &Handlers{
		Hub: &hub, DetailSyncInFlight: &inFlight, DetailSyncPending: &pending, DetailSyncMu: &mu,
		RunBackground: func(fn func(context.Context)) bool { fn(t.Context()); return true },
	}
	events, _ := hub.Subscribe(t.Context(), false)
	repo := RepositoryEvent{Provider: "github", PlatformHost: "github.com", RepoPath: "example/project", Key: platform.RepositoryIDKey(42)}
	h.EnqueueRepositoryDetailSync("unchanged", nil, repo, func(context.Context) (bool, error) { return false, nil })
	select {
	case event := <-events:
		require.FailNow(t, "unchanged refresh broadcast", "%v", event)
	default:
	}
	h.EnqueueRepositoryDetailSync("changed", nil, repo, func(context.Context) (bool, error) { return true, nil })
	event := <-events
	require.Equal(t, "data_changed", event.Event.Type)
	data, err := json.Marshal(event.Event.Data)
	require.NoError(t, err)
	require.JSONEq(t, `{"repositories":[{"provider":"github","platform_host":"github.com","repo_path":"example/project","platform_repo_id":42}]}`, string(data))
	var legacy struct{}
	require.NoError(t, json.Unmarshal(data, &legacy))
	select {
	case event := <-events:
		require.FailNow(t, "extra event", "%v", event)
	default:
	}
}

func TestDataChangedCoalescesRepositoryIdentities(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewEventHub()
		defer hub.Close()
		events, _ := hub.Subscribe(t.Context(), false)
		repoA := RepositoryEvent{Provider: "github", PlatformHost: "github.com", RepoPath: "example/a", Key: platform.RepositoryIDKey(42)}
		repoB := RepositoryEvent{Provider: "gitlab", PlatformHost: "gitlab.com", RepoPath: "example/b", Key: platform.RepositoryIDKey(43)}
		hub.Broadcast(Event{Type: "data_changed", Data: RepositoryChanges{Repositories: []RepositoryEvent{repoA}}})
		<-events
		hub.Broadcast(Event{Type: "data_changed", Data: RepositoryChanges{Repositories: []RepositoryEvent{repoA}}})
		hub.Broadcast(Event{Type: "data_changed", Data: RepositoryChanges{Repositories: []RepositoryEvent{repoB}}})
		time.Sleep(DataChangedInterval)
		synctest.Wait()
		data, err := json.Marshal((<-events).Event.Data)
		require.NoError(t, err)
		require.JSONEq(t, `{"repositories":[{"provider":"github","platform_host":"github.com","repo_path":"example/a","platform_repo_id":42},{"provider":"gitlab","platform_host":"gitlab.com","repo_path":"example/b","platform_repo_id":43}]}`, string(data))
	})
}

func TestDataChangedCoalescesUnscopedEventAsFullRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewEventHub()
		defer hub.Close()
		events, _ := hub.Subscribe(t.Context(), false)
		repo := RepositoryEvent{Provider: "github", PlatformHost: "github.com", RepoPath: "example/project", Key: platform.RepositoryIDKey(42)}
		hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
		<-events
		hub.Broadcast(Event{Type: "data_changed", Data: RepositoryChanges{Repositories: []RepositoryEvent{repo}}})
		hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
		time.Sleep(DataChangedInterval)
		synctest.Wait()
		data, err := json.Marshal((<-events).Event.Data)
		require.NoError(t, err)
		require.JSONEq(t, `{}`, string(data))
	})
}

func TestFederationKeepsRepositoryChangesWhileCoalescing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewEventHub()
		defer hub.Close()
		h := &Handlers{Hub: &hub, FederationEnabled: func() bool { return true }}
		events, _ := hub.Subscribe(t.Context(), false)
		hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
		<-events
		err := h.ReceiveHubEvent(t.Context(), providerplane.Event{Type: "data_changed", Data: []byte(`{"repositories":[{"provider":"github","platform_host":"github.com","repo_path":"example/project","platform_repo_id":42}]}`)})
		require.NoError(t, err)
		time.Sleep(DataChangedInterval)
		synctest.Wait()
		data, err := json.Marshal((<-events).Event.Data)
		require.NoError(t, err)
		require.JSONEq(t, `{"repositories":[{"provider":"github","platform_host":"github.com","repo_path":"example/project","platform_repo_id":42}]}`, string(data))
	})
}

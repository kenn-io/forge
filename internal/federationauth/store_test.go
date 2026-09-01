package federationauth

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testHubNodeID    = "0123456789abcdef0123456789abcdef"
	testMemberNodeID = "fedcba9876543210fedcba9876543210"
)

func TestStorePersistsInboundDigestAndRevokesSynchronously(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	path := filepath.Join(t.TempDir(), "federation-credentials.json")
	store, err := Open(path)
	require.NoError(err)

	token, err := store.MintInbound(testMemberNodeID, []Scope{ScopeSnapshotRead})
	require.NoError(err)
	assert.Regexp(`^[0-9a-f]{64}$`, token)

	raw, err := os.ReadFile(store.Path())
	require.NoError(err)
	assert.NotContains(string(raw), token)
	info, err := os.Stat(store.Path())
	require.NoError(err)
	assert.Equal(fs.FileMode(0o600), info.Mode().Perm())

	principal, ok := store.Authenticate(token)
	require.True(ok)
	assert.Equal(testMemberNodeID, principal.NodeID)
	assert.True(principal.Has(ScopeSnapshotRead))

	require.NoError(store.RevokeInbound(token))
	_, ok = store.Authenticate(token)
	assert.False(ok)

	reopened, err := Open(path)
	require.NoError(err)
	_, ok = reopened.Authenticate(token)
	assert.False(ok)
}

func TestStoreKeepsOutboundCredentialsReadable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store, err := Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)

	token, err := store.MintOutbound(testHubNodeID, SpokeToHubScopes())
	require.NoError(err)
	credential, ok := store.Outbound(testHubNodeID)
	require.True(ok)
	assert.Equal(token, credential.Token)
	assert.Equal(testHubNodeID, credential.NodeID)
	assert.Equal(SpokeToHubScopes(), credential.Scopes)

	raw, err := os.ReadFile(store.Path())
	require.NoError(err)
	assert.Contains(string(raw), token)

	reopened, err := Open(store.Path())
	require.NoError(err)
	credential, ok = reopened.Outbound(testHubNodeID)
	require.True(ok)
	assert.Equal(token, credential.Token)
}

func TestStoreBindsDigestOnlyPendingInboundCredential(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store, err := Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	const reservationID = "11111111111111111111111111111111"

	token, err := store.ReserveInbound(reservationID, HubToSpokeScopes())
	require.NoError(err)
	raw, err := os.ReadFile(store.Path())
	require.NoError(err)
	assert.NotContains(string(raw), token)
	_, ok := store.Authenticate(token)
	assert.False(ok, "a pending credential has no authenticated subject")

	require.NoError(store.BindInbound(reservationID, testHubNodeID))
	principal, ok := store.Authenticate(token)
	require.True(ok)
	assert.Equal(testHubNodeID, principal.NodeID)

	reopened, err := Open(store.Path())
	require.NoError(err)
	principal, ok = reopened.Authenticate(token)
	require.True(ok)
	assert.Equal(testHubNodeID, principal.NodeID)
}

func TestDirectionalGrantsAreClosedAndNonOverlapping(t *testing.T) {
	assert := assert.New(t)

	hubToSpoke := scopeSet(HubToSpokeScopes())
	assert.Equal(map[Scope]struct{}{
		ScopeSnapshotRead: {}, ScopeWorkspaceRead: {}, ScopeWorkspaceWrite: {},
		ScopeTerminalAttach: {}, ScopeEnrollmentActivate: {},
	}, hubToSpoke)
	assert.NotContains(hubToSpoke, ScopeProviderRead)
	assert.NotContains(hubToSpoke, ScopeProviderWrite)
	assert.NotContains(hubToSpoke, ScopeEventsRead)

	spokeToHub := scopeSet(SpokeToHubScopes())
	assert.Equal(map[Scope]struct{}{
		ScopeSnapshotRead: {}, ScopeProviderRead: {}, ScopeProviderWrite: {},
		ScopeEventsRead: {}, ScopeEnrollmentActivate: {},
	}, spokeToHub)
	assert.NotContains(spokeToHub, ScopeWorkspaceRead)
	assert.NotContains(spokeToHub, ScopeWorkspaceWrite)
	assert.NotContains(spokeToHub, ScopeTerminalAttach)
}

func TestStoreRejectsInvalidSubjectsAndUnknownScopes(t *testing.T) {
	require := require.New(t)
	store, err := Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)

	_, err = store.MintInbound("spoke-a", []Scope{ScopeSnapshotRead})
	require.ErrorContains(err, "node ID")
	_, err = store.MintInbound(testMemberNodeID, []Scope{"everything"})
	require.ErrorContains(err, "scope")

	raw := []byte(`{
  "version": 1,
  "inbound": [{
    "node_id": "fedcba9876543210fedcba9876543210",
    "token_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "scopes": ["everything"]
  }]
}`)
	badPath := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(os.WriteFile(badPath, raw, 0o600))
	_, err = Open(badPath)
	require.ErrorContains(err, "scope")
}

func TestStoreConcurrentAuthenticationUsesImmutableSnapshots(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store, err := Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(err)
	token, err := store.MintInbound(testMemberNodeID, HubToSpokeScopes())
	require.NoError(err)

	const readers = 24
	start := make(chan struct{})
	stop := make(chan struct{})
	observed := make(chan bool, readers)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(readers)
	done.Add(readers)
	for range readers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			first := true
			for {
				select {
				case <-stop:
					return
				default:
					principal, ok := store.Authenticate(token)
					if first {
						observed <- ok
						first = false
					}
					if ok {
						principal.Scopes[ScopeProviderWrite] = struct{}{}
					}
				}
			}
		}()
	}
	ready.Wait()
	close(start)
	for range readers {
		assert.True(<-observed)
	}
	require.NoError(store.RevokeInbound(token))
	close(stop)
	done.Wait()

	_, ok := store.Authenticate(token)
	assert.False(ok)

	raw, err := os.ReadFile(store.Path())
	require.NoError(err)
	var persisted map[string]any
	require.NoError(json.Unmarshal(raw, &persisted))
	assert.Empty(persisted["inbound"])
}

func scopeSet(scopes []Scope) map[Scope]struct{} {
	result := make(map[Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		result[scope] = struct{}{}
	}
	return result
}

package fleet

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/federation"
)

func TestRawSnapshotCarriesDetachedLocalWorkspacesOnly(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	raw := RawSnapshot{
		ProtocolVersion: federation.ProtocolVersion,
		NodeID:          NodeID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Host:            RawHost{Hostname: "studio", Platform: "linux"},
		Projects:        []RawProject{{ScopedKey: "repo:/srv/app", Name: "app", RootPath: "/srv/app"}},
		Worktrees:       []RawWorktree{{ScopedKey: "worktree:/srv/app", ProjectKey: "repo:/srv/app", Path: "/srv/app", IsPrimary: true}},
		Sessions:        []RawSession{{ScopedKey: "session:app-main", WorktreeKey: "worktree:/srv/app", Status: "running"}},
		Workspaces: []RawWorkspace{{
			ID: "ws-local", Status: "ready", ItemType: "pull_request", ItemNumber: 42,
			Repository: RepositoryIdentity{
				Provider: "github", PlatformHost: "github.com", PlatformRepoID: "R_1",
			},
		}},
	}
	b, err := json.Marshal(raw)
	require.NoError(err)
	assert.Contains(string(b), `"protocolVersion":`+strconv.Itoa(federation.ProtocolVersion))
	assert.NotContains(string(b), "schemaVersion")
	assert.NotContains(string(b), "repoID")
	assert.NotContains(string(b), "remoteHosts")
	var back RawSnapshot
	require.NoError(json.Unmarshal(b, &back))
	require.Equal(federation.ProtocolVersion, back.ProtocolVersion)
	require.Equal(raw.NodeID, back.NodeID)
	require.Len(back.Projects, 1, "round-trip projects")
	require.Len(back.Workspaces, 1, "round-trip workspaces")
}

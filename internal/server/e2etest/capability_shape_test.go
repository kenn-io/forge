package e2etest

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSnapshotCommandCapabilityShape pins the wire shape of the
// capability block fleet clients consume: exactly the eight flags the
// operation-availability policy reads, nothing more. A removed or
// renamed flag changes what every fleet client decodes, so the
// contract is asserted on real response bytes.
func TestSnapshotCommandCapabilityShape(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	ts, _ := bootFleetServer(t, nil)

	var snapshot struct {
		Hosts []struct {
			Kind         string `json:"kind"`
			Capabilities *struct {
				Commands map[string]bool `json:"commands"`
			} `json:"capabilities"`
		} `json:"hosts"`
	}
	getJSON(t, ts, "/api/v1/snapshot", &snapshot)

	require.NotEmpty(snapshot.Hosts)
	local := snapshot.Hosts[0]
	require.Equal("self", local.Kind)
	require.NotNil(local.Capabilities, "local host must advertise capabilities")

	keys := make([]string, 0, len(local.Capabilities.Commands))
	for k := range local.Capabilities.Commands {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal([]string{
		"projectAdd",
		"projectRemove",
		"repositoryClone",
		"sessionEnsure",
		"sessionKill",
		"worktreeCreate",
		"worktreeDelete",
		"worktreeImportPullRequest",
	}, keys, "capability commands must carry exactly the eight availability-gating flags")
}

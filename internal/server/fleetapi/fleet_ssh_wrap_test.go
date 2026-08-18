package fleetapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

// TestWrapAttachSpecForSSHQuotesRemoteCommand pins remote-argument
// quoting: OpenSSH joins remote arguments with spaces and hands the
// result to the remote shell, so attach-spec arguments containing
// whitespace or metacharacters must collapse into one shell-quoted
// remote command or the remote shell re-splits and interprets them.
func TestWrapAttachSpecForSSHQuotesRemoteCommand(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	spec := workspaceapi.RuntimeAttachSpecResponse{
		Version:     1,
		Kind:        "tmux",
		TmuxSession: "mm-s1",
		Command: []string{
			"env", "-u", "TMUX", "TMUX_TMPDIR=/socket dir/with'quote",
			"tmux", "-u", "attach-session", "-E", "-t", "mm-s1",
		},
	}
	body, err := json.Marshal(spec)
	require.NoError(err)

	wrapped, ok := wrapAttachSpecForSSH(body, []string{"ssh", "-t", "peer"})
	require.True(ok)
	var got workspaceapi.RuntimeAttachSpecResponse
	require.NoError(json.Unmarshal(wrapped, &got))

	require.Len(got.Command, 4,
		"the remote command must collapse into a single quoted ssh argument")
	assert.Equal([]string{"ssh", "-t", "peer"}, got.Command[:3])
	remote := got.Command[3]
	assert.Contains(remote, `'TMUX_TMPDIR=/socket dir/with'\''quote'`,
		"metacharacter values must reach the remote shell quoted")
	assert.Contains(remote, "attach-session")
	assert.False(got.RequiresLocalHost)
}

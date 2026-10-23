package mcpserver

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShowMediaReturnsChatDescriptorForLocalMedia(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	recording := filepath.Join(root, "recording")
	require.NoError(os.WriteFile(recording, []byte{0x1a, 0x45, 0xdf, 0xa3, 0x9f, 0x42, 0x86, 0x81, 0x01}, 0o600))
	notes := filepath.Join(root, "notes.txt")
	require.NoError(os.WriteFile(notes, []byte("not media"), 0o600))
	session := connectMCPTestSession(t, newMCPTestServer(t, &fakeBackend{}))

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "kenn_forge_show_media", Arguments: map[string]any{"path": recording, "title": "Demo"},
	})
	require.NoError(err)
	require.False(result.IsError)
	require.Len(result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(ok)
	var descriptor MediaDescriptor
	require.NoError(json.Unmarshal([]byte(text.Text), &descriptor))
	assert.Equal(MediaDescriptor{Kind: MediaDescriptorKind, Path: recording, Title: "Demo", MimeType: "video/webm"}, descriptor)
	assert.Equal(MediaDescriptorKind, result.StructuredContent.(map[string]any)["kind"])

	for _, path := range []string{notes, "recording.webm", filepath.Join(root, "missing.mp4"), "/dev/zero"} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
			Name: "kenn_forge_show_media", Arguments: map[string]any{"path": path},
		})
		require.NoError(err)
		assert.True(result.IsError, path)
	}
}

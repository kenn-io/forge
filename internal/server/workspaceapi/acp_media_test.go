package workspaceapi

import (
	"encoding/base64"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestACPMediaPreviewInlinesLocalVideoAndAudio(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	root := t.TempDir()
	webm := []byte{0x1a, 0x45, 0xdf, 0xa3, 0x9f, 0x42, 0x86, 0x81, 0x01}
	mp4 := append([]byte{0, 0, 0, 0x18}, []byte("ftypmp42\x00\x00\x00\x00mp42isom")...)
	wav := []byte("RIFF\x24\x00\x00\x00WAVEfmt ")
	write := func(name string, data []byte) string {
		path := filepath.Join(root, name)
		require.NoError(os.WriteFile(path, data, 0o600))
		return path
	}
	recording := write("recording.webm", webm)
	demo := write("before-after.mp4", mp4)
	voice := write("note.wav", wav)
	descriptor := func(path, title string) string {
		data, err := json.Marshal(mcpserver.MediaDescriptor{Kind: mcpserver.MediaDescriptorKind, Path: path, Title: title, MimeType: "video/mp4"})
		require.NoError(err)
		return string(data)
	}

	linked := &localruntime.ACPContent{Type: "resource_link", URI: recording}
	require.True(acpContentMediaPreview(linked))
	assert.Equal("resource", linked.Type)
	assert.Equal("video/webm", linked.MimeType)
	assert.Equal(base64.StdEncoding.EncodeToString(webm), linked.Data)

	audio := &localruntime.ACPContent{Type: "resource_link", URI: voice}
	require.True(acpContentMediaPreview(audio))
	assert.Equal("audio", audio.Type)
	assert.True(strings.HasPrefix(audio.MimeType, "audio/"), audio.MimeType)

	// The text descriptor is the only way a Claude agent's tool result can
	// reference a file.
	shown := &localruntime.ACPContent{Type: "text", Text: descriptor(demo, "Before and after")}
	require.True(acpContentMediaPreview(shown))
	assert.Equal("resource", shown.Type)
	assert.Equal("video/mp4", shown.MimeType)
	assert.Equal(demo, shown.URI)
	assert.Equal("Before and after", shown.Title)
	assert.Equal(base64.StdEncoding.EncodeToString(mp4), shown.Data)

	missing := filepath.Join(root, "deleted.mp4")
	gone := &localruntime.ACPContent{Type: "text", Text: descriptor(missing, "Demo")}
	require.True(acpContentMediaPreview(gone))
	assert.Equal(localruntime.ACPContent{Type: "resource_link", URI: missing, Name: "deleted.mp4", Title: "Demo"}, *gone)

	for _, text := range []string{`{"kind":"other","path":"` + demo + `"}`, "plain text", descriptor("relative.mp4", "")} {
		content := &localruntime.ACPContent{Type: "text", Text: text}
		assert.False(acpContentMediaPreview(content), text)
		assert.Equal(text, content.Text)
	}
}

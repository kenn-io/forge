//go:build unix

package workspaceapi

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestACPImagePreviewFollowsSymlinksAndSkipsSpecialFiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	root := t.TempDir()
	image := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`)
	target := filepath.Join(root, "target.svg")
	require.NoError(os.WriteFile(target, image, 0o600))
	link := filepath.Join(root, "link.svg")
	require.NoError(os.Symlink(target, link))
	fifo := filepath.Join(root, "pipe.png")
	require.NoError(syscall.Mkfifo(fifo, 0o600))

	linked := &localruntime.ACPContent{Type: "resource_link", URI: link}
	require.True(acpContentImagePreview(linked))
	assert.Equal("image", linked.Type)
	assert.Equal(base64.StdEncoding.EncodeToString(image), linked.Data)

	for _, path := range []string{fifo, "/dev/zero"} {
		content := &localruntime.ACPContent{Type: "resource_link", URI: path}
		done := make(chan bool, 1)
		go func() { done <- acpContentImagePreview(content) }()
		select {
		case changed := <-done:
			assert.False(changed, path)
			assert.Equal(localruntime.ACPContent{Type: "resource_link", URI: path}, *content, path)
		case <-time.After(5 * time.Second):
			require.FailNow("preview did not return", path)
		}
	}
}

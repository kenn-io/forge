package web

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssetsIncludesStubFile(t *testing.T) {
	assets, err := Assets()
	require.NoError(t, err)

	content, err := fs.ReadFile(assets, "stub.html")
	require.NoError(t, err)
	assert.Equal(t, "ok\n", string(content))
}

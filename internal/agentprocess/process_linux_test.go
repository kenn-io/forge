//go:build linux

package agentprocess

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProcStatHandlesNamesWithParentheses(t *testing.T) {
	info, err := parseProcStat("42 (odd) (name) S 7 42 42 0 -1 4194304 1 0 0 0 0 0 0 0 20 0 1 0 987654 0 0")
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Equal(7, info.parent)
	assert.Equal("odd) (name", info.name)
	assert.Equal("987654", info.start)
	assert.False(info.exited)

	zombie, err := parseProcStat("42 (claude) Z 7 42 42 0 -1 4194304 1 0 0 0 0 0 0 0 20 0 1 0 987654 0 0")
	require.NoError(t, err)
	assert.True(zombie.exited)

	_, err = parseProcStat("42 (claude) S 7")
	assert.Error(err)
}

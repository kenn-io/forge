package procutil

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsResourceExhausted(t *testing.T) {
	assert := assert.New(t)

	assert.True(IsResourceExhausted(syscall.EAGAIN))
	assert.True(IsResourceExhausted(fmt.Errorf("wrap: %w", syscall.EAGAIN)))
	assert.True(IsResourceExhausted(errors.New("forkpty: Resource temporarily unavailable")))
	assert.True(IsResourceExhausted(errors.New("zsh: fork failed: resource temporarily unavailable")))
	assert.False(IsResourceExhausted(errors.New("permission denied")))
	assert.False(IsResourceExhausted(nil))
}

func TestWrapResourceExhaustion(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	err := WrapResourceExhaustion(
		errors.New("forkpty: Resource temporarily unavailable"),
		"attach workspace terminal",
	)
	require.ErrorIs(err, ErrProcessLimitReached)
	assert.Contains(err.Error(), "attach workspace terminal")
	assert.Contains(err.Error(), "forkpty")

	other := errors.New("permission denied")
	assert.Same(other, WrapResourceExhaustion(other, "git fetch"))
	require.NoError(WrapResourceExhaustion(nil, "git fetch"))
}

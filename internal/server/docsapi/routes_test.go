package docsapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPublishLockSetReleasesAndScopesPerFolder(t *testing.T) {
	assert := assert.New(t)
	locks := NewPublishLockSet()

	assert.True(locks.TryAcquire("a"))
	assert.False(locks.TryAcquire("a"))
	assert.True(locks.TryAcquire("b"))
	locks.Release("a")
	assert.True(locks.TryAcquire("a"))
	locks.Release("a")
	locks.Release("b")
}

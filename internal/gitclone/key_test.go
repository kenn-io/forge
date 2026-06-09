package gitclone

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEnsureCloneKey(t *testing.T) {
	assert := assert.New(t)

	a := ensureCloneKey("", "github.com", "acme", "widget")
	b := ensureCloneKey("", "github.com", "acme", "widget")
	assert.Equal(a, b, "same identity must hash to the same key")

	assert.NotEqual(a, ensureCloneKey("gitlab", "github.com", "acme", "widget"))
	assert.NotEqual(a, ensureCloneKey("", "gitlab.com", "acme", "widget"))
	assert.NotEqual(a, ensureCloneKey("", "github.com", "other", "widget"))
	assert.NotEqual(a, ensureCloneKey("", "github.com", "acme", "other"))

	// Pathological owner/name combinations must not collide with each
	// other after concatenation. Without the null separator, owner=foo
	// name=barbaz would alias owner=foobar name=baz.
	x := ensureCloneKey("", "github.com", "foo", "barbaz")
	y := ensureCloneKey("", "github.com", "foobar", "baz")
	assert.NotEqual(x, y, "key must not be collision-prone on concat")
}

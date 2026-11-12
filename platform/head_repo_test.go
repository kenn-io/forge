package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCloneRepoIdentity(t *testing.T) {
	assert := assert.New(t)

	assert.Equal(
		"github/github.com/fork/widget",
		CloneRepoIdentity("github", "", " git@GitHub.com:Fork/Widget.git "),
	)
	assert.Equal(
		"github/github.com/fork/widget",
		CloneRepoIdentity("github", "", "https://token@github.com/Fork/Widget/"),
	)
	assert.Equal(
		"github/github.com/fork/widget",
		CloneRepoIdentity("github", "", "ssh://git@github.com:22/Fork/Widget.git"),
	)
	assert.Equal(
		"github/ghe.example.com:8443/fork/widget",
		CloneRepoIdentity("github", "", "https://ghe.example.com:8443/Fork/Widget.git"),
	)
	assert.Equal(
		"gitlab/gitlab.com/group/subgroup/project",
		CloneRepoIdentity("gitlab", "", "https://gitlab.com/Group/Subgroup/Project.git"),
	)
	assert.Equal(
		"gitlab/gitlab.com/group/subgroup/project",
		CloneRepoIdentity("gitlab", "", "git@gitlab.com:Group/Subgroup/Project.git"),
	)
	assert.NotEqual(
		CloneRepoIdentity("github", "", "https://forge.example/acme/widget.git"),
		CloneRepoIdentity("gitlab", "", "https://forge.example/acme/widget.git"),
	)
	assert.Empty(CloneRepoIdentity("github", "", "/tmp/workspace/remote.git"))
	assert.Empty(CloneRepoIdentity("github", "", "not a clone url"))
}

func TestPlatformHostIdentity(t *testing.T) {
	assert := assert.New(t)

	assert.Equal("ghe.example.com:8443", PlatformHostIdentity("GHE.example.com:8443"))
	assert.Equal("ghe.example.com", PlatformHostIdentity("ghe.example.com:443"))
	assert.Equal("ghe.example.com", PlatformHostIdentity("ghe.example.com"))
}

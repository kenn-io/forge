package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

func TestPlatformRepoRefFromDBRestoresNumericPlatformID(t *testing.T) {
	assert := assert.New(t)

	ref := platformRepoRefFromDB(db.Repo{
		Platform:       string(platform.KindGitLab),
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "4242",
		Owner:          "group",
		Name:           "project",
		RepoPath:       "group/project",
	})

	assert.Equal(platform.KindGitLab, ref.Platform)
	assert.Equal("gitlab.example.com", ref.Host)
	assert.Equal("group/project", ref.RepoPath)
	assert.Equal(int64(4242), ref.PlatformID)
	assert.Equal("4242", ref.PlatformExternalID)
}

func TestPlatformRepoRefFromDBPreservesNonNumericExternalID(t *testing.T) {
	assert := assert.New(t)

	ref := platformRepoRefFromDB(db.Repo{
		Platform:       string(platform.KindGitLab),
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "gid://gitlab/Project/4242",
		Owner:          "group",
		Name:           "project",
		RepoPath:       "group/project",
	})

	assert.Zero(ref.PlatformID)
	assert.Equal("gid://gitlab/Project/4242", ref.PlatformExternalID)
}

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.kenn.io/middleman/internal/db"
)

func TestParseRepoFiltersAcceptsProviderQualifiedRepoPath(t *testing.T) {
	assert.Equal(t, []db.RepoFilter{{
		Platform:     "gitea",
		PlatformHost: "github.com",
		RepoPath:     "acme/widgets",
	}}, parseRepoFilters("Gitea|github.com/acme/widgets"))
}

func TestParseRepoFiltersRejectsUnqualifiedRepoPath(t *testing.T) {
	assert.Empty(t, parseRepoFilters("gitea/acme/team/widgets"))
	assert.Empty(t, parseRepoFilters("acme/widgets"))
}

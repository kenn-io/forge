package server

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/workspaceapi"
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

func TestWorkspaceRefForActivityItemUsesStableRepositoryIdentity(t *testing.T) {
	issueKey := db.WorkspaceSubjectKey{
		RepoID: 41, ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 7,
	}
	pullKey := db.WorkspaceSubjectKey{
		RepoID: 41, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 8,
	}
	snapshot := workspaceapi.WorkspaceSubjectSnapshot{
		OwnReferences: map[db.WorkspaceSubjectKey]workspaceapi.WorkspaceRef{
			issueKey: {ID: "ws-issue", Status: "ready"},
		},
		Subjects: map[db.WorkspaceSubjectKey]workspaceapi.SubjectActivity{
			pullKey: {Workspace: workspaceapi.WorkspaceRef{ID: "ws-associated", Status: "ready"}},
		},
	}

	renamedIssue := db.ActivityItem{
		RepoID: 41, RepoOwner: "acme", RepoName: "renamed",
		ItemType: "issue", ItemNumber: 7,
	}
	assert.Equal(t,
		&workspaceapi.WorkspaceRef{ID: "ws-issue", Status: "ready"},
		workspaceRefForActivityItem(snapshot, renamedIssue),
	)

	associatedPull := db.ActivityItem{
		RepoID: 41, RepoOwner: "acme", RepoName: "renamed",
		ItemType: "pr", ItemNumber: 8,
	}
	assert.Equal(t,
		&workspaceapi.WorkspaceRef{ID: "ws-associated", Status: "ready"},
		workspaceRefForActivityItem(snapshot, associatedPull),
	)

	reusedRoute := renamedIssue
	reusedRoute.RepoID = 42
	assert.Nil(t, workspaceRefForActivityItem(snapshot, reusedRoute),
		"a replacement repository at the same route must not inherit the old workspace")
}

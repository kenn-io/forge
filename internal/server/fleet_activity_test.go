package server

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

func TestFleetActivityWorkspaceMatching(t *testing.T) {
	repo := activityRepoRefResponse{
		Provider: "gitlab", PlatformHost: "git.example.test", PlatformRepoID: "42",
		Owner: "acme", Name: "renamed",
	}
	workspace := fleet.WorkspaceSummary{
		ID: "remote", Status: "ready", FleetHostKey: "spoke", Visible: true,
		SourceItemVisible: true, ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 7,
		AssociatedPRNumber: new(8),
		Repo: fleet.WorkspaceRepositorySummary{
			Provider: repo.Provider, PlatformHost: repo.PlatformHost, PlatformRepoID: repo.PlatformRepoID,
			Owner: "acme", Name: "original",
		},
	}
	remote := &workspaceapi.WorkspaceRef{ID: "remote", Status: "ready"}
	local := &workspaceapi.WorkspaceRef{ID: "local", Status: "creating"}
	for _, tc := range []struct {
		name     string
		repo     activityRepoRefResponse
		itemType string
		number   int
		local    *workspaceapi.WorkspaceRef
		want     *workspaceapi.WorkspaceRef
	}{
		{"issue after rename", repo, "issue", 7, nil, remote},
		{"associated pull", repo, "pr", 8, nil, remote},
		{"different item type", repo, "pr", 7, nil, nil},
		{"different number", repo, "issue", 9, nil, nil},
		{"local takes precedence", repo, "issue", 7, local, local},
		{"reused route", activityRepoRefResponse{Provider: repo.Provider, PlatformHost: repo.PlatformHost, PlatformRepoID: "43", Owner: repo.Owner, Name: repo.Name}, "issue", 7, nil, nil},
		{"different provider", activityRepoRefResponse{Provider: "gitea", PlatformHost: repo.PlatformHost, PlatformRepoID: "42"}, "issue", 7, nil, nil},
		{"different host", activityRepoRefResponse{Provider: repo.Provider, PlatformHost: "other.example.test", PlatformRepoID: "42"}, "issue", 7, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := activityResponse{
				Items:        []activityItemResponse{{Repo: tc.repo, ItemType: tc.itemType, ItemNumber: tc.number, Workspace: tc.local}},
				ItemActivity: []activitySubjectResponse{{Repo: tc.repo, ItemType: tc.itemType, ItemNumber: tc.number, Workspace: tc.local}},
			}
			overlayFleetActivityWorkspaces(&response, []fleet.WorkspaceSummary{workspace})
			assert.Equal(t, tc.want, response.Items[0].Workspace)
			assert.Equal(t, tc.want, response.ItemActivity[0].Workspace)
		})
	}
}

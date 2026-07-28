package workspaceapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/middleman/internal/db"
)

func TestMRHeadRepoKind(t *testing.T) {
	t.Parallel()
	unknown := ""
	fork := "https://example.com/attacker/repo.git"

	tests := []struct {
		name       string
		itemType   string
		mrHeadRepo *string
		want       string
	}{
		{
			name:       "pull request same repo",
			itemType:   db.WorkspaceItemTypePullRequest,
			mrHeadRepo: nil,
			want:       mrHeadRepoKindSameRepo,
		},
		{
			name:       "pull request fork",
			itemType:   db.WorkspaceItemTypePullRequest,
			mrHeadRepo: &fork,
			want:       mrHeadRepoKindFork,
		},
		{
			name:       "pull request unknown repo identity",
			itemType:   db.WorkspaceItemTypePullRequest,
			mrHeadRepo: &unknown,
			want:       mrHeadRepoKindUnknown,
		},
		{
			name:       "non pull request workspace is unset",
			itemType:   db.WorkspaceItemTypeIssue,
			mrHeadRepo: nil,
			want:       "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, mrHeadRepoKind(tt.itemType, tt.mrHeadRepo))
		})
	}
}

func TestToWorkspaceResponseSetsMRHeadRepoKind(t *testing.T) {
	t.Parallel()
	fork := "https://example.com/attacker/repo.git"

	tests := []struct {
		name       string
		itemType   string
		mrHeadRepo *string
		want       string
	}{
		{
			name:       "same repo pull request",
			itemType:   db.WorkspaceItemTypePullRequest,
			mrHeadRepo: nil,
			want:       mrHeadRepoKindSameRepo,
		},
		{
			name:       "fork pull request",
			itemType:   db.WorkspaceItemTypePullRequest,
			mrHeadRepo: &fork,
			want:       mrHeadRepoKindFork,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			summary := &db.WorkspaceSummary{
				Workspace: db.Workspace{
					ID:         "ws-1",
					ItemType:   tt.itemType,
					MRHeadRepo: tt.mrHeadRepo,
				},
			}
			got := toWorkspaceResponse(summary)
			assert.Equal(t, tt.want, got.MRHeadRepoKind)
		})
	}
}

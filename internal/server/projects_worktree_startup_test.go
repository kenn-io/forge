package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/serverfake"
)

func TestProjectsCoordinatorStartup(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	for _, configured := range []bool{false, true} {
		name := "no item workspaces"
		if configured {
			name = "item workspaces configured"
		}
		t.Run(name, func(t *testing.T) {
			require := require.New(t)
			root := ""
			if configured {
				root = t.TempDir()
			}
			srv, database, _ := setupTestServerWithReposAndOptions(t, &serverfake.MockGH{}, nil, ServerOptions{WorktreeDir: root})
			repo := gitfixture.NewRepository(t, false)
			project, err := database.CreateProject(t.Context(), db.CreateProjectInput{DisplayName: "Example", LocalPath: repo.Dir, DefaultBranch: "main"})
			require.NoError(err)
			if !configured {
				require.Nil(srv.workspaces)
			}
			server := httptest.NewServer(srv)
			t.Cleanup(server.Close)
			path := filepath.Join(t.TempDir(), "checkout")
			response := serverfake.HttpDo(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/worktrees", serverfake.MustMarshal(t, map[string]any{
				"branch": "topic", "path": path, "create_on_disk": true,
			}))
			defer response.Body.Close()
			require.Equal(http.StatusCreated, response.StatusCode)
			require.FileExists(filepath.Join(path, "seed.md"))
			rows, err := database.ListProjectWorktrees(t.Context(), project.ID)
			require.NoError(err)
			require.Len(rows, 1)
			require.Equal(path, rows[0].Path)
		})
	}
}

package workspaceapi

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestWorkspaceViewStatePersistsAcrossRequests(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	database := dbtest.Open(t)
	for i, id := range []string{"work-a", "work-b"} {
		require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
			ID: id, Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget",
			ItemType: db.WorkspaceItemTypeIssue, ItemNumber: i + 1, WorktreePath: t.TempDir(), Status: "ready",
		}))
	}

	for _, step := range []struct {
		method, id, body, wantTab string
		status                    int
	}{
		{http.MethodGet, "work-a", "", "", http.StatusOK},
		{http.MethodPut, "work-a", `{"active_tab":"session:agent-a"}`, "session:agent-a", http.StatusOK},
		{http.MethodGet, "work-a", "", "session:agent-a", http.StatusOK},
		{http.MethodGet, "work-b", "", "", http.StatusOK},
		{http.MethodPut, "work-b", `{"active_tab":"terminal"}`, "terminal", http.StatusOK},
		{http.MethodPut, "work-a", `{"active_tab":"home"}`, "home", http.StatusOK},
		{http.MethodGet, "work-b", "", "terminal", http.StatusOK},
		{http.MethodGet, "work-a", "", "home", http.StatusOK},
		{http.MethodPut, "work-a", `{"active_tab":"session:"}`, "", http.StatusBadRequest},
		{http.MethodPut, "work-a", `{"active_tab":"other"}`, "", http.StatusBadRequest},
		{http.MethodPut, "work-a", `{"active_tab":""}`, "", http.StatusBadRequest},
		{http.MethodGet, "work-a", "", "home", http.StatusOK},
		{http.MethodGet, "missing", "", "", http.StatusNotFound},
		{http.MethodPut, "missing", `{"active_tab":"home"}`, "", http.StatusNotFound},
	} {
		// Each request gets a new handler, as another browser or daemon restart would.
		mux := http.NewServeMux()
		handler := &Handler{db: database}
		handler.RegisterExecution(humago.New(mux, huma.DefaultConfig("test", "1")))
		request := httptest.NewRequestWithContext(t.Context(), step.method, "/workspaces/"+step.id+"/view-state", strings.NewReader(step.body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		require.Equal(step.status, response.Code, "%s %s: %s", step.method, step.id, response.Body.String())
		if step.status == http.StatusOK {
			var body struct {
				ActiveTab string `json:"active_tab"`
			}
			require.NoError(json.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, step.wantTab, body.ActiveTab)
		}
	}
}

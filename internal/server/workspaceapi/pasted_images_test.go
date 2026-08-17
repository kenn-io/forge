package workspaceapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
)

func TestPostPastedImageWritesDecodedBytes(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	mux, api, worktree := setupPastedImageAPI(t, "ready")
	png := []byte("\x89PNG\r\n\x1a\napi fixture")

	recorder := postPastedImage(t, mux, "ws-pasted-image-api", map[string]string{
		"data": base64.StdEncoding.EncodeToString(png),
	})

	require.Equal(http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		Path string `json:"path"`
	}
	require.NoError(json.Unmarshal(recorder.Body.Bytes(), &response))
	stored, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(response.Path)))
	require.NoError(err)
	assert.Equal(png, stored)
	_, documented := api.OpenAPI().Paths["/workspaces/{id}/pasted-images"]
	assert.False(documented)
}

func TestPostPastedImageValidatesJSONAndDecodedContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     string
		workspace  string
		data       string
		wantStatus int
		wantCode   string
	}{
		{
			name: "invalid base64", status: "ready", workspace: "ws-pasted-image-api",
			data: "%%%", wantStatus: http.StatusBadRequest, wantCode: "validationError",
		},
		{
			name: "unsupported bytes", status: "ready", workspace: "ws-pasted-image-api",
			data:       base64.StdEncoding.EncodeToString([]byte("not an image")),
			wantStatus: http.StatusBadRequest, wantCode: "validationError",
		},
		{
			name: "missing workspace", status: "ready", workspace: "missing",
			data:       base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfixture")),
			wantStatus: http.StatusNotFound, wantCode: "workspaceNotFound",
		},
		{
			name: "workspace not ready", status: "starting", workspace: "ws-pasted-image-api",
			data:       base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfixture")),
			wantStatus: http.StatusConflict, wantCode: "conflict",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mux, _, _ := setupPastedImageAPI(t, tt.status)
			recorder := postPastedImage(t, mux, tt.workspace, map[string]string{"data": tt.data})
			assert.Equal(t, tt.wantStatus, recorder.Code, recorder.Body.String())
			var problem struct {
				Code string `json:"code"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
			assert.Equal(t, tt.wantCode, problem.Code)
		})
	}
}

func TestPostPastedImageEnforcesEncodedAndDecodedLimits(t *testing.T) {
	mux, _, _ := setupPastedImageAPI(t, "ready")
	decoded := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, workspace.MaxPastedImageBytes-7)...)
	recorder := postPastedImage(t, mux, "ws-pasted-image-api", map[string]string{
		"data": base64.StdEncoding.EncodeToString(decoded),
	})
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code, recorder.Body.String())

	body := `{"data":"` + strings.Repeat("A", int(MaxPastedImageRequestBytes)) + `"}`
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/workspaces/ws-pasted-image-api/pasted-images",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code, recorder.Body.String())
}

func TestPostPastedImageReturnsConflictAtWorkspaceQuota(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	mux, _, worktree := setupPastedImageAPI(t, "ready")
	imageDir := filepath.Join(worktree, workspace.PastedImageDirectory)
	require.NoError(os.MkdirAll(imageDir, 0o755))
	for index := range workspace.MaxPastedImagesPerWorkspace {
		require.NoError(os.WriteFile(
			filepath.Join(imageDir, fmt.Sprintf("existing-%03d.png", index)),
			[]byte("fixture"),
			0o600,
		))
	}

	recorder := postPastedImage(t, mux, "ws-pasted-image-api", map[string]string{
		"data": base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfixture")),
	})

	assert.Equal(http.StatusConflict, recorder.Code, recorder.Body.String())
	var problem struct {
		Code string `json:"code"`
	}
	require.NoError(json.Unmarshal(recorder.Body.Bytes(), &problem))
	assert.Equal("conflict", problem.Code)
}

func setupPastedImageAPI(t *testing.T, status string) (*http.ServeMux, huma.API, string) {
	t.Helper()
	database := dbtest.Open(t)
	worktree := t.TempDir()
	runTokenRuntimeTestGit(t, worktree, "init", "--initial-branch=main")
	runTokenRuntimeTestGit(t, worktree, "config", "user.email", "test@example.test")
	runTokenRuntimeTestGit(t, worktree, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "README.md"), []byte("# Test\n"), 0o644))
	runTokenRuntimeTestGit(t, worktree, "add", "README.md")
	runTokenRuntimeTestGit(t, worktree, "commit", "-m", "initial")
	require.NoError(t, database.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-pasted-image-api", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
		ItemNumber: 1, WorktreePath: worktree, Status: status,
	}))
	manager := workspace.NewManager(database, t.TempDir())
	handler := New(Deps{DB: database, Workspaces: manager})
	mux := http.NewServeMux()
	api := humago.NewWithPrefix(mux, "/api/v1", huma.DefaultConfig("workspace test", "1"))
	handler.Register(api)
	return mux, api, worktree
}

func postPastedImage(
	t *testing.T,
	mux *http.ServeMux,
	workspaceID string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/pasted-images",
		bytes.NewReader(encoded),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

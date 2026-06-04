package terminal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/ptyowner"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

func seedRepo(
	t *testing.T, d *db.DB,
	host, owner, name string,
) int64 {
	t.Helper()
	id, err := d.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity(host, owner, name),
	)
	require.NoError(t, err)
	return id
}

func seedMR(
	t *testing.T, d *db.DB,
	repoID int64, number int, headBranch string,
) {
	t.Helper()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	mr := &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     repoID*10000 + int64(number),
		Number:         number,
		Title:          "Test PR",
		Author:         "author",
		State:          "open",
		HeadBranch:     headBranch,
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	_, err := d.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
}

func TestHandlerWorkspaceNotFound(t *testing.T) {
	d := openTestDB(t)
	mgr := workspace.NewManager(d, t.TempDir())
	h := &Handler{Workspaces: mgr}

	req := httptest.NewRequest(
		http.MethodGet, "/api/v1/workspaces/nonexistent/terminal",
		nil,
	)
	req.SetPathValue("id", "nonexistent")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert := Assert.New(t)
	assert.Equal(http.StatusNotFound, rec.Code)
	assert.Contains(rec.Body.String(), "not found")
}

func TestHandlerWorkspaceNotReady(t *testing.T) {
	d := openTestDB(t)
	wtDir := t.TempDir()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := workspace.NewManager(d, wtDir)
	ws, err := mgr.Create(
		t.Context(), "github", "github.com", "acme", "widget", 42,
	)
	require.NoError(t, err)
	require.Equal(t, "creating", ws.Status)

	h := &Handler{Workspaces: mgr}

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/workspaces/"+ws.ID+"/terminal",
		nil,
	)
	req.SetPathValue("id", ws.ID)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert := Assert.New(t)
	assert.Equal(http.StatusConflict, rec.Code)
	assert.Contains(rec.Body.String(), "not ready")
}

func TestHandlerRejectsConcurrentWorkspaceTerminals(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	h := &Handler{}
	release, err := h.claimTerminalSlot("ws-1")
	require.NoError(err)
	assert.NotNil(release)

	release2, err := h.claimTerminalSlot("ws-1")
	require.Error(err)
	assert.Nil(release2)

	release()

	release3, err := h.claimTerminalSlot("ws-1")
	require.NoError(err)
	assert.NotNil(release3)
	release3()
}

func TestHandlerAttachesPtyOwnerTerminal(t *testing.T) {
	require := require.New(t)

	d := openTestDB(t)
	mgr := workspace.NewManager(d, t.TempDir())
	ownerRoot := t.TempDir()
	mgr.SetPtyOwnerClient(&ptyowner.Client{Root: ownerRoot})
	ws := &workspace.Workspace{
		ID:              "ws-owner",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/thing",
		WorkspaceBranch: "feature/thing",
		WorktreePath:    t.TempDir(),
		TmuxSession:     "middleman-owner-test",
		TerminalBackend: workspace.TerminalBackendPtyOwner,
		Status:          "ready",
	}
	require.NoError(d.InsertWorkspace(t.Context(), ws))

	ctx := t.Context()
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- ptyowner.RunOwner(ctx, ptyowner.Options{
			Root:    ownerRoot,
			Session: ws.TmuxSession,
			Cwd:     ws.WorktreePath,
			Command: []string{"sh", "-c", "printf ready; while IFS= read -r line; do echo got:$line; done"},
		})
	}()
	require.Eventually(func() bool {
		return (&ptyowner.Client{Root: ownerRoot}).Ping(
			context.Background(), ws.TmuxSession,
		) == nil
	}, 2*time.Second, 20*time.Millisecond)
	t.Cleanup(func() {
		_ = (&ptyowner.Client{Root: ownerRoot}).Stop(
			context.Background(), ws.TmuxSession,
		)
		select {
		case <-ownerDone:
		case <-time.After(2 * time.Second):
		}
	})

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/workspaces/{id}/terminal", &Handler{
		Workspaces: mgr,
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	conn, _, err := websocket.Dial(
		t.Context(),
		"ws"+strings.TrimPrefix(server.URL, "http")+
			"/api/v1/workspaces/"+ws.ID+"/terminal",
		nil,
	)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	require.Contains(readWebSocketUntil(t, conn, "ready"), "ready")
	require.NoError(conn.Write(
		t.Context(), websocket.MessageBinary, []byte("hello\n"),
	))
	require.Contains(readWebSocketUntil(t, conn, "got:hello"), "got:hello")
}

func TestProcessExitCode(t *testing.T) {
	assert := Assert.New(t)
	assert.Equal(0, processExitCode(nil))
	assert.Equal(-1, processExitCode(errors.New("wait failed")))
}

func TestParseSizeFallsBack(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodGet, "/?cols=bad&rows=0", nil,
	)
	cols, rows := parseSize(req)
	assert := Assert.New(t)
	assert.Equal(120, cols)
	assert.Equal(30, rows)
}

func readWebSocketUntil(
	t *testing.T,
	conn *websocket.Conn,
	needle string,
) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var builder strings.Builder
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			require.New(t).Failf(
				"read websocket",
				"waiting for %q: %v; got %q",
				needle, err, builder.String(),
			)
		}
		if typ != websocket.MessageBinary {
			continue
		}
		builder.Write(data)
		if strings.Contains(builder.String(), needle) {
			return builder.String()
		}
	}
}

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
)

type docsFolderWire struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Daemon string `json:"daemon,omitempty"`
}

type docsFolderListWire struct {
	Folders []docsFolderWire `json:"folders"`
}

func setupDocsRouteServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	mustWrite := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}
	mustWrite("README.md", "# Readme\nbudget overview\n")
	mustWrite("notes/daily.md", "# Daily\nbudget item\n")
	mustWrite("notes/ideas.md", "# Ideas\n")
	mustWrite("notes/image.png", string([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}))

	cfg := &config.Config{
		DocFolders: []config.DocFolder{
			{ID: "notes", Name: "Notes", Path: root, Daemon: "work"},
		},
	}
	srv := New(openTestDB(t), nil, nil, "/", cfg, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, root
}

func setupPersistentDocsRouteServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		SyncInterval: "5m",
		Host:         "127.0.0.1",
		Port:         8091,
		DocFolders: []config.DocFolder{
			{ID: "notes", Name: "Notes", Path: root},
		},
	}
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, cfg.Save(cfgPath))
	loaded, err := config.Load(cfgPath)
	require.NoError(t, err)
	srv := NewWithConfig(openTestDB(t), nil, nil, nil, loaded, cfgPath, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, root, cfgPath
}

func doDocsJSON(t *testing.T, srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = "127.0.0.1:12345"
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func decodeDocsFolder(t *testing.T, rr *httptest.ResponseRecorder) docsFolderWire {
	t.Helper()
	var body struct {
		Folder docsFolderWire `json:"folder"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	return body.Folder
}

func TestDocsFoldersEndpointListsConfiguredFolders(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, root := setupDocsRouteServer(t)

	rr := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body docsFolderListWire
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Folders, 1)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	require.NoError(err)
	assert.Equal("notes", body.Folders[0].ID)
	assert.Equal("Notes", body.Folders[0].Name)
	assert.Equal(canonicalRoot, body.Folders[0].Path)
	assert.Equal("work", body.Folders[0].Daemon)
}

func TestDocsFolderConfigEndpointsAddRenameRemoveAndPersist(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _, cfgPath := setupPersistentDocsRouteServer(t)
	extraRoot := t.TempDir()

	addRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders", map[string]string{
		"id":     "extra",
		"path":   extraRoot,
		"daemon": "work",
	})
	require.Equal(http.StatusCreated, addRR.Code, addRR.Body.String())
	added := decodeDocsFolder(t, addRR)
	assert.Equal("extra", added.ID)
	assert.Equal(filepath.Base(extraRoot), added.Name)
	assert.Equal("work", added.Daemon)
	wantExtraRoot, err := filepath.EvalSymlinks(extraRoot)
	require.NoError(err)
	assert.Equal(wantExtraRoot, added.Path)

	renameRR := doDocsJSON(t, srv, http.MethodPatch, "/api/v1/docs/folders/extra", map[string]string{
		"name": "Reference",
	})
	require.Equal(http.StatusOK, renameRR.Code, renameRR.Body.String())
	renamed := decodeDocsFolder(t, renameRR)
	assert.Equal("extra", renamed.ID)
	assert.Equal("Reference", renamed.Name)

	deleteRR := doDocsJSON(t, srv, http.MethodDelete, "/api/v1/docs/folders/notes", nil)
	require.Equal(http.StatusNoContent, deleteRR.Code, deleteRR.Body.String())

	listRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders", nil)
	require.Equal(http.StatusOK, listRR.Code, listRR.Body.String())
	var listBody docsFolderListWire
	require.NoError(json.NewDecoder(listRR.Body).Decode(&listBody))
	require.Len(listBody.Folders, 1)
	assert.Equal("extra", listBody.Folders[0].ID)
	assert.Equal("Reference", listBody.Folders[0].Name)

	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(reloaded.DocFolders, 1)
	assert.Equal(config.DocFolder{ID: "extra", Name: "Reference", Path: wantExtraRoot, Daemon: "work"}, reloaded.DocFolders[0])
}

func TestDocsFolderAddRejectsNonLoopback(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _, _ := setupPersistentDocsRouteServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/docs/folders", bytes.NewReader([]byte(`{"path":"/tmp/whatever"}`)))
	req.RemoteAddr = "203.0.113.7:54321"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusForbidden, rr.Code, rr.Body.String())
	var problem ProblemError
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal(CodeForbidden, problem.Code)
	assert.Equal("loopbackOnly", problem.Details["reason"])
	assert.Contains(problem.Detail, "docs mutations require a loopback client")
}

func TestDocsFolderAddDerivesIDAndRejectsInvalidRequests(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _, _ := setupPersistentDocsRouteServer(t)
	extraRoot := filepath.Join(t.TempDir(), "Research Papers!")
	require.NoError(os.Mkdir(extraRoot, 0o755))

	addRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders", map[string]string{
		"path": extraRoot,
	})
	require.Equal(http.StatusCreated, addRR.Code, addRR.Body.String())
	added := decodeDocsFolder(t, addRR)
	assert.Equal("research-papers", added.ID)

	collidingRoot := filepath.Join(t.TempDir(), "Notes")
	require.NoError(os.Mkdir(collidingRoot, 0o755))
	collisionRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders", map[string]string{
		"path": collidingRoot,
	})
	require.Equal(http.StatusCreated, collisionRR.Code, collisionRR.Body.String())
	collision := decodeDocsFolder(t, collisionRR)
	assert.Equal("notes-2", collision.ID)
	assert.Equal("Notes", collision.Name)

	spacedRoot := t.TempDir()
	wantSpacedRoot, err := filepath.EvalSymlinks(spacedRoot)
	require.NoError(err)
	spacedRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders", map[string]string{
		"id":   " spaced ",
		"name": " Spaced ",
		"path": " " + spacedRoot + " ",
	})
	require.Equal(http.StatusCreated, spacedRR.Code, spacedRR.Body.String())
	spaced := decodeDocsFolder(t, spacedRR)
	assert.Equal("spaced", spaced.ID)
	assert.Equal("Spaced", spaced.Name)
	assert.Equal(wantSpacedRoot, spaced.Path)

	duplicateRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders", map[string]string{
		"id":   "notes",
		"path": filepath.Join(t.TempDir(), "missing"),
	})
	assert.Equal(http.StatusConflict, duplicateRR.Code, duplicateRR.Body.String())

	missingRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders", map[string]string{
		"id":   "ghost",
		"path": filepath.Join(t.TempDir(), "missing"),
	})
	assert.Equal(http.StatusNotFound, missingRR.Code, missingRR.Body.String())

	trimmedDuplicateRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders", map[string]string{
		"id":   " notes ",
		"path": t.TempDir(),
	})
	assert.Equal(http.StatusConflict, trimmedDuplicateRR.Code, trimmedDuplicateRR.Body.String())

	blankPathRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders", map[string]string{
		"id":   "blank",
		"path": " \t",
	})
	assert.Equal(http.StatusBadRequest, blankPathRR.Code, blankPathRR.Body.String())

	blankNameRR := doDocsJSON(t, srv, http.MethodPatch, "/api/v1/docs/folders/notes", map[string]string{
		"name": " \t",
	})
	assert.Equal(http.StatusBadRequest, blankNameRR.Code, blankNameRR.Body.String())

	// An explicit id that cannot be addressed as a single path segment is
	// rejected up front instead of persisting an unreachable folder.
	slashIDRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders", map[string]string{
		"id":   "team/docs",
		"path": t.TempDir(),
	})
	require.Equal(http.StatusBadRequest, slashIDRR.Code, slashIDRR.Body.String())
	var slashIDProblem ProblemError
	require.NoError(json.NewDecoder(slashIDRR.Body).Decode(&slashIDProblem))
	assert.Equal("invalidFolder", slashIDProblem.Details["reason"])
}

func TestDocsFolderMutationsRequireConfigPersistenceAndRollbackOnSaveFailure(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)

	unavailableRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders", map[string]string{
		"id":   "extra",
		"path": t.TempDir(),
	})
	require.Equal(http.StatusNotFound, unavailableRR.Code, unavailableRR.Body.String())
	var unavailable ProblemError
	require.NoError(json.NewDecoder(unavailableRR.Body).Decode(&unavailable))
	assert.Equal(CodeSettingsUnavailable, unavailable.Code)

	root := t.TempDir()
	cfg := &config.Config{
		SyncInterval: "5m",
		BasePath:     "/",
		Host:         "0.0.0.0",
		Port:         8091,
		DocFolders:   []config.DocFolder{{ID: "notes", Name: "Notes", Path: root}},
	}
	badPath := filepath.Join(t.TempDir(), "config.toml")
	failSrv := NewWithConfig(openTestDB(t), nil, nil, nil, cfg, badPath, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, failSrv) })

	rollbackRR := doDocsJSON(t, failSrv, http.MethodPost, "/api/v1/docs/folders", map[string]string{
		"id":   "rollback",
		"path": t.TempDir(),
	})
	require.Equal(http.StatusInternalServerError, rollbackRR.Code, rollbackRR.Body.String())
	listRR := doDocsJSON(t, failSrv, http.MethodGet, "/api/v1/docs/folders", nil)
	require.Equal(http.StatusOK, listRR.Code, listRR.Body.String())
	var listBody docsFolderListWire
	require.NoError(json.NewDecoder(listRR.Body).Decode(&listBody))
	require.Len(listBody.Folders, 1)
	assert.Equal("notes", listBody.Folders[0].ID)
}

func TestDocsBrowseEndpointListsDirectoriesOnly(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)
	root := t.TempDir()
	require.NoError(os.MkdirAll(filepath.Join(root, "alpha"), 0o755))
	require.NoError(os.MkdirAll(filepath.Join(root, ".hidden"), 0o755))
	require.NoError(os.WriteFile(filepath.Join(root, "skipme.md"), []byte("hi"), 0o644))

	rr := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/browse?path="+root, nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Path    string `json:"path"`
		Entries []struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			Hidden bool   `json:"hidden"`
		} `json:"entries"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(root, body.Path)
	byName := make(map[string]bool, len(body.Entries))
	for _, entry := range body.Entries {
		byName[entry.Name] = entry.Hidden
		assert.True(filepath.IsAbs(entry.Path))
	}
	_, hasAlpha := byName["alpha"]
	assert.True(hasAlpha)
	hidden, hasHidden := byName[".hidden"]
	require.True(hasHidden)
	assert.True(hidden)
	_, hasFile := byName["skipme.md"]
	assert.False(hasFile)
}

func TestDocsBrowseEndpointExpandsHomeShortcut(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	rr := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/browse?path=~", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Path string `json:"path"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(home, body.Path)
}

func TestDocsBrowseEndpointRejectsNonLoopback(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/docs/browse?path="+t.TempDir(), nil)
	req.RemoteAddr = "203.0.113.7:54321"
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusForbidden, rr.Code, rr.Body.String())
	var problem ProblemError
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal(CodeForbidden, problem.Code)
	assert.Equal("loopbackOnly", problem.Details["reason"])
}

func TestDocsTreeEndpointListsMarkdownOnly(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)

	rr := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/notes/tree", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	raw := rr.Body.String()
	var body struct {
		Name     string `json:"name"`
		Children []struct {
			Name     string `json:"name"`
			RelPath  string `json:"rel_path"`
			IsDir    bool   `json:"is_dir"`
			Children []struct {
				Name    string `json:"name"`
				RelPath string `json:"rel_path"`
				IsDir   bool   `json:"is_dir"`
			} `json:"children"`
		} `json:"children"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal("Notes", body.Name)
	assert.Contains(raw, `"rel_path":"README.md"`)
	assert.Contains(raw, `"rel_path":"notes/daily.md"`)
	assert.NotContains(raw, "image.png")
}

func TestDocsFileEndpointReadsAndWritesMarkdown(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, root := setupDocsRouteServer(t)

	readRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/notes/file?path=notes/ideas.md", nil)
	require.Equal(http.StatusOK, readRR.Code, readRR.Body.String())
	var readBody struct {
		RelPath string `json:"rel_path"`
		Content string `json:"content"`
	}
	require.NoError(json.NewDecoder(readRR.Body).Decode(&readBody))
	assert.Equal("notes/ideas.md", readBody.RelPath)
	assert.Equal("# Ideas\n", readBody.Content)

	writeRR := doDocsJSON(t, srv, http.MethodPut, "/api/v1/docs/folders/notes/file?path=notes/ideas.md", map[string]string{
		"content": "# Updated\n",
	})
	require.Equal(http.StatusOK, writeRR.Code, writeRR.Body.String())
	var writeBody struct {
		RelPath string `json:"rel_path"`
		Size    int    `json:"size"`
	}
	require.NoError(json.NewDecoder(writeRR.Body).Decode(&writeBody))
	assert.Equal("notes/ideas.md", writeBody.RelPath)
	assert.Equal(len("# Updated\n"), writeBody.Size)
	got, err := os.ReadFile(filepath.Join(root, "notes/ideas.md"))
	require.NoError(err)
	assert.Equal("# Updated\n", string(got))
}

func TestDocsSearchEndpointsReturnArrays(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)

	folderRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/notes/search?q=daily&limit=10", nil)
	require.Equal(http.StatusOK, folderRR.Code, folderRR.Body.String())
	var folderBody struct {
		Query string            `json:"query"`
		Hits  []json.RawMessage `json:"hits"`
	}
	require.NoError(json.NewDecoder(folderRR.Body).Decode(&folderBody))
	assert.Equal("daily", folderBody.Query)
	assert.NotNil(folderBody.Hits)
	assert.NotEmpty(folderBody.Hits)

	globalRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/search?q=budget&limit=10", nil)
	require.Equal(http.StatusOK, globalRR.Code, globalRR.Body.String())
	var globalBody struct {
		Query     string            `json:"query"`
		Hits      []json.RawMessage `json:"hits"`
		Warnings  []string          `json:"warnings,omitempty"`
		Truncated bool              `json:"truncated"`
	}
	require.NoError(json.NewDecoder(globalRR.Body).Decode(&globalBody))
	assert.Equal("budget", globalBody.Query)
	assert.NotNil(globalBody.Hits)
	assert.NotEmpty(globalBody.Hits)
	assert.False(globalBody.Truncated)
}

func TestDocsFileCreateDeleteRenameAndBlob(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, root := setupDocsRouteServer(t)

	createRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/notes/file?path=notes/new.md", map[string]string{
		"content": "# New\n",
	})
	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())
	var createBody struct {
		RelPath string `json:"rel_path"`
		Size    int    `json:"size"`
	}
	require.NoError(json.NewDecoder(createRR.Body).Decode(&createBody))
	assert.Equal("notes/new.md", createBody.RelPath)
	assert.Equal(len("# New\n"), createBody.Size)

	duplicateRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/notes/file?path=notes/new.md", map[string]string{
		"content": "# New\n",
	})
	assert.Equal(http.StatusConflict, duplicateRR.Code, duplicateRR.Body.String())

	emptyRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/notes/file?path=notes/empty.md", map[string]string{})
	require.Equal(http.StatusCreated, emptyRR.Code, emptyRR.Body.String())
	emptyReadRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/notes/file?path=notes/empty.md", nil)
	require.Equal(http.StatusOK, emptyReadRR.Code, emptyReadRR.Body.String())
	var emptyReadBody struct {
		Content string `json:"content"`
	}
	require.NoError(json.NewDecoder(emptyReadRR.Body).Decode(&emptyReadBody))
	assert.Empty(emptyReadBody.Content)

	renameRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/notes/file/actions/rename", map[string]string{
		"from": "notes/new.md",
		"to":   "notes/renamed.md",
	})
	require.Equal(http.StatusOK, renameRR.Code, renameRR.Body.String())
	var renameBody struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	require.NoError(json.NewDecoder(renameRR.Body).Decode(&renameBody))
	assert.Equal("notes/new.md", renameBody.From)
	assert.Equal("notes/renamed.md", renameBody.To)

	renamedReadRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/notes/file?path=notes/renamed.md", nil)
	require.Equal(http.StatusOK, renamedReadRR.Code, renamedReadRR.Body.String())
	var renamedReadBody struct {
		Content string `json:"content"`
	}
	require.NoError(json.NewDecoder(renamedReadRR.Body).Decode(&renamedReadBody))
	assert.Equal("# New\n", renamedReadBody.Content)

	blobRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/notes/blob?path=notes/image.png", nil)
	require.Equal(http.StatusOK, blobRR.Code, blobRR.Body.String())
	assert.Equal("image/png", blobRR.Header().Get("Content-Type"))
	assert.Equal("private, max-age=60", blobRR.Header().Get("Cache-Control"))
	assert.NotEmpty(blobRR.Body.Bytes())

	deleteRR := doDocsJSON(t, srv, http.MethodDelete, "/api/v1/docs/folders/notes/file?path=notes/renamed.md", nil)
	require.Equal(http.StatusNoContent, deleteRR.Code, deleteRR.Body.String())
	_, err := os.Stat(filepath.Join(root, "notes/renamed.md"))
	require.ErrorIs(err, os.ErrNotExist)
	deletedReadRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/notes/file?path=notes/renamed.md", nil)
	assert.Equal(http.StatusNotFound, deletedReadRR.Code, deletedReadRR.Body.String())
}

func TestDocsBlobOpenAPIResponseIsBinary(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	doc := NewOpenAPI()
	item := doc.Paths["/docs/folders/{id}/blob"]
	require.NotNil(item)
	require.NotNil(item.Get)
	resp := item.Get.Responses["200"]
	require.NotNil(resp)

	assert.Contains(resp.Content, "application/octet-stream")
	assert.NotContains(resp.Content, "application/json")
	schema := resp.Content["application/octet-stream"].Schema
	require.NotNil(schema)
	assert.Equal("string", schema.Type)
	assert.Equal("binary", schema.Format)
}

func TestDocsFileEndpointRejectsInvalidPathsAndTypes(t *testing.T) {
	assert := Assert.New(t)
	srv, _ := setupDocsRouteServer(t)

	unknownRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/missing/tree", nil)
	assert.Equal(http.StatusNotFound, unknownRR.Code, unknownRR.Body.String())

	traversalRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/notes/file?path=../../escape", nil)
	assert.Equal(http.StatusForbidden, traversalRR.Code, traversalRR.Body.String())

	createTextRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/notes/file?path=notes/bad.txt", map[string]string{
		"content": "x",
	})
	assert.Equal(http.StatusUnsupportedMediaType, createTextRR.Code, createTextRR.Body.String())

	deleteTextRR := doDocsJSON(t, srv, http.MethodDelete, "/api/v1/docs/folders/notes/file?path=notes/bad.txt", nil)
	assert.Equal(http.StatusUnsupportedMediaType, deleteTextRR.Code, deleteTextRR.Body.String())

	blobMarkdownRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/notes/blob?path=README.md", nil)
	assert.Equal(http.StatusUnsupportedMediaType, blobMarkdownRR.Code, blobMarkdownRR.Body.String())
}

func TestDocsSearchEndpointEmptyQueryReturnsEmptyArray(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)

	rr := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/search?q=&limit=10", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Hits []json.RawMessage `json:"hits"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.NotNil(body.Hits)
	assert.Empty(body.Hits)
}

func TestDocsSearchEndpointTruncationAndFailure(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)

	truncatedRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/search?q=budget&limit=1", nil)
	require.Equal(http.StatusOK, truncatedRR.Code, truncatedRR.Body.String())
	var truncatedBody struct {
		Hits      []json.RawMessage `json:"hits"`
		Truncated bool              `json:"truncated"`
	}
	require.NoError(json.NewDecoder(truncatedRR.Body).Decode(&truncatedBody))
	assert.Len(truncatedBody.Hits, 1)
	assert.True(truncatedBody.Truncated)

	missingA := filepath.Join(t.TempDir(), "missing-a")
	missingB := filepath.Join(t.TempDir(), "missing-b")
	failSrv := New(openTestDB(t), nil, nil, "/", &config.Config{
		DocFolders: []config.DocFolder{
			{ID: "a", Name: "A", Path: missingA},
			{ID: "b", Name: "B", Path: missingB},
		},
	}, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, failSrv) })
	failRR := doDocsJSON(t, failSrv, http.MethodGet, "/api/v1/docs/search?q=budget&limit=10", nil)
	assert.Equal(http.StatusInternalServerError, failRR.Code, failRR.Body.String())
}

func TestDocsSearchEndpointSerializesPartialWarnings(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	goodRoot := t.TempDir()
	require.NoError(os.WriteFile(filepath.Join(goodRoot, "hit.md"), []byte("budget partial\n"), 0o644))
	missingRoot := filepath.Join(t.TempDir(), "missing")
	srv := New(openTestDB(t), nil, nil, "/", &config.Config{
		DocFolders: []config.DocFolder{
			{ID: "good", Name: "Good", Path: goodRoot},
			{ID: "missing", Name: "Missing", Path: missingRoot},
		},
	}, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	rr := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/search?q=budget&limit=10", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Hits []struct {
			Folder string `json:"folder"`
		} `json:"hits"`
		Warnings  []string `json:"warnings"`
		Truncated bool     `json:"truncated"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Hits, 1)
	assert.Equal("good", body.Hits[0].Folder)
	assert.NotEmpty(body.Warnings)
	assert.False(body.Truncated)
}

func TestDocsSearchEndpointFindsHitsAcrossFolders(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	rootA := t.TempDir()
	rootB := t.TempDir()
	require.NoError(os.WriteFile(filepath.Join(rootA, "a.md"), []byte("budget alpha\n"), 0o644))
	require.NoError(os.WriteFile(filepath.Join(rootB, "b.md"), []byte("budget beta\n"), 0o644))
	srv := New(openTestDB(t), nil, nil, "/", &config.Config{
		DocFolders: []config.DocFolder{
			{ID: "a", Name: "A", Path: rootA},
			{ID: "b", Name: "B", Path: rootB},
		},
	}, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	rr := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/search?q=budget&limit=10", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Hits []struct {
			Folder string `json:"folder"`
		} `json:"hits"`
		Truncated bool `json:"truncated"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Hits, 2)
	ids := []string{body.Hits[0].Folder, body.Hits[1].Folder}
	assert.ElementsMatch([]string{"a", "b"}, ids)
	assert.False(body.Truncated)
}

func TestDocsFileMutationsRejectNonLoopback(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			name:   "write",
			method: http.MethodPut,
			path:   "/api/v1/docs/folders/notes/file?path=notes/ideas.md",
			body:   map[string]string{"content": "blocked"},
		},
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/api/v1/docs/folders/notes/file?path=notes/blocked.md",
			body:   map[string]string{"content": "blocked"},
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			path:   "/api/v1/docs/folders/notes/file?path=notes/ideas.md",
		},
		{
			name:   "rename",
			method: http.MethodPost,
			path:   "/api/v1/docs/folders/notes/file/actions/rename",
			body:   map[string]string{"from": "notes/ideas.md", "to": "notes/blocked.md"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if tc.body != nil {
				require.NoError(json.NewEncoder(&buf).Encode(tc.body))
			}
			req := httptest.NewRequest(tc.method, tc.path, &buf)
			req.RemoteAddr = "203.0.113.7:54321"
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)

			require.Equal(http.StatusForbidden, rr.Code, rr.Body.String())
			var problem ProblemError
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(CodeForbidden, problem.Code)
			assert.Equal("loopbackOnly", problem.Details["reason"])
		})
	}
}

func TestDocsMutationsRejectBodyTooLarge(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)
	huge := strings.Repeat("a", 5<<20)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "create folder",
			method: http.MethodPost,
			path:   "/api/v1/docs/folders",
			body:   `{"path":"` + huge + `"}`,
		},
		{
			name:   "update folder",
			method: http.MethodPatch,
			path:   "/api/v1/docs/folders/notes",
			body:   `{"name":"` + huge + `"}`,
		},
		{
			name:   "write file",
			method: http.MethodPut,
			path:   "/api/v1/docs/folders/notes/file?path=notes/ideas.md",
			body:   `{"content":"` + huge + `"}`,
		},
		{
			name:   "create file",
			method: http.MethodPost,
			path:   "/api/v1/docs/folders/notes/file?path=notes/large.md",
			body:   `{"content":"` + huge + `"}`,
		},
		{
			name:   "rename file",
			method: http.MethodPost,
			path:   "/api/v1/docs/folders/notes/file/actions/rename",
			body:   `{"from":"notes/ideas.md","to":"` + huge + `"}`,
		},
		{
			name:   "publish git",
			method: http.MethodPost,
			path:   "/api/v1/docs/folders/notes/git/publish",
			body:   `{"message":"` + huge + `"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)

			require.Equal(http.StatusRequestEntityTooLarge, rr.Code, rr.Body.String())
			var problem ProblemError
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(CodePayloadTooLarge, problem.Code)
		})
	}
}

func TestDocsFileWriteAllowsBodyBelowEditorLimit(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)
	content := strings.Repeat("a", 2<<20)
	body, err := json.Marshal(map[string]string{"content": content})
	require.NoError(err)

	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/docs/folders/notes/file?path=notes/ideas.md",
		bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var parsed struct {
		RelPath string `json:"rel_path"`
		Size    int    `json:"size"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&parsed))
	assert.Equal("notes/ideas.md", parsed.RelPath)
	assert.Equal(len(content), parsed.Size)
}

func TestDocsReadEndpointsRejectNonLoopback(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, _ := setupDocsRouteServer(t)

	cases := []struct {
		name string
		path string
	}{
		{name: "list", path: "/api/v1/docs/folders"},
		{name: "tree", path: "/api/v1/docs/folders/notes/tree"},
		{name: "file", path: "/api/v1/docs/folders/notes/file?path=notes/ideas.md"},
		{name: "blob", path: "/api/v1/docs/folders/notes/blob?path=notes/image.png"},
		{name: "folder search", path: "/api/v1/docs/folders/notes/search?q=ideas"},
		{name: "global search", path: "/api/v1/docs/search?q=budget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.RemoteAddr = "203.0.113.7:54321"
			rr := httptest.NewRecorder()

			srv.ServeHTTP(rr, req)

			require.Equal(http.StatusForbidden, rr.Code, rr.Body.String())
			var problem ProblemError
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(CodeForbidden, problem.Code)
			assert.Equal("loopbackOnly", problem.Details["reason"])
		})
	}
}

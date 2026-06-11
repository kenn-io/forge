package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/docs"
)

const docsMaxBodyBytes = 4 << 20

type docsFolderResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Daemon string `json:"daemon,omitempty"`
}

func docsFolderToResponse(v config.DocFolder) docsFolderResponse {
	return docsFolderResponse{ID: v.ID, Name: v.Name, Path: v.Path, Daemon: v.Daemon}
}

func docsFoldersToResponse(vs []config.DocFolder) []docsFolderResponse {
	out := make([]docsFolderResponse, 0, len(vs))
	for _, v := range vs {
		out = append(out, docsFolderToResponse(v))
	}
	return out
}

type listDocsFoldersOutput struct {
	Body struct {
		Folders []docsFolderResponse `json:"folders"`
	}
}

type docsFolderOutput struct {
	Body struct {
		Folder docsFolderResponse `json:"folder"`
	}
}

type createDocsFolderInput struct {
	Body struct {
		ID     string `json:"id" required:"false"`
		Name   string `json:"name" required:"false"`
		Path   string `json:"path" required:"false"`
		Daemon string `json:"daemon" required:"false"`
	}
}

type createDocsFolderOutput struct {
	Status int `status:"201"`
	Body   struct {
		Folder docsFolderResponse `json:"folder"`
	}
}

type docsFolderIDInput struct {
	ID string `path:"id"`
}

type updateDocsFolderInput struct {
	ID   string `path:"id"`
	Body struct {
		Name string `json:"name" required:"false"`
	}
}

type docsFolderPathInput struct {
	ID   string `path:"id"`
	Path string `query:"path"`
}

type docsReadFileOutput struct {
	Body struct {
		RelPath string `json:"rel_path"`
		Content string `json:"content"`
	}
}

type docsWriteFileInput struct {
	ID   string `path:"id"`
	Path string `query:"path"`
	Body struct {
		Content string `json:"content" required:"false"`
	}
}

type docsFileWriteBody struct {
	RelPath string `json:"rel_path"`
	Size    int    `json:"size"`
}

type docsWriteFileOutput = bodyOutput[docsFileWriteBody]

type docsCreateFileInput struct {
	ID   string `path:"id"`
	Path string `query:"path"`
	Body *struct {
		Content string `json:"content" required:"false"`
	}
}

type docsCreateFileOutput = createdOutput[docsFileWriteBody]

type docsRenameFileInput struct {
	ID   string `path:"id"`
	Body struct {
		From string `json:"from" required:"false"`
		To   string `json:"to" required:"false"`
	}
}

type docsRenameFileOutput struct {
	Body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
}

type docsNoContentOutput struct {
	Status int `status:"204"`
}

type docsBlobOutput struct {
	ContentType   string `header:"Content-Type"`
	CacheControl  string `header:"Cache-Control"`
	ContentLength string `header:"Content-Length"`
	Body          []byte
}

type docsSearchInput struct {
	ID    string `path:"id"`
	Query string `query:"q"`
	Limit int    `query:"limit"`
}

type docsSearchOutput struct {
	Body struct {
		Query string     `json:"query"`
		Hits  []docs.Hit `json:"hits"`
	}
}

type docsSearchAllInput struct {
	Query string `query:"q"`
	Limit int    `query:"limit"`
}

type docsSearchAllOutput struct {
	Body struct {
		Query     string                `json:"query"`
		Hits      []docs.CrossFolderHit `json:"hits"`
		Warnings  []string              `json:"warnings,omitempty"`
		Truncated bool                  `json:"truncated"`
	}
}

type docsBrowseInput struct {
	Path string `query:"path"`
}

type docsBrowseEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Hidden bool   `json:"hidden"`
}

type docsBrowseOutput struct {
	Body struct {
		Path    string            `json:"path"`
		Parent  string            `json:"parent,omitempty"`
		Entries []docsBrowseEntry `json:"entries"`
	}
}

type docsGitPublishInput struct {
	ID   string `path:"id"`
	Body struct {
		Message string `json:"message" required:"false"`
	}
}

type docsPublishLockSet struct {
	mu    sync.Mutex
	locks map[string]struct{}
}

func newDocsPublishLockSet() *docsPublishLockSet {
	return &docsPublishLockSet{locks: make(map[string]struct{})}
}

func (l *docsPublishLockSet) tryAcquire(folderID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.locks[folderID]; ok {
		return false
	}
	l.locks[folderID] = struct{}{}
	return true
}

func (l *docsPublishLockSet) release(folderID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.locks, folderID)
}

func (s *Server) registerDocsAPI(api huma.API) {
	huma.Get(api, "/docs/folders", s.listDocsFolders,
		documentOperation("list-docs-folders", "List docs folders", "Docs"))
	huma.Register(api, huma.Operation{
		OperationID:   "create-docs-folder",
		Method:        http.MethodPost,
		Path:          "/docs/folders",
		DefaultStatus: http.StatusCreated,
		Summary:       "Create docs folder",
		Tags:          []string{"Docs"},
		MaxBodyBytes:  docsMaxBodyBytes,
	}, s.createDocsFolder)
	huma.Register(api, huma.Operation{
		OperationID:  "update-docs-folder",
		Method:       http.MethodPatch,
		Path:         "/docs/folders/{id}",
		Summary:      "Update docs folder",
		Tags:         []string{"Docs"},
		MaxBodyBytes: docsMaxBodyBytes,
	}, s.updateDocsFolder)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-docs-folder",
		Method:        http.MethodDelete,
		Path:          "/docs/folders/{id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete docs folder",
		Tags:          []string{"Docs"},
	}, s.deleteDocsFolder)
	huma.Get(api, "/docs/browse", s.browseDocsFolders,
		documentOperation("browse-docs-folders", "Browse docs folders", "Docs"))
	huma.Get(api, "/docs/folders/{id}/tree", s.getDocsTree,
		documentOperation("get-docs-tree", "Get docs folder tree", "Docs"))
	huma.Get(api, "/docs/folders/{id}/git", s.getDocsGitStatus,
		documentOperation("get-docs-git-status", "Get docs Git status", "Docs"))
	huma.Get(api, "/docs/folders/{id}/git/changes", s.getDocsGitChanges,
		documentOperation("get-docs-git-changes", "Get docs Git changes", "Docs"))
	huma.Register(api, huma.Operation{
		OperationID:   "publish-docs-git",
		Method:        http.MethodPost,
		Path:          "/docs/folders/{id}/git/publish",
		DefaultStatus: http.StatusOK,
		Summary:       "Publish docs Git changes",
		Tags:          []string{"Docs"},
		MaxBodyBytes:  docsMaxBodyBytes,
	}, s.publishDocsGit)
	huma.Get(api, "/docs/folders/{id}/file", s.readDocsFile,
		documentOperation("read-docs-file", "Read docs file", "Docs"))
	huma.Register(api, huma.Operation{
		OperationID:   "write-docs-file",
		Method:        http.MethodPut,
		Path:          "/docs/folders/{id}/file",
		DefaultStatus: http.StatusOK,
		Summary:       "Write docs file",
		Tags:          []string{"Docs"},
		MaxBodyBytes:  docsMaxBodyBytes,
	}, s.writeDocsFile)
	huma.Register(api, huma.Operation{
		OperationID:   "create-docs-file",
		Method:        http.MethodPost,
		Path:          "/docs/folders/{id}/file",
		DefaultStatus: http.StatusCreated,
		Summary:       "Create docs file",
		Tags:          []string{"Docs"},
		MaxBodyBytes:  docsMaxBodyBytes,
	}, s.createDocsFile)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-docs-file",
		Method:        http.MethodDelete,
		Path:          "/docs/folders/{id}/file",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete docs file",
		Tags:          []string{"Docs"},
	}, s.deleteDocsFile)
	huma.Register(api, huma.Operation{
		OperationID:   "rename-docs-file",
		Method:        http.MethodPost,
		Path:          "/docs/folders/{id}/file/actions/rename",
		DefaultStatus: http.StatusOK,
		Summary:       "Rename docs file",
		Tags:          []string{"Docs"},
		MaxBodyBytes:  docsMaxBodyBytes,
	}, s.renameDocsFile)
	huma.Register(api, huma.Operation{
		OperationID: "read-docs-blob",
		Method:      http.MethodGet,
		Path:        "/docs/folders/{id}/blob",
		Summary:     "Read docs image blob",
		Tags:        []string{"Docs"},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Image response",
				Content: map[string]*huma.MediaType{
					"application/octet-stream": {
						Schema: &huma.Schema{Type: "string", Format: "binary"},
					},
				},
			},
		},
	}, s.readDocsBlob)
	huma.Get(api, "/docs/folders/{id}/search", s.searchDocsFolder,
		documentOperation("search-docs-folder", "Search docs folder", "Docs"))
	huma.Get(api, "/docs/search", s.searchDocs,
		documentOperation("search-docs", "Search docs", "Docs"))
}

func (s *Server) listDocsFolders(_ context.Context, _ *struct{}) (*listDocsFoldersOutput, error) {
	out := &listDocsFoldersOutput{}
	out.Body.Folders = docsFoldersToResponse(s.docsRegistry.Folders())
	return out, nil
}

func (s *Server) createDocsFolder(_ context.Context, in *createDocsFolderInput) (*createDocsFolderOutput, error) {
	if s.cfgPath == "" || s.cfg == nil {
		return nil, problemNotFound(CodeSettingsUnavailable, "settings not available", nil)
	}
	path := strings.TrimSpace(in.Body.Path)
	if strings.TrimSpace(path) == "" {
		return nil, problemBadRequest(CodeBadRequest, "path is required", map[string]any{"reason": "missingPath"})
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	prev := cloneDocFolders(s.cfg.DocFolders)
	id := strings.TrimSpace(in.Body.ID)
	if id == "" {
		derivedPath, err := docsFolderDerivePath(path)
		if err != nil {
			return nil, problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "invalidFolder"})
		}
		id = docs.DeriveFolderID(derivedPath, prev)
	}
	folder := config.DocFolder{ID: id, Name: strings.TrimSpace(in.Body.Name), Path: path, Daemon: strings.TrimSpace(in.Body.Daemon)}
	if err := s.docsRegistry.Add(folder); err != nil {
		return nil, docsRegistryProblem(err)
	}
	s.cfg.DocFolders = s.docsRegistry.Folders()
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.DocFolders = prev
		s.docsRegistry.Replace(prev)
		return nil, problemInternal("save config: " + err.Error())
	}
	added, err := s.docsRegistry.Lookup(id)
	if err != nil {
		return nil, docsRegistryProblem(err)
	}
	out := &createDocsFolderOutput{Status: http.StatusCreated}
	out.Body.Folder = docsFolderToResponse(added)
	return out, nil
}

func (s *Server) updateDocsFolder(_ context.Context, in *updateDocsFolderInput) (*docsFolderOutput, error) {
	if s.cfgPath == "" || s.cfg == nil {
		return nil, problemNotFound(CodeSettingsUnavailable, "settings not available", nil)
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	prev := cloneDocFolders(s.cfg.DocFolders)
	id := strings.TrimSpace(in.ID)
	if err := s.docsRegistry.Rename(id, in.Body.Name); err != nil {
		return nil, docsRegistryProblem(err)
	}
	s.cfg.DocFolders = s.docsRegistry.Folders()
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.DocFolders = prev
		s.docsRegistry.Replace(prev)
		return nil, problemInternal("save config: " + err.Error())
	}
	renamed, err := s.docsRegistry.Lookup(id)
	if err != nil {
		return nil, docsRegistryProblem(err)
	}
	out := &docsFolderOutput{}
	out.Body.Folder = docsFolderToResponse(renamed)
	return out, nil
}

func (s *Server) deleteDocsFolder(_ context.Context, in *docsFolderIDInput) (*docsNoContentOutput, error) {
	if s.cfgPath == "" || s.cfg == nil {
		return nil, problemNotFound(CodeSettingsUnavailable, "settings not available", nil)
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	prev := cloneDocFolders(s.cfg.DocFolders)
	if err := s.docsRegistry.Remove(in.ID); err != nil {
		return nil, docsRegistryProblem(err)
	}
	s.cfg.DocFolders = s.docsRegistry.Folders()
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.DocFolders = prev
		s.docsRegistry.Replace(prev)
		return nil, problemInternal("save config: " + err.Error())
	}
	return &docsNoContentOutput{Status: http.StatusNoContent}, nil
}

func (s *Server) browseDocsFolders(_ context.Context, in *docsBrowseInput) (*docsBrowseOutput, error) {
	path := in.Path
	if path == "" {
		path = "~"
	}
	expanded, err := docsFolderDerivePath(path)
	if err != nil {
		return nil, problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "invalidFolder"})
	}
	entries, err := os.ReadDir(expanded)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, problemNotFound(CodeNotFound, err.Error(), map[string]any{"reason": "notFound"})
		}
		return nil, problemInternal(err.Error())
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	out := &docsBrowseOutput{}
	out.Body.Path = expanded
	out.Body.Parent = filepath.Dir(expanded)
	out.Body.Entries = make([]docsBrowseEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		out.Body.Entries = append(out.Body.Entries, docsBrowseEntry{
			Name:   name,
			Path:   filepath.Join(expanded, name),
			Hidden: len(name) > 0 && name[0] == '.',
		})
	}
	return out, nil
}

func (s *Server) getDocsTree(_ context.Context, in *docsFolderIDInput) (*bodyOutput[docs.Node], error) {
	tree, err := s.docsRegistry.Tree(in.ID)
	if err != nil {
		return nil, docsRegistryProblem(err)
	}
	return &bodyOutput[docs.Node]{Body: tree}, nil
}

func (s *Server) getDocsGitStatus(ctx context.Context, in *docsFolderIDInput) (*bodyOutput[docs.GitStatusResponse], error) {
	status, err := s.docsRegistry.GitStatus(ctx, in.ID)
	if err != nil {
		return nil, docsRegistryProblem(err)
	}
	return &bodyOutput[docs.GitStatusResponse]{Body: status}, nil
}

func (s *Server) getDocsGitChanges(ctx context.Context, in *docsFolderIDInput) (*bodyOutput[docs.GitChangesResponse], error) {
	changes, err := s.docsRegistry.GitChanges(ctx, in.ID)
	if err != nil {
		return nil, docsRegistryProblem(err)
	}
	return &bodyOutput[docs.GitChangesResponse]{Body: changes}, nil
}

func (s *Server) publishDocsGit(ctx context.Context, in *docsGitPublishInput) (*bodyOutput[docs.PublishResponse], error) {
	if !s.docsPublishLocks.tryAcquire(in.ID) {
		return nil, problemConflict(
			CodeConflict,
			"another publish is in flight for this folder",
			map[string]any{"reason": "publishInProgress"},
		)
	}
	defer s.docsPublishLocks.release(in.ID)

	published, err := s.docsRegistry.GitPublish(ctx, in.ID, in.Body.Message)
	if err != nil {
		return nil, docsGitPublishProblem(err)
	}
	return &bodyOutput[docs.PublishResponse]{Body: published}, nil
}

func (s *Server) readDocsFile(_ context.Context, in *docsFolderPathInput) (*docsReadFileOutput, error) {
	if in.Path == "" {
		return nil, docsMissingPathProblem()
	}
	body, err := s.docsRegistry.ReadFile(in.ID, in.Path)
	if err != nil {
		return nil, docsRegistryProblem(err)
	}
	out := &docsReadFileOutput{}
	out.Body.RelPath = in.Path
	out.Body.Content = string(body)
	return out, nil
}

func (s *Server) writeDocsFile(_ context.Context, in *docsWriteFileInput) (*docsWriteFileOutput, error) {
	if in.Path == "" {
		return nil, docsMissingPathProblem()
	}
	if err := s.docsRegistry.WriteFile(in.ID, in.Path, []byte(in.Body.Content)); err != nil {
		return nil, docsRegistryProblem(err)
	}
	return &docsWriteFileOutput{
		Body: docsFileWriteBody{RelPath: in.Path, Size: len(in.Body.Content)},
	}, nil
}

func (s *Server) createDocsFile(_ context.Context, in *docsCreateFileInput) (*docsCreateFileOutput, error) {
	if in.Path == "" {
		return nil, docsMissingPathProblem()
	}
	content := ""
	if in.Body != nil {
		content = in.Body.Content
	}
	if err := s.docsRegistry.CreateFile(in.ID, in.Path, []byte(content)); err != nil {
		return nil, docsRegistryProblem(err)
	}
	return &docsCreateFileOutput{
		Status: http.StatusCreated,
		Body:   docsFileWriteBody{RelPath: in.Path, Size: len(content)},
	}, nil
}

func (s *Server) deleteDocsFile(_ context.Context, in *docsFolderPathInput) (*docsNoContentOutput, error) {
	if in.Path == "" {
		return nil, docsMissingPathProblem()
	}
	if err := s.docsRegistry.DeleteFile(in.ID, in.Path); err != nil {
		return nil, docsRegistryProblem(err)
	}
	return &docsNoContentOutput{Status: http.StatusNoContent}, nil
}

func (s *Server) renameDocsFile(_ context.Context, in *docsRenameFileInput) (*docsRenameFileOutput, error) {
	if in.Body.From == "" || in.Body.To == "" {
		return nil, docsMissingPathProblem()
	}
	if err := s.docsRegistry.RenameFile(in.ID, in.Body.From, in.Body.To); err != nil {
		return nil, docsRegistryProblem(err)
	}
	out := &docsRenameFileOutput{}
	out.Body.From = in.Body.From
	out.Body.To = in.Body.To
	return out, nil
}

func (s *Server) readDocsBlob(_ context.Context, in *docsFolderPathInput) (*docsBlobOutput, error) {
	if in.Path == "" {
		return nil, docsMissingPathProblem()
	}
	blob, err := s.docsRegistry.ReadBlob(in.ID, in.Path)
	if err != nil {
		return nil, docsRegistryProblem(err)
	}
	return &docsBlobOutput{
		ContentType:   blob.ContentType,
		CacheControl:  "private, max-age=60",
		ContentLength: strconv.Itoa(len(blob.Body)),
		Body:          blob.Body,
	}, nil
}

func (s *Server) searchDocsFolder(_ context.Context, in *docsSearchInput) (*docsSearchOutput, error) {
	hits, err := s.docsRegistry.Search(in.ID, in.Query, docsSearchLimit(in.Limit))
	if err != nil {
		return nil, docsRegistryProblem(err)
	}
	if hits == nil {
		hits = []docs.Hit{}
	}
	out := &docsSearchOutput{}
	out.Body.Query = in.Query
	out.Body.Hits = hits
	return out, nil
}

func (s *Server) searchDocs(ctx context.Context, in *docsSearchAllInput) (*docsSearchAllOutput, error) {
	result, err := s.docsRegistry.SearchAll(ctx, in.Query, docsSearchLimit(in.Limit))
	if err != nil {
		return nil, problemInternal("docs search failed: " + err.Error())
	}
	hits := result.Hits
	if hits == nil {
		hits = []docs.CrossFolderHit{}
	}
	out := &docsSearchAllOutput{}
	out.Body.Query = in.Query
	out.Body.Hits = hits
	out.Body.Warnings = result.Warnings
	out.Body.Truncated = result.Truncated
	return out, nil
}

func docsSearchLimit(limit int) int {
	if limit > 0 && limit <= 200 {
		return limit
	}
	return 25
}

func cloneDocFolders(v []config.DocFolder) []config.DocFolder {
	out := make([]config.DocFolder, len(v))
	copy(out, v)
	return out
}

func docsFolderDerivePath(path string) (string, error) {
	if path == "~" || len(path) >= 2 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	return filepath.Abs(path)
}

func docsMissingPathProblem() huma.StatusError {
	return problemBadRequest(
		CodeBadRequest,
		"path query parameter is required",
		map[string]any{"reason": "missingPath"},
	)
}

func docsRegistryProblem(err error) huma.StatusError {
	var unsafeConfig *docs.UnsafeGitConfigError
	switch {
	case errors.As(err, &unsafeConfig):
		return problemBadRequest(CodeBadRequest, unsafeConfig.Error(), map[string]any{"reason": "unsafeGitConfig"})
	case errors.Is(err, docs.ErrFolderNotFound):
		return problemNotFound(CodeNotFound, err.Error(), map[string]any{"reason": "folderNotFound"})
	case errors.Is(err, docs.ErrDuplicateFolderID):
		return problemConflict(CodeConflict, err.Error(), map[string]any{"reason": "duplicateFolderID"})
	case errors.Is(err, docs.ErrInvalidFolder):
		return problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "invalidFolder"})
	case errors.Is(err, docs.ErrOutsideFolder):
		return problemForbidden(err.Error(), map[string]any{"reason": "outsideFolder"})
	case errors.Is(err, docs.ErrUnsupportedExtension):
		return newProblem(http.StatusUnsupportedMediaType, CodeBadRequest, err.Error(), map[string]any{"reason": "unsupportedExtension"})
	case errors.Is(err, docs.ErrAlreadyExists):
		return problemConflict(CodeConflict, err.Error(), map[string]any{"reason": "alreadyExists"})
	case errors.Is(err, os.ErrNotExist):
		return problemNotFound(CodeNotFound, err.Error(), map[string]any{"reason": "notFound"})
	default:
		return problemInternal(err.Error())
	}
}

func docsGitPublishProblem(err error) huma.StatusError {
	var commitFailed *docs.CommitFailedError
	var noUpstream *docs.NoUpstreamError
	var pushFailed *docs.PushFailedAfterCommitError
	switch {
	case errors.Is(err, docs.ErrEmptyMessage):
		return problemBadRequest(CodeBadRequest, "commit message is required", map[string]any{"reason": "emptyMessage"})
	case errors.Is(err, docs.ErrNoMarkdownChanges):
		return problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "noMarkdownChanges"})
	case errors.Is(err, docs.ErrNotAGitRepo):
		return problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "notGitRepo"})
	case errors.Is(err, docs.ErrIndexNotClean):
		return problemConflict(CodeConflict, err.Error(), map[string]any{"reason": "indexNotClean"})
	case errors.Is(err, docs.ErrConflict):
		return problemConflict(CodeConflict, err.Error(), map[string]any{"reason": "conflict"})
	case errors.As(err, &noUpstream):
		return problemBadRequest(CodeBadRequest, noUpstream.Error(), map[string]any{
			"reason":            "noUpstream",
			"branch":            noUpstream.Branch,
			"suggested_command": noUpstream.SuggestedCommand,
		})
	case errors.As(err, &commitFailed):
		return newProblem(http.StatusInternalServerError, CodeInternalError, commitFailed.Stderr, map[string]any{
			"reason": "commitFailed",
		})
	case errors.As(err, &pushFailed):
		return newProblem(http.StatusBadGateway, CodeUpstreamError, pushFailed.Error(), map[string]any{
			"reason": "pushFailedAfterCommit",
			"commit": pushFailed.Commit,
		})
	default:
		return docsRegistryProblem(err)
	}
}

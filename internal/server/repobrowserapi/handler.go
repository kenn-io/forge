package repobrowserapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/server/httpapi"
)

type Handler struct {
	resolver     *httpapi.RepositoryResolver
	clones       *gitclone.Manager
	refreshEvery time.Duration
}

type Deps struct {
	Resolver *httpapi.RepositoryResolver
	Clones   *gitclone.Manager
	Config   *config.Config
}

func New(deps Deps) *Handler {
	return &Handler{
		resolver:     deps.Resolver,
		clones:       deps.Clones,
		refreshEvery: refreshIntervalForConfig(deps.Config),
	}
}

type RepoBrowserRefsResponse struct {
	Repo       httpapi.RepoRefResponse   `json:"repo"`
	Refs       []gitclone.RepoBrowserRef `json:"refs"`
	DefaultRef gitclone.RepoBrowserRef   `json:"default_ref"`
	Truncated  bool                      `json:"truncated"`
}

type RepoBrowserTreeResponse struct {
	Repo      httpapi.RepoRefResponse         `json:"repo"`
	Ref       gitclone.RepoBrowserRef         `json:"ref"`
	Entries   []gitclone.RepoBrowserTreeEntry `json:"entries"`
	Truncated bool                            `json:"truncated"`
}

type RepoBrowserBlobResponse struct {
	Repo httpapi.RepoRefResponse  `json:"repo"`
	Ref  gitclone.RepoBrowserRef  `json:"ref"`
	Blob gitclone.RepoBrowserBlob `json:"blob"`
}

type RepoBrowserLastChangedResponse struct {
	Repo    httpapi.RepoRefResponse               `json:"repo"`
	Ref     gitclone.RepoBrowserRef               `json:"ref"`
	Commits map[string]gitclone.RepoBrowserCommit `json:"commits"`
}

type RepoBrowserHistoryResponse struct {
	Repo    httpapi.RepoRefResponse      `json:"repo"`
	Ref     gitclone.RepoBrowserRef      `json:"ref"`
	Path    string                       `json:"path"`
	Commits []gitclone.RepoBrowserCommit `json:"commits"`
}

type RepoBrowserCommitResponse struct {
	Repo   httpapi.RepoRefResponse    `json:"repo"`
	Ref    gitclone.RepoBrowserRef    `json:"ref"`
	Path   string                     `json:"path"`
	Commit gitclone.RepoBrowserCommit `json:"commit"`
}

type repoBrowserInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	RepoPath     string `query:"repo_path"`
}

type repoBrowserHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	RepoPath     string `query:"repo_path"`
}

type repoBrowserRefInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	RepoPath     string `query:"repo_path"`
	RefType      string `query:"ref_type"`
	RefName      string `query:"ref_name"`
	RefSHA       string `query:"ref_sha"`
}

type repoBrowserHostRefInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	RepoPath     string `query:"repo_path"`
	RefType      string `query:"ref_type"`
	RefName      string `query:"ref_name"`
	RefSHA       string `query:"ref_sha"`
}

type repoBrowserPathInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	RepoPath     string `query:"repo_path"`
	RefType      string `query:"ref_type"`
	RefName      string `query:"ref_name"`
	RefSHA       string `query:"ref_sha"`
	Path         string `query:"path"`
}

type repoBrowserHostPathInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	RepoPath     string `query:"repo_path"`
	RefType      string `query:"ref_type"`
	RefName      string `query:"ref_name"`
	RefSHA       string `query:"ref_sha"`
	Path         string `query:"path"`
}

type repoBrowserLastChangedInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string   `path:"owner"`
	Name         string   `path:"name"`
	RepoPath     string   `query:"repo_path"`
	RefType      string   `query:"ref_type"`
	RefName      string   `query:"ref_name"`
	RefSHA       string   `query:"ref_sha"`
	Paths        []string `query:"path,explode"`
}

type repoBrowserHostLastChangedInput struct {
	Provider     string   `path:"provider"`
	PlatformHost string   `path:"platform_host"`
	Owner        string   `path:"owner"`
	Name         string   `path:"name"`
	RepoPath     string   `query:"repo_path"`
	RefType      string   `query:"ref_type"`
	RefName      string   `query:"ref_name"`
	RefSHA       string   `query:"ref_sha"`
	Paths        []string `query:"path,explode"`
}

type repoBrowserCommitInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	RepoPath     string `query:"repo_path"`
	RefType      string `query:"ref_type"`
	RefName      string `query:"ref_name"`
	RefSHA       string `query:"ref_sha"`
	Path         string `query:"path"`
	SHA          string `query:"sha"`
}

type repoBrowserHostCommitInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	RepoPath     string `query:"repo_path"`
	RefType      string `query:"ref_type"`
	RefName      string `query:"ref_name"`
	RefSHA       string `query:"ref_sha"`
	Path         string `query:"path"`
	SHA          string `query:"sha"`
}

type repoBrowserAssetOutput struct {
	ContentType        string `header:"Content-Type"`
	CacheControl       string `header:"Cache-Control"`
	ContentLength      string `header:"Content-Length"`
	ContentTypeOptions string `header:"X-Content-Type-Options"`
	Body               []byte
}

func (h *Handler) listRepoBrowserRefs(
	ctx context.Context,
	input *repoBrowserInput,
) (*httpapi.BodyOutput[RepoBrowserRefsResponse], error) {
	return h.listRepoBrowserRefsFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath)
}

func (h *Handler) listRepoBrowserRefsOnHost(
	ctx context.Context,
	input *repoBrowserHostInput,
) (*httpapi.BodyOutput[RepoBrowserRefsResponse], error) {
	return h.listRepoBrowserRefsFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath)
}

func (h *Handler) listRepoBrowserRefsFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
) (*httpapi.BodyOutput[RepoBrowserRefsResponse], error) {
	repo, repoRef, release, err := h.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	defer release()
	refs, defaultRef, truncated, err := h.clones.ListRepoBrowserRefs(ctx, repoRef, repo.DefaultBranch)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &httpapi.BodyOutput[RepoBrowserRefsResponse]{Body: RepoBrowserRefsResponse{
		Repo:       h.repoRefFromRepo(*repo),
		Refs:       refs,
		DefaultRef: defaultRef,
		Truncated:  truncated,
	}}, nil
}

func (h *Handler) listRepoBrowserTree(
	ctx context.Context,
	input *repoBrowserRefInput,
) (*httpapi.BodyOutput[RepoBrowserTreeResponse], error) {
	return h.listRepoBrowserTreeFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA))
}

func (h *Handler) listRepoBrowserTreeOnHost(
	ctx context.Context,
	input *repoBrowserHostRefInput,
) (*httpapi.BodyOutput[RepoBrowserTreeResponse], error) {
	return h.listRepoBrowserTreeFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA))
}

func (h *Handler) listRepoBrowserTreeFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
) (*httpapi.BodyOutput[RepoBrowserTreeResponse], error) {
	repo, repoRef, release, err := h.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	defer release()
	resolvedRef, err := h.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	entries, truncated, err := h.clones.ListRepoBrowserTree(ctx, repoRef, repoBrowserPinnedRef(resolvedRef))
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &httpapi.BodyOutput[RepoBrowserTreeResponse]{Body: RepoBrowserTreeResponse{
		Repo:      h.repoRefFromRepo(*repo),
		Ref:       resolvedRef,
		Entries:   entries,
		Truncated: truncated,
	}}, nil
}

func (h *Handler) getRepoBrowserBlob(
	ctx context.Context,
	input *repoBrowserPathInput,
) (*httpapi.BodyOutput[RepoBrowserBlobResponse], error) {
	return h.getRepoBrowserBlobFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (h *Handler) getRepoBrowserBlobOnHost(
	ctx context.Context,
	input *repoBrowserHostPathInput,
) (*httpapi.BodyOutput[RepoBrowserBlobResponse], error) {
	return h.getRepoBrowserBlobFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (h *Handler) getRepoBrowserBlobFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
	path string,
) (*httpapi.BodyOutput[RepoBrowserBlobResponse], error) {
	repo, repoRef, release, err := h.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	defer release()
	resolvedRef, err := h.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	blob, err := h.clones.ReadRepoBrowserBlob(ctx, repoRef, repoBrowserPinnedRef(resolvedRef), path)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &httpapi.BodyOutput[RepoBrowserBlobResponse]{Body: RepoBrowserBlobResponse{
		Repo: h.repoRefFromRepo(*repo),
		Ref:  resolvedRef,
		Blob: blob,
	}}, nil
}

func (h *Handler) getRepoBrowserAsset(
	ctx context.Context,
	input *repoBrowserPathInput,
) (*repoBrowserAssetOutput, error) {
	return h.getRepoBrowserAssetFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (h *Handler) getRepoBrowserAssetOnHost(
	ctx context.Context,
	input *repoBrowserHostPathInput,
) (*repoBrowserAssetOutput, error) {
	return h.getRepoBrowserAssetFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (h *Handler) getRepoBrowserAssetFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
	path string,
) (*repoBrowserAssetOutput, error) {
	if !repoBrowserAssetRefIsImmutable(ref) {
		return nil, repoBrowserProblem(errRepoBrowserMutableAssetRef)
	}
	_, repoRef, release, err := h.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	defer release()
	resolvedRef, err := h.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	blob, err := h.clones.ReadRepoBrowserAsset(ctx, repoRef, repoBrowserPinnedRef(resolvedRef), path)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &repoBrowserAssetOutput{
		ContentType:        blob.MediaType,
		CacheControl:       "private, max-age=300",
		ContentLength:      strconvFormatInt(blob.Size),
		ContentTypeOptions: "nosniff",
		Body:               []byte(blob.Content),
	}, nil
}

func repoBrowserAssetRefIsImmutable(ref gitclone.RepoBrowserRef) bool {
	return ref.Type == gitclone.RepoBrowserRefCommit && isRepoBrowserFullHexSHA(ref.SHA)
}

func isRepoBrowserFullHexSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, ch := range value {
		if (ch >= '0' && ch <= '9') ||
			(ch >= 'a' && ch <= 'f') ||
			(ch >= 'A' && ch <= 'F') {
			continue
		}
		return false
	}
	return true
}

func (h *Handler) getRepoBrowserLastChanged(
	ctx context.Context,
	input *repoBrowserLastChangedInput,
) (*httpapi.BodyOutput[RepoBrowserLastChangedResponse], error) {
	return h.getRepoBrowserLastChangedFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Paths)
}

func (h *Handler) getRepoBrowserLastChangedOnHost(
	ctx context.Context,
	input *repoBrowserHostLastChangedInput,
) (*httpapi.BodyOutput[RepoBrowserLastChangedResponse], error) {
	return h.getRepoBrowserLastChangedFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Paths)
}

func (h *Handler) getRepoBrowserLastChangedFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
	paths []string,
) (*httpapi.BodyOutput[RepoBrowserLastChangedResponse], error) {
	repo, repoRef, release, err := h.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	defer release()
	resolvedRef, err := h.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	commits, err := h.clones.RepoBrowserLastChanged(ctx, repoRef, repoBrowserPinnedRef(resolvedRef), paths)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &httpapi.BodyOutput[RepoBrowserLastChangedResponse]{Body: RepoBrowserLastChangedResponse{
		Repo:    h.repoRefFromRepo(*repo),
		Ref:     resolvedRef,
		Commits: commits,
	}}, nil
}

func (h *Handler) getRepoBrowserHistory(
	ctx context.Context,
	input *repoBrowserPathInput,
) (*httpapi.BodyOutput[RepoBrowserHistoryResponse], error) {
	return h.getRepoBrowserHistoryFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (h *Handler) getRepoBrowserHistoryOnHost(
	ctx context.Context,
	input *repoBrowserHostPathInput,
) (*httpapi.BodyOutput[RepoBrowserHistoryResponse], error) {
	return h.getRepoBrowserHistoryFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (h *Handler) getRepoBrowserHistoryFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
	path string,
) (*httpapi.BodyOutput[RepoBrowserHistoryResponse], error) {
	repo, repoRef, release, err := h.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	defer release()
	resolvedRef, err := h.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	commits, err := h.clones.RepoBrowserFileHistory(ctx, repoRef, repoBrowserPinnedRef(resolvedRef), path)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &httpapi.BodyOutput[RepoBrowserHistoryResponse]{Body: RepoBrowserHistoryResponse{
		Repo:    h.repoRefFromRepo(*repo),
		Ref:     resolvedRef,
		Path:    path,
		Commits: commits,
	}}, nil
}

func (h *Handler) getRepoBrowserCommit(
	ctx context.Context,
	input *repoBrowserCommitInput,
) (*httpapi.BodyOutput[RepoBrowserCommitResponse], error) {
	return h.getRepoBrowserCommitFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path, input.SHA)
}

func (h *Handler) getRepoBrowserCommitOnHost(
	ctx context.Context,
	input *repoBrowserHostCommitInput,
) (*httpapi.BodyOutput[RepoBrowserCommitResponse], error) {
	return h.getRepoBrowserCommitFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path, input.SHA)
}

func (h *Handler) getRepoBrowserCommitFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
	path, sha string,
) (*httpapi.BodyOutput[RepoBrowserCommitResponse], error) {
	repo, repoRef, release, err := h.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	defer release()
	resolvedRef, err := h.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	commit, err := h.clones.RepoBrowserCommitDetail(ctx, repoRef, repoBrowserPinnedRef(resolvedRef), path, sha)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &httpapi.BodyOutput[RepoBrowserCommitResponse]{Body: RepoBrowserCommitResponse{
		Repo:   h.repoRefFromRepo(*repo),
		Ref:    resolvedRef,
		Path:   path,
		Commit: commit,
	}}, nil
}

func (h *Handler) ensureRepoBrowserClone(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
) (*db.Repo, gitclone.RepoBrowserRepoRef, func(), error) {
	if h.clones == nil {
		return nil, gitclone.RepoBrowserRepoRef{}, nil, errRepoBrowserCloneUnavailable
	}
	if h.resolver == nil {
		return nil, gitclone.RepoBrowserRepoRef{}, nil, httpapi.ErrRepositoryStoreUnavailable
	}
	repoPath = canonicalRepoBrowserRepoPath(owner, name, repoPath)
	repo, err := h.resolver.Lookup(ctx, provider, platformHost, repoPath)
	if err != nil {
		return nil, gitclone.RepoBrowserRepoRef{}, nil, err
	}
	repo, release, err := h.resolver.LeaseActiveRepository(ctx, repo.ID)
	if err != nil {
		return nil, gitclone.RepoBrowserRepoRef{}, nil, err
	}
	if repo == nil {
		return nil, gitclone.RepoBrowserRepoRef{}, nil, httpapi.ErrRepoNotFound
	}
	if strings.TrimSpace(repo.CloneURL) == "" {
		release()
		return nil, gitclone.RepoBrowserRepoRef{}, nil, errRepoBrowserCloneUnavailable
	}
	repoRef := gitclone.RepoBrowserRepoRef{
		RepoID:    repo.ID,
		Provider:  repo.Platform,
		Host:      repo.PlatformHost,
		Owner:     repo.Owner,
		Name:      repo.Name,
		RepoPath:  repo.RepoPath,
		RemoteURL: repo.CloneURL,
	}
	if err := h.clones.EnsureRepoBrowserClone(ctx, repoRef); err != nil {
		release()
		return nil, gitclone.RepoBrowserRepoRef{}, nil, err
	}
	return repo, repoRef, release, nil
}

func (h *Handler) repoRefFromRepo(repo db.Repo) httpapi.RepoRefResponse {
	return h.resolver.Ref(repo)
}

func (h *Handler) resolveRepoBrowserReadRef(
	ctx context.Context,
	repo gitclone.RepoBrowserRepoRef,
	ref gitclone.RepoBrowserRef,
) (gitclone.RepoBrowserRef, error) {
	return h.clones.ResolveRepoBrowserRef(ctx, repo, ref)
}

func repoBrowserPinnedRef(ref gitclone.RepoBrowserRef) gitclone.RepoBrowserRef {
	return gitclone.RepoBrowserRef{
		Type: gitclone.RepoBrowserRefCommit,
		SHA:  ref.SHA,
	}
}

func canonicalRepoBrowserRepoPath(owner, name, repoPath string) string {
	repoPath = strings.Trim(repoPath, "/ ")
	if repoPath != "" {
		return repoPath
	}
	owner = strings.Trim(owner, "/ ")
	name = strings.Trim(name, "/ ")
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

var errRepoBrowserCloneUnavailable = errors.New("repo browser clone unavailable")
var errRepoBrowserMutableAssetRef = errors.New("repo browser asset requires immutable commit ref")

func repoBrowserRef(refType, name, sha string) gitclone.RepoBrowserRef {
	typ := gitclone.RepoBrowserRefType(strings.TrimSpace(refType))
	if typ == "" {
		typ = gitclone.RepoBrowserRefBranch
	}
	return gitclone.RepoBrowserRef{
		Type: typ,
		Name: strings.TrimSpace(name),
		SHA:  strings.TrimSpace(sha),
	}
}

func repoBrowserProblem(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, httpapi.ErrRepoPathRequired) {
		return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), map[string]any{"reason": "missing_repo_path"})
	}
	if errors.Is(err, httpapi.ErrRepoNotFound) {
		return httpapi.NotFound(httpapi.CodeRepoNotFound, "repo not found", map[string]any{"reason": "repo_not_found"})
	}
	if errors.Is(err, errRepoBrowserCloneUnavailable) {
		return httpapi.NotFound(httpapi.CodeNotFound, "repo browser clone unavailable", map[string]any{"reason": "clone_unavailable"})
	}
	if errors.Is(err, errRepoBrowserMutableAssetRef) {
		return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), map[string]any{"reason": "mutable_ref_not_allowed"})
	}
	if errors.Is(err, gitclone.ErrUnsafePath) {
		return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), map[string]any{"reason": "unsafe_path"})
	}
	if errors.Is(err, gitclone.ErrTooManyPaths) {
		return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), map[string]any{"reason": "too_many_paths"})
	}
	if errors.Is(err, gitclone.ErrTooLarge) || errors.Is(err, gitclone.ErrTooLargeAsset) {
		return httpapi.NewProblem(http.StatusRequestEntityTooLarge, httpapi.CodeBadRequest, err.Error(), map[string]any{"reason": "too_large"})
	}
	if errors.Is(err, gitclone.ErrUnsupportedAsset) {
		return httpapi.NewProblem(http.StatusUnsupportedMediaType, httpapi.CodeBadRequest, err.Error(), map[string]any{"reason": "unsupported_asset"})
	}
	if errors.Is(err, gitclone.ErrCommitOutOfScope) {
		return httpapi.NotFound(httpapi.CodeNotFound, err.Error(), map[string]any{"reason": "commit_out_of_scope"})
	}
	if errors.Is(err, gitclone.ErrNotFound) {
		return httpapi.NotFound(httpapi.CodeNotFound, err.Error(), map[string]any{"reason": "not_found"})
	}
	if strings.Contains(err.Error(), "platform_host is required") ||
		strings.Contains(err.Error(), "unsupported platform") {
		return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	return httpapi.Internal(err.Error())
}

func strconvFormatInt(v int64) string {
	return strconv.FormatInt(v, 10)
}

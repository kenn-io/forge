package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
)

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

func (s *Server) listRepoBrowserRefs(
	ctx context.Context,
	input *repoBrowserInput,
) (*bodyOutput[repoBrowserRefsResponse], error) {
	return s.listRepoBrowserRefsFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath)
}

func (s *Server) listRepoBrowserRefsOnHost(
	ctx context.Context,
	input *repoBrowserHostInput,
) (*bodyOutput[repoBrowserRefsResponse], error) {
	return s.listRepoBrowserRefsFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath)
}

func (s *Server) listRepoBrowserRefsFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
) (*bodyOutput[repoBrowserRefsResponse], error) {
	repo, repoRef, err := s.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	refs, defaultRef, truncated, err := s.clones.ListRepoBrowserRefs(ctx, repoRef, repo.DefaultBranch)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &bodyOutput[repoBrowserRefsResponse]{Body: repoBrowserRefsResponse{
		Repo:       s.repoRefFromRepo(*repo),
		Refs:       refs,
		DefaultRef: defaultRef,
		Truncated:  truncated,
	}}, nil
}

func (s *Server) listRepoBrowserTree(
	ctx context.Context,
	input *repoBrowserRefInput,
) (*bodyOutput[repoBrowserTreeResponse], error) {
	return s.listRepoBrowserTreeFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA))
}

func (s *Server) listRepoBrowserTreeOnHost(
	ctx context.Context,
	input *repoBrowserHostRefInput,
) (*bodyOutput[repoBrowserTreeResponse], error) {
	return s.listRepoBrowserTreeFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA))
}

func (s *Server) listRepoBrowserTreeFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
) (*bodyOutput[repoBrowserTreeResponse], error) {
	repo, repoRef, err := s.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	resolvedRef, err := s.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	entries, truncated, err := s.clones.ListRepoBrowserTree(ctx, repoRef, repoBrowserPinnedRef(resolvedRef))
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &bodyOutput[repoBrowserTreeResponse]{Body: repoBrowserTreeResponse{
		Repo:      s.repoRefFromRepo(*repo),
		Ref:       resolvedRef,
		Entries:   entries,
		Truncated: truncated,
	}}, nil
}

func (s *Server) getRepoBrowserBlob(
	ctx context.Context,
	input *repoBrowserPathInput,
) (*bodyOutput[repoBrowserBlobResponse], error) {
	return s.getRepoBrowserBlobFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (s *Server) getRepoBrowserBlobOnHost(
	ctx context.Context,
	input *repoBrowserHostPathInput,
) (*bodyOutput[repoBrowserBlobResponse], error) {
	return s.getRepoBrowserBlobFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (s *Server) getRepoBrowserBlobFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
	path string,
) (*bodyOutput[repoBrowserBlobResponse], error) {
	repo, repoRef, err := s.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	resolvedRef, err := s.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	blob, err := s.clones.ReadRepoBrowserBlob(ctx, repoRef, repoBrowserPinnedRef(resolvedRef), path)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &bodyOutput[repoBrowserBlobResponse]{Body: repoBrowserBlobResponse{
		Repo: s.repoRefFromRepo(*repo),
		Ref:  resolvedRef,
		Blob: blob,
	}}, nil
}

func (s *Server) getRepoBrowserAsset(
	ctx context.Context,
	input *repoBrowserPathInput,
) (*repoBrowserAssetOutput, error) {
	return s.getRepoBrowserAssetFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (s *Server) getRepoBrowserAssetOnHost(
	ctx context.Context,
	input *repoBrowserHostPathInput,
) (*repoBrowserAssetOutput, error) {
	return s.getRepoBrowserAssetFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (s *Server) getRepoBrowserAssetFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
	path string,
) (*repoBrowserAssetOutput, error) {
	if !repoBrowserAssetRefIsImmutable(ref) {
		return nil, repoBrowserProblem(errRepoBrowserMutableAssetRef)
	}
	_, repoRef, err := s.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	resolvedRef, err := s.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	blob, err := s.clones.ReadRepoBrowserAsset(ctx, repoRef, repoBrowserPinnedRef(resolvedRef), path)
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

func (s *Server) getRepoBrowserLastChanged(
	ctx context.Context,
	input *repoBrowserLastChangedInput,
) (*bodyOutput[repoBrowserLastChangedResponse], error) {
	return s.getRepoBrowserLastChangedFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Paths)
}

func (s *Server) getRepoBrowserLastChangedOnHost(
	ctx context.Context,
	input *repoBrowserHostLastChangedInput,
) (*bodyOutput[repoBrowserLastChangedResponse], error) {
	return s.getRepoBrowserLastChangedFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Paths)
}

func (s *Server) getRepoBrowserLastChangedFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
	paths []string,
) (*bodyOutput[repoBrowserLastChangedResponse], error) {
	repo, repoRef, err := s.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	resolvedRef, err := s.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	commits, err := s.clones.RepoBrowserLastChanged(ctx, repoRef, repoBrowserPinnedRef(resolvedRef), paths)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &bodyOutput[repoBrowserLastChangedResponse]{Body: repoBrowserLastChangedResponse{
		Repo:    s.repoRefFromRepo(*repo),
		Ref:     resolvedRef,
		Commits: commits,
	}}, nil
}

func (s *Server) getRepoBrowserHistory(
	ctx context.Context,
	input *repoBrowserPathInput,
) (*bodyOutput[repoBrowserHistoryResponse], error) {
	return s.getRepoBrowserHistoryFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (s *Server) getRepoBrowserHistoryOnHost(
	ctx context.Context,
	input *repoBrowserHostPathInput,
) (*bodyOutput[repoBrowserHistoryResponse], error) {
	return s.getRepoBrowserHistoryFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path)
}

func (s *Server) getRepoBrowserHistoryFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
	path string,
) (*bodyOutput[repoBrowserHistoryResponse], error) {
	repo, repoRef, err := s.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	resolvedRef, err := s.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	commits, err := s.clones.RepoBrowserFileHistory(ctx, repoRef, repoBrowserPinnedRef(resolvedRef), path)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &bodyOutput[repoBrowserHistoryResponse]{Body: repoBrowserHistoryResponse{
		Repo:    s.repoRefFromRepo(*repo),
		Ref:     resolvedRef,
		Path:    path,
		Commits: commits,
	}}, nil
}

func (s *Server) getRepoBrowserCommit(
	ctx context.Context,
	input *repoBrowserCommitInput,
) (*bodyOutput[repoBrowserCommitResponse], error) {
	return s.getRepoBrowserCommitFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path, input.SHA)
}

func (s *Server) getRepoBrowserCommitOnHost(
	ctx context.Context,
	input *repoBrowserHostCommitInput,
) (*bodyOutput[repoBrowserCommitResponse], error) {
	return s.getRepoBrowserCommitFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.RepoPath, repoBrowserRef(input.RefType, input.RefName, input.RefSHA), input.Path, input.SHA)
}

func (s *Server) getRepoBrowserCommitFor(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
	ref gitclone.RepoBrowserRef,
	path, sha string,
) (*bodyOutput[repoBrowserCommitResponse], error) {
	repo, repoRef, err := s.ensureRepoBrowserClone(ctx, provider, platformHost, owner, name, repoPath)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	resolvedRef, err := s.resolveRepoBrowserReadRef(ctx, repoRef, ref)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	commit, err := s.clones.RepoBrowserCommitDetail(ctx, repoRef, repoBrowserPinnedRef(resolvedRef), path, sha)
	if err != nil {
		return nil, repoBrowserProblem(err)
	}
	return &bodyOutput[repoBrowserCommitResponse]{Body: repoBrowserCommitResponse{
		Repo:   s.repoRefFromRepo(*repo),
		Ref:    resolvedRef,
		Path:   path,
		Commit: commit,
	}}, nil
}

func (s *Server) ensureRepoBrowserClone(
	ctx context.Context,
	provider, platformHost, owner, name, repoPath string,
) (*db.Repo, gitclone.RepoBrowserRepoRef, error) {
	if s.clones == nil {
		return nil, gitclone.RepoBrowserRepoRef{}, errRepoBrowserCloneUnavailable
	}
	repoPath = canonicalRepoBrowserRepoPath(owner, name, repoPath)
	repo, err := s.lookupRepoByRefInput(ctx, repoRefInput{
		Provider:     provider,
		PlatformHost: platformHost,
		RepoPath:     repoPath,
	})
	if err != nil {
		return nil, gitclone.RepoBrowserRepoRef{}, err
	}
	if strings.TrimSpace(repo.CloneURL) == "" {
		return nil, gitclone.RepoBrowserRepoRef{}, errRepoBrowserCloneUnavailable
	}
	repoRef := gitclone.RepoBrowserRepoRef{
		Provider:  repo.Platform,
		Host:      repo.PlatformHost,
		Owner:     repo.Owner,
		Name:      repo.Name,
		RepoPath:  repo.RepoPath,
		RemoteURL: repo.CloneURL,
	}
	if err := s.clones.EnsureRepoBrowserClone(ctx, repoRef); err != nil {
		return nil, gitclone.RepoBrowserRepoRef{}, err
	}
	return repo, repoRef, nil
}

func (s *Server) resolveRepoBrowserReadRef(
	ctx context.Context,
	repo gitclone.RepoBrowserRepoRef,
	ref gitclone.RepoBrowserRef,
) (gitclone.RepoBrowserRef, error) {
	return s.clones.ResolveRepoBrowserRef(ctx, repo, ref)
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
	if errors.Is(err, errRepoPathRequired) {
		return problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "missing_repo_path"})
	}
	if errors.Is(err, errRepoNotFound) {
		return problemNotFound(CodeRepoNotFound, "repo not found", map[string]any{"reason": "repo_not_found"})
	}
	if errors.Is(err, errRepoBrowserCloneUnavailable) {
		return problemNotFound(CodeNotFound, "repo browser clone unavailable", map[string]any{"reason": "clone_unavailable"})
	}
	if errors.Is(err, errRepoBrowserMutableAssetRef) {
		return problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "mutable_ref_not_allowed"})
	}
	if errors.Is(err, gitclone.ErrUnsafePath) {
		return problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "unsafe_path"})
	}
	if errors.Is(err, gitclone.ErrTooManyPaths) {
		return problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "too_many_paths"})
	}
	if errors.Is(err, gitclone.ErrTooLarge) || errors.Is(err, gitclone.ErrTooLargeAsset) {
		return newProblem(http.StatusRequestEntityTooLarge, CodeBadRequest, err.Error(), map[string]any{"reason": "too_large"})
	}
	if errors.Is(err, gitclone.ErrUnsupportedAsset) {
		return newProblem(http.StatusUnsupportedMediaType, CodeBadRequest, err.Error(), map[string]any{"reason": "unsupported_asset"})
	}
	if errors.Is(err, gitclone.ErrCommitOutOfScope) {
		return problemNotFound(CodeNotFound, err.Error(), map[string]any{"reason": "commit_out_of_scope"})
	}
	if errors.Is(err, gitclone.ErrNotFound) {
		return problemNotFound(CodeNotFound, err.Error(), map[string]any{"reason": "not_found"})
	}
	if strings.Contains(err.Error(), "platform_host is required") ||
		strings.Contains(err.Error(), "unsupported platform") {
		return problemBadRequest(CodeBadRequest, err.Error(), nil)
	}
	return problemInternal(err.Error())
}

func strconvFormatInt(v int64) string {
	return strconv.FormatInt(v, 10)
}

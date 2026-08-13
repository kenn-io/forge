package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

type viewerIdentityProvider struct {
	kind      platform.Kind
	host      string
	cacheKeys map[string]string
	logins    map[string]string
	errs      map[string]error

	mu    sync.Mutex
	calls []string
}

func (p *viewerIdentityProvider) Platform() platform.Kind { return p.kind }
func (p *viewerIdentityProvider) Host() string            { return p.host }
func (p *viewerIdentityProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{ReadAuthenticatedUser: true}
}
func (p *viewerIdentityProvider) AuthenticatedUser(_ context.Context, ref platform.RepoRef) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, ref.RepoPath)
	if err := p.errs[ref.RepoPath]; err != nil {
		return "", err
	}
	return p.logins[ref.RepoPath], nil
}
func (p *viewerIdentityProvider) AuthenticatedUserCacheKey(ref platform.RepoRef) string {
	return p.cacheKeys[ref.RepoPath]
}
func (p *viewerIdentityProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func newViewerIdentityTestServer(
	t *testing.T,
	providers []*viewerIdentityProvider,
	repos []platform.RepoRef,
) (*Server, map[string]int64) {
	t.Helper()
	database := dbtest.Open(t)
	providerList := make([]platform.Provider, 0, len(providers))
	for _, provider := range providers {
		providerList = append(providerList, provider)
	}
	registry, err := platform.NewRegistry(providerList...)
	require.NoError(t, err)
	refs := make([]ghclient.RepoRef, 0, len(repos))
	repoIDs := make(map[string]int64, len(repos))
	for _, repo := range repos {
		id, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(repo))
		require.NoError(t, err)
		repoIDs[repo.RepoPath] = id
		refs = append(refs, ghclient.RepoRef{
			Platform: repo.Platform, PlatformHost: repo.Host,
			Owner: repo.Owner, Name: repo.Name, RepoPath: repo.RepoPath,
			PlatformExternalID: repo.PlatformExternalID,
		})
	}
	syncer := ghclient.NewSyncerWithRegistry(registry, database, nil, refs, time.Hour, nil, nil)
	t.Cleanup(syncer.Stop)
	return &Server{db: database, syncer: syncer}, repoIDs
}

func TestResolveAuthenticatedViewerLoginsRestrictsProviderCallsToRepoFilters(t *testing.T) {
	selected := &viewerIdentityProvider{
		kind: platform.KindGitHub, host: "github.com",
		cacheKeys: map[string]string{"acme/widget": "selected-credential"},
		logins:    map[string]string{"acme/widget": "alice"},
	}
	unrelated := &viewerIdentityProvider{
		kind: platform.KindGitLab, host: "gitlab.example.com",
		cacheKeys: map[string]string{"other/tool": "unrelated-credential"},
		errs:      map[string]error{"other/tool": errors.New("unavailable credential")},
	}
	srv, repoIDs := newViewerIdentityTestServer(t, []*viewerIdentityProvider{selected, unrelated}, []platform.RepoRef{
		{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget", PlatformExternalID: "repo-widget"},
		{Platform: platform.KindGitLab, Host: "gitlab.example.com", Owner: "other", Name: "tool", RepoPath: "other/tool", PlatformExternalID: "repo-tool"},
	})

	got, err := srv.resolveAuthenticatedViewerLogins(t.Context(), []db.RepoFilter{{
		Platform: "github", PlatformHost: "github.com", RepoPath: "acme/widget",
	}})
	require.NoError(t, err)
	assert.Equal(t, []db.RepoViewerLogin{{RepoID: repoIDs["acme/widget"], Login: "alice"}}, got)
	assert.Equal(t, 1, selected.callCount())
	assert.Zero(t, unrelated.callCount())
}

func TestResolveAuthenticatedViewerLoginsCachesByEffectiveCredentialForProcessLifetime(t *testing.T) {
	provider := &viewerIdentityProvider{
		kind: platform.KindGitHub, host: "github.com",
		cacheKeys: map[string]string{
			"acme/widget": "shared-credential",
			"acme/gadget": "shared-credential",
		},
		logins: map[string]string{
			"acme/widget": "alice",
			"acme/gadget": "alice",
		},
	}
	srv, _ := newViewerIdentityTestServer(t, []*viewerIdentityProvider{provider}, []platform.RepoRef{
		{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget", PlatformExternalID: "repo-widget"},
		{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "gadget", RepoPath: "acme/gadget", PlatformExternalID: "repo-gadget"},
	})

	first, err := srv.resolveAuthenticatedViewerLogins(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, first, 2)
	second, err := srv.resolveAuthenticatedViewerLogins(t.Context(), nil)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, provider.callCount())
}

func TestResolveAuthenticatedViewerLoginsDoesNotCacheFailures(t *testing.T) {
	provider := &viewerIdentityProvider{
		kind: platform.KindGitHub, host: "github.com",
		cacheKeys: map[string]string{"acme/widget": "credential"},
		logins:    map[string]string{"acme/widget": "alice"},
		errs:      map[string]error{"acme/widget": errors.New("temporary failure")},
	}
	srv, _ := newViewerIdentityTestServer(t, []*viewerIdentityProvider{provider}, []platform.RepoRef{{
		Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget", PlatformExternalID: "repo-widget",
	}})

	_, err := srv.resolveAuthenticatedViewerLogins(t.Context(), nil)
	require.Error(t, err)
	delete(provider.errs, "acme/widget")
	got, err := srv.resolveAuthenticatedViewerLogins(t.Context(), nil)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, 2, provider.callCount())
}

type unkeyedViewerIdentityProvider struct {
	base *viewerIdentityProvider
}

func (p *unkeyedViewerIdentityProvider) Platform() platform.Kind { return p.base.Platform() }
func (p *unkeyedViewerIdentityProvider) Host() string            { return p.base.Host() }
func (p *unkeyedViewerIdentityProvider) Capabilities() platform.Capabilities {
	return p.base.Capabilities()
}
func (p *unkeyedViewerIdentityProvider) AuthenticatedUser(
	ctx context.Context, ref platform.RepoRef,
) (string, error) {
	return p.base.AuthenticatedUser(ctx, ref)
}

func TestResolveAuthenticatedViewerLoginsKeepsUnkeyedGitHubReposSeparate(t *testing.T) {
	base := &viewerIdentityProvider{
		kind: platform.KindGitHub, host: "github.com",
		logins: map[string]string{
			"acme/widget": "alice",
			"other/tool":  "bob",
		},
	}
	provider := &unkeyedViewerIdentityProvider{base: base}
	database := dbtest.Open(t)
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)
	refs := []ghclient.RepoRef{
		{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget", PlatformExternalID: "repo-widget"},
		{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "other", Name: "tool", RepoPath: "other/tool", PlatformExternalID: "repo-tool"},
	}
	for _, ref := range refs {
		_, err := database.UpsertRepo(t.Context(), platform.DBRepoIdentity(platform.RepoRef{
			Platform: ref.Platform, Host: ref.PlatformHost, Owner: ref.Owner, Name: ref.Name,
			RepoPath: ref.RepoPath, PlatformExternalID: ref.PlatformExternalID,
		}))
		require.NoError(t, err)
	}
	syncer := ghclient.NewSyncerWithRegistry(registry, database, nil, refs, time.Hour, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := &Server{db: database, syncer: syncer}

	got, err := srv.resolveAuthenticatedViewerLogins(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, 2, base.callCount())
}

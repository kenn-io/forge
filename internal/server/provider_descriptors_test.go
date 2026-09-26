package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

type descriptorCloneRoutes struct {
	source tokenauth.Source
}

func (r descriptorCloneRoutes) SourceForRepo(
	_, _, owner, name string,
) tokenauth.Source {
	if owner == "acme" && (name == "widget" || name == "widgets") {
		return r.source
	}
	return nil
}

func TestWorkspaceLaunchRefreshFollowsStableRepositoryRename(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	seedPR(t, database, "acme", "widget", 42)
	server := New(database, nil, nil, "/", nil, ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, server) })

	current, err := server.ResolveWorkspaceLaunchSpec(
		t.Context(), providerplane.WorkspaceLaunchRequest{
			Repository: providerplane.RepositoryRoute{
				Provider: "github", PlatformHost: "github.com",
				Owner: "acme", Name: "widget",
			},
			ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		},
	)
	require.NoError(err)

	renameTime := time.Now().UTC().Add(time.Minute)
	renamed, err := database.ObserveRepository(
		t.Context(), db.RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			PlatformRepoID: current.Repository.PlatformRepoID,
			Owner:          "acme-renamed", Name: "widget-renamed",
		},
	)
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderObservation(
		t.Context(), renamed.Repository.ID, db.RepoProviderMetadata{
			CloneURL:      "https://github.com/acme-renamed/widget-renamed.git",
			DefaultBranch: "main",
		}, nil, nil,
	))
	server.now = func() time.Time { return renameTime.Add(time.Minute) }

	refreshed, err := server.RefreshWorkspaceLaunchSpec(t.Context(), current)
	require.NoError(err)
	assert.Equal(current.Repository.PlatformRepoID, refreshed.Repository.PlatformRepoID)
	assert.Equal("acme-renamed", refreshed.Repository.Owner)
	assert.Equal("widget-renamed", refreshed.Repository.Name)
}

func TestNodeGitLabCloneReadsFetchMergeRequestHead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const mrNumber = 7

	work, remote, platformHost := setupHTTPWorktreeBaseForServerTest(t, "base-copy")
	baseSHA := gitfixture.SHA(t, work, "main")
	gitfixture.Run(t, work, "checkout", "-b", "contributor/fork", "main")
	require.NoError(os.WriteFile(
		filepath.Join(work, "gitlab-mr.txt"), []byte("merge request head\n"), 0o644,
	))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "gitlab merge request head")
	headSHA := gitfixture.SHA(t, work, "HEAD")
	gitfixture.Run(t, work, "push", remote, "HEAD:refs/merge-requests/7/head")
	gitfixture.Run(t, remote, "update-server-info")
	cloneURL := "http://" + platformHost + "/acme/widget.git"

	hubDB := dbtest.Open(t)
	repoID, err := reposeed.Seed(t.Context(), hubDB, db.RepoIdentity{
		Platform: "gitlab", PlatformHost: platformHost,
		PlatformRepoID: 7,
		Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
	})
	require.NoError(err)
	seedPRForRepo(
		t, hubDB, repoID, platformHost, "acme", "widget", mrNumber,
		withSeedPRHeadSHA(headSHA), withSeedPRBaseSHA(baseSHA),
		withSeedPRHeadRepoCloneURL(cloneURL),
	)
	require.NoError(hubDB.UpdateDiffSHAs(
		t.Context(), repoID, mrNumber, headSHA, baseSHA, baseSHA,
	))
	require.NoError(hubDB.UpdateRepoProviderObservation(
		t.Context(), repoID, db.RepoProviderMetadata{
			CloneURL: cloneURL, DefaultBranch: "main",
		}, nil, nil,
	))

	hubCredentials, err := federationauth.Open(
		filepath.Join(t.TempDir(), "hub-credentials.json"),
	)
	require.NoError(err)
	token, err := hubCredentials.MintInbound(
		proxyTestNodeID, federationauth.SpokeToHubScopes(),
	)
	require.NoError(err)
	hubServer := New(
		hubDB, nil, nil, "/", nil,
		ServerOptions{
			DaemonAccess: DaemonAccessOptions{
				Token: "hub-local-secret", RequireAPIAuth: true,
			},
			FederationSpokeID:                  proxyTestHubID,
			FederationCredentials:              hubCredentials,
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	t.Cleanup(func() { gracefulShutdown(t, hubServer) })
	hub := httptest.NewTLSServer(hubServer)
	t.Cleanup(hub.Close)

	spokeCredentials, err := federationauth.Open(filepath.Join(t.TempDir(), "spoke-credentials.json"))
	require.NoError(err)
	require.NoError(spokeCredentials.StoreOutbound(
		proxyTestHubID, token, federationauth.SpokeToHubScopes(),
	))
	nodeConfig := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{
			NodeID: proxyTestHubID, BaseURL: hub.URL,
		},
	}}
	nodeServer := New(dbtest.Open(t), nil, nil, "/", nodeConfig, ServerOptions{
		FederationSpokeID: proxyTestNodeID, FederationSpokeActive: true,
		FederationCredentials: spokeCredentials,
		FederationHTTPClient:  hub.Client(),
		Clones: gitclone.New(t.TempDir(), descriptorCloneRoutes{
			source: testTokenSource("spoke-git-token"),
		}),
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, nodeServer) })

	response := testutil.DoJSON(
		t, nodeServer, http.MethodGet,
		"/api/v1/host/"+platformHost+"/pulls/gitlab/acme/widget/7/commits", nil)

	require.Equal(http.StatusOK, response.Code, response.Body.String())
	assert.Contains(response.Body.String(), headSHA)
}

func (descriptorCloneRoutes) FallbackSource(string) tokenauth.Source { return nil }

func TestDiffDescriptorRoundTripSeedsNodeRepositoryCatalog(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hubDB := dbtest.Open(t)
	seedPR(
		t, hubDB, "acme", "widget", 42,
		withSeedPRHeadSHA("head-sha"), withSeedPRBaseSHA("base-sha"),
	)
	repo, err := hubDB.GetRepoByIdentity(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(hubDB.UpdateRepoProviderObservation(
		t.Context(), repo.ID, db.RepoProviderMetadata{
			CloneURL:      "https://github.com/acme/widget.git",
			DefaultBranch: "main",
		}, nil, nil,
	))

	hubCredentials, err := federationauth.Open(
		t.TempDir() + "/hub-credentials.json",
	)
	require.NoError(err)
	token, err := hubCredentials.MintInbound(
		proxyTestNodeID, federationauth.SpokeToHubScopes(),
	)
	require.NoError(err)
	hubServer := New(
		hubDB, nil, nil, "/", nil,
		ServerOptions{
			DaemonAccess: DaemonAccessOptions{
				Token: "hub-local-secret", RequireAPIAuth: true,
			},
			FederationSpokeID:                  proxyTestHubID,
			FederationCredentials:              hubCredentials,
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	observedAt := time.Date(2026, time.August, 22, 14, 0, 0, 0, time.UTC)
	hubServer.now = func() time.Time { return observedAt }
	t.Cleanup(func() { gracefulShutdown(t, hubServer) })
	hub := httptest.NewTLSServer(hubServer)
	t.Cleanup(hub.Close)

	spokeCredentials, err := federationauth.Open(t.TempDir() + "/spoke-credentials.json")
	require.NoError(err)
	require.NoError(spokeCredentials.StoreOutbound(
		proxyTestHubID, token, federationauth.SpokeToHubScopes(),
	))
	nodeDB := dbtest.Open(t)
	nodeConfig := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{
			NodeID: proxyTestHubID, BaseURL: hub.URL,
		},
	}}
	nodeServer := New(nodeDB, nil, nil, "/", nodeConfig, ServerOptions{
		FederationSpokeID:                  proxyTestNodeID,
		FederationSpokeActive:              true,
		FederationCredentials:              spokeCredentials,
		FederationHTTPClient:               hub.Client(),
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, nodeServer) })

	descriptor, err := nodeServer.providerSource.GetDiffDescriptor(
		t.Context(), pullapi.ItemIdentity{
			Provider: "github", PlatformHost: "github.com",
			Owner: "acme", Name: "widget", Number: 42,
		},
	)
	require.NoError(err)
	assert.Equal("head-sha", descriptor.DiffHeadSHA)
	assert.Equal("base-sha", descriptor.DiffBaseSHA)
	assert.Equal("merge-base", descriptor.MergeBaseSHA)
	assert.Equal(observedAt, descriptor.Repository.ObservedAt)

	observed, err := nodeDB.GetRepositoryByProviderID(
		t.Context(), platform.RepositoryIdentity{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: repo.PlatformRepoID,
		},
	)
	require.NoError(err)
	require.NotNil(observed)
	assert.Equal("https://github.com/acme/widget.git", observed.Repository.CloneURL)
	assert.Equal("main", observed.Repository.DefaultBranch)
	nodePull, err := nodeDB.GetMergeRequestByRepoIDAndNumber(
		t.Context(), observed.Repository.ID, 42,
	)
	require.NoError(err)
	assert.Nil(nodePull, "descriptors must not become a spoke-side provider item cache")
}

func TestRemoteAdHocWorkspaceCreationSeedsSpokeRepositoryCatalog(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hubDB := dbtest.Open(t)
	repoID, err := reposeed.Seed(t.Context(), hubDB, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: 1001, Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	require.NoError(hubDB.UpdateRepoProviderObservation(
		t.Context(), repoID, db.RepoProviderMetadata{
			CloneURL:      "https://github.com/acme/widget.git",
			DefaultBranch: "main",
		}, nil, nil,
	))

	hubCredentials, err := federationauth.Open(
		filepath.Join(t.TempDir(), "hub-credentials.json"),
	)
	require.NoError(err)
	token, err := hubCredentials.MintInbound(
		proxyTestNodeID, federationauth.SpokeToHubScopes(),
	)
	require.NoError(err)
	hubServer := New(hubDB, nil, nil, "/", nil, ServerOptions{
		DaemonAccess: DaemonAccessOptions{
			Token: "hub-local-secret", RequireAPIAuth: true,
		},
		FederationSpokeID:                  proxyTestHubID,
		FederationCredentials:              hubCredentials,
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, hubServer) })
	hub := httptest.NewTLSServer(hubServer)
	t.Cleanup(hub.Close)

	spokeCredentials, err := federationauth.Open(
		filepath.Join(t.TempDir(), "spoke-credentials.json"),
	)
	require.NoError(err)
	require.NoError(spokeCredentials.StoreOutbound(
		proxyTestHubID, token, federationauth.SpokeToHubScopes(),
	))
	spokeDB := dbtest.Open(t)
	spokeConfig := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{NodeID: proxyTestHubID, BaseURL: hub.URL},
	}}
	spokeServer := New(spokeDB, nil, nil, "/", spokeConfig, ServerOptions{
		FederationSpokeID:                  proxyTestNodeID,
		FederationSpokeActive:              true,
		FederationCredentials:              spokeCredentials,
		FederationHTTPClient:               hub.Client(),
		WorktreeDir:                        t.TempDir(),
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, spokeServer) })

	response := testutil.DoJSON(
		t, spokeServer, http.MethodPost,
		"/api/v1/host/github.com/repo/github/acme/widget/workspaces",
		map[string]any{"branch": "work/remote-create"})

	require.Equal(http.StatusAccepted, response.Code, response.Body.String())
	observed, err := spokeDB.GetRepositoryByProviderID(
		t.Context(), platform.RepositoryIdentity{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: 1001,
		},
	)
	require.NoError(err)
	require.NotNil(observed)
	assert.Equal("acme", observed.Repository.Owner)
	assert.Equal("widget", observed.Repository.Name)
}

func TestWorkspaceLaunchSpecRoundTripSeedsNodeRepositoryCatalog(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	hubDB := dbtest.Open(t)
	seedPR(t, hubDB, "acme", "widgets", 42)

	hubCredentials, err := federationauth.Open(
		filepath.Join(t.TempDir(), "hub-credentials.json"),
	)
	require.NoError(err)
	token, err := hubCredentials.MintInbound(
		proxyTestNodeID, federationauth.SpokeToHubScopes(),
	)
	require.NoError(err)
	hubServer := New(
		hubDB, nil, nil, "/", nil,
		ServerOptions{
			DaemonAccess: DaemonAccessOptions{
				Token: "hub-local-secret", RequireAPIAuth: true,
			},
			FederationSpokeID:                  proxyTestHubID,
			FederationCredentials:              hubCredentials,
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	issuedAt := time.Date(2026, time.August, 22, 16, 30, 0, 0, time.UTC)
	hubServer.now = func() time.Time { return issuedAt }
	t.Cleanup(func() { gracefulShutdown(t, hubServer) })
	hub := httptest.NewTLSServer(hubServer)
	t.Cleanup(hub.Close)

	spokeCredentials, err := federationauth.Open(
		filepath.Join(t.TempDir(), "spoke-credentials.json"),
	)
	require.NoError(err)
	require.NoError(spokeCredentials.StoreOutbound(
		proxyTestHubID, token, federationauth.SpokeToHubScopes(),
	))
	nodeDB := dbtest.Open(t)
	nodeConfig := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{
			NodeID: proxyTestHubID, BaseURL: hub.URL,
		},
	}}
	nodeServer := New(nodeDB, nil, nil, "/", nodeConfig, ServerOptions{
		FederationSpokeID:     proxyTestNodeID,
		FederationSpokeActive: true,
		FederationCredentials: spokeCredentials,
		FederationHTTPClient:  hub.Client(),
		Clones: gitclone.New(t.TempDir(), descriptorCloneRoutes{
			source: testTokenSource("spoke-git-token"),
		}),
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, nodeServer) })

	spec, err := nodeServer.providerSource.ResolveWorkspaceLaunchSpec(
		t.Context(), providerplane.WorkspaceLaunchRequest{
			Repository: providerplane.RepositoryRoute{
				Provider: "github", PlatformHost: "github.com",
				Owner: "acme", Name: "widgets",
			},
			ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		},
	)
	require.NoError(err)
	assert.Equal(testutil.FixtureRepoID("acme", "widgets"), spec.Repository.PlatformRepoID)
	assert.Equal(issuedAt, spec.IssuedAt)

	observed, err := nodeDB.GetRepositoryByProviderID(
		t.Context(), platform.RepositoryIdentity{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: testutil.FixtureRepoID("acme", "widgets"),
		},
	)
	require.NoError(err)
	require.NotNil(observed)
	assert.Equal("https://github.com/acme/widgets.git", observed.Repository.CloneURL)
	assert.Equal("main", observed.Repository.DefaultBranch)
	nodePull, err := nodeDB.GetMergeRequestByRepoIDAndNumber(
		t.Context(), observed.Repository.ID, 42,
	)
	require.NoError(err)
	assert.Nil(nodePull, "launch facts must not become a spoke-side provider item cache")

	credentialless := &hubProviderSource{
		client: nodeServer.providerSource.client,
		db:     dbtest.Open(t),
		clones: gitclone.New(t.TempDir(), nil),
	}
	_, err = credentialless.ResolveWorkspaceLaunchSpec(
		t.Context(), providerplane.WorkspaceLaunchRequest{
			Repository: providerplane.RepositoryRoute{
				Provider: "github", PlatformHost: "github.com",
				Owner: "acme", Name: "widgets",
			},
			ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		},
	)
	require.Error(err)
	problem, ok := errors.AsType[*httpapi.ProblemError](err)
	require.True(ok)
	assert.Equal(httpapi.CodeGitCredentialUnavailable, problem.Code)
}

func TestWorkspaceLaunchSpecRequiresForkCredentialRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	issuedAt := time.Date(2026, time.August, 22, 16, 30, 0, 0, time.UTC)
	spec := db.WorkspaceLaunchSpec{
		Version: db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: 1001, Owner: "acme", Name: "widget",
			CloneURL: "https://github.com/acme/widget.git", DefaultBranch: "main",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		ItemKey: "42", GitHeadRef: "feature/fork",
		Pull: &db.WorkspaceLaunchPull{
			HeadBranch: "feature/fork", HeadRepoKind: "fork",
			HeadRepoCloneURL: "https://github.com/contributor/widget.git",
			SnapshotRevision: 1,
		},
		SourceVisible: true, IssuedAt: issuedAt,
		SourceVisibleUntil: issuedAt.Add(db.WorkspaceLaunchSpecVisibilityLease),
	}
	encoded, err := json.Marshal(spec)
	require.NoError(err)
	source := &hubProviderSource{
		client: providerPlaneClientFunc(func(
			_ context.Context, _ federationauth.Scope, _ *http.Request,
		) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(encoded)),
			}, nil
		}),
		clones: gitclone.New(t.TempDir(), descriptorCloneRoutes{
			source: testTokenSource("spoke-git-token"),
		}),
	}

	_, err = source.ResolveWorkspaceLaunchSpec(
		t.Context(), providerplane.WorkspaceLaunchRequest{
			Repository: providerplane.RepositoryRoute{
				Provider: "github", PlatformHost: "github.com",
				Owner: "acme", Name: "widget",
			},
			ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		},
	)
	require.Error(err)
	problem, ok := errors.AsType[*httpapi.ProblemError](err)
	require.True(ok)
	assert.Equal(httpapi.CodeGitCredentialUnavailable, problem.Code)
	assert.Equal("contributor/widget", problem.Details["repoPath"])
}

func TestNodeCloneReadsRequireFreshDescriptorAndComputeLocally(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hubDB := dbtest.Open(t)
	seedPR(t, hubDB, "acme", "widgets", 1)
	diffRepo, err := testutil.SetupDiffRepo(t.Context(), t.TempDir(), hubDB)
	require.NoError(err)
	const hostedCloneURL = "https://github.com/acme/widgets.git"
	sourceClone, err := diffRepo.Manager.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), diffRepo.PlatformRepoID),
		"github", "github.com", "acme", "widgets",
	)
	require.NoError(err)
	repository, err := hubDB.GetRepositoryByProviderID(
		t.Context(), platform.RepositoryIdentity{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: diffRepo.PlatformRepoID,
		},
	)
	require.NoError(err)
	require.NotNil(repository)
	require.NoError(hubDB.UpdateRepoProviderObservation(
		t.Context(), repository.Repository.ID, db.RepoProviderMetadata{
			WebURL: "https://github.com/acme/widgets", CloneURL: hostedCloneURL,
			DefaultBranch: "main",
		}, nil, nil,
	))

	hubCredentials, err := federationauth.Open(
		t.TempDir() + "/hub-credentials.json",
	)
	require.NoError(err)
	token, err := hubCredentials.MintInbound(
		proxyTestNodeID, federationauth.SpokeToHubScopes(),
	)
	require.NoError(err)
	hubServer := New(
		hubDB, nil, nil, "/", nil,
		ServerOptions{
			DaemonAccess: DaemonAccessOptions{
				Token: "hub-local-secret", RequireAPIAuth: true,
			},
			FederationSpokeID:                  proxyTestHubID,
			FederationCredentials:              hubCredentials,
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	observedAt := time.Date(2026, time.August, 22, 15, 0, 0, 0, time.UTC)
	hubServer.now = func() time.Time { return observedAt }
	t.Cleanup(func() { gracefulShutdown(t, hubServer) })
	hub := httptest.NewTLSServer(hubServer)

	spokeCredentials, err := federationauth.Open(t.TempDir() + "/spoke-credentials.json")
	require.NoError(err)
	require.NoError(spokeCredentials.StoreOutbound(
		proxyTestHubID, token, federationauth.SpokeToHubScopes(),
	))
	nodeConfig := &config.Config{Fleet: config.Fleet{
		Enabled: true, Role: config.FleetRoleSpoke,
		Hub: &config.FleetHub{
			NodeID: proxyTestHubID, BaseURL: hub.URL,
		},
	}}
	nodeDB := dbtest.Open(t)
	nodeClones := gitclone.New(
		filepath.Join(t.TempDir(), "spoke-clones"),
		descriptorCloneRoutes{source: testTokenSource("spoke-git-token")},
	)
	nodeClone, err := nodeClones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), diffRepo.PlatformRepoID),
		"github", "github.com", "acme", "widgets",
	)
	require.NoError(err)
	seedNodeClone := func(target string) {
		require.NoError(os.MkdirAll(filepath.Dir(target), 0o755))
		_, stderr, runErr := gitcmd.New().Run(
			t.Context(), "", nil, "clone", "--bare", sourceClone, target,
		)
		require.NoError(runErr, string(stderr))
		_, stderr, runErr = gitcmd.New().Run(
			t.Context(), target, nil, "config", "remote.origin.url", hostedCloneURL,
		)
		require.NoError(runErr, string(stderr))
		_, stderr, runErr = gitcmd.New().Run(
			t.Context(), target, nil, "config", "--add",
			"url."+sourceClone+".insteadOf", hostedCloneURL,
		)
		require.NoError(runErr, string(stderr))
		_, stderr, runErr = gitcmd.New().Run(
			t.Context(), target, nil, "update-ref",
			"refs/remotes/origin/main", "refs/heads/main",
		)
		require.NoError(runErr, string(stderr))
		_, stderr, runErr = gitcmd.New().Run(
			t.Context(), target, nil, "symbolic-ref",
			"refs/remotes/origin/HEAD", "refs/remotes/origin/main",
		)
		require.NoError(runErr, string(stderr))
	}
	seedNodeClone(nodeClone)
	browserNamespaceInput := "github\x00github.com\x00acme/widgets\x00" + strconv.FormatInt(diffRepo.PlatformRepoID, 10)
	browserNamespaceSum := sha256.Sum256([]byte(browserNamespaceInput))
	browserClone, err := nodeClones.ClonePathInNamespace(
		"repo-browser-"+hex.EncodeToString(browserNamespaceSum[:8]),
		"github.com", "acme", "widgets",
	)
	require.NoError(err)
	seedNodeClone(browserClone)
	nodeServer := New(nodeDB, nil, nil, "/", nodeConfig, ServerOptions{
		FederationSpokeID:                  proxyTestNodeID,
		FederationSpokeActive:              true,
		FederationCredentials:              spokeCredentials,
		FederationHTTPClient:               hub.Client(),
		Clones:                             nodeClones,
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, nodeServer) })

	for _, path := range []string{
		"/api/v1/pulls/github/acme/widgets/1/diff",
		"/api/v1/pulls/github/acme/widgets/1/commits",
		"/api/v1/pulls/github/acme/widgets/1/files",
		"/api/v1/pulls/github/acme/widgets/1/file-preview?path=internal/cache.go",
		"/api/v1/repo/github/acme/widgets/browser/refs",
	} {
		response := testutil.DoJSON(t, nodeServer, http.MethodGet, path, nil)
		require.Equal(http.StatusOK, response.Code, response.Body.String())
	}

	observed, err := nodeDB.GetRepositoryByProviderID(
		t.Context(), platform.RepositoryIdentity{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: diffRepo.PlatformRepoID,
		},
	)
	require.NoError(err)
	require.NotNil(observed)
	nodePull, err := nodeDB.GetMergeRequestByRepoIDAndNumber(
		t.Context(), observed.Repository.ID, 1,
	)
	require.NoError(err)
	assert.Nil(nodePull)

	credentiallessDB := dbtest.Open(t)
	credentiallessNode := New(
		credentiallessDB, nil, nil, "/", nodeConfig,
		ServerOptions{
			FederationSpokeID:                  proxyTestNodeID,
			FederationSpokeActive:              true,
			FederationCredentials:              spokeCredentials,
			FederationHTTPClient:               hub.Client(),
			Clones:                             gitclone.New(t.TempDir(), nil),
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	t.Cleanup(func() { gracefulShutdown(t, credentiallessNode) })
	credentialProblem := testutil.DoJSON(
		t, credentiallessNode, http.MethodGet,
		"/api/v1/pulls/github/acme/widgets/1/diff", nil)

	require.Equal(http.StatusServiceUnavailable, credentialProblem.Code)
	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(credentialProblem.Body).Decode(&problem))
	assert.Equal(httpapi.CodeGitCredentialUnavailable, problem.Code)
	credentialBrowserProblem := testutil.DoJSON(
		t, credentiallessNode, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/refs", nil)

	require.Equal(http.StatusServiceUnavailable, credentialBrowserProblem.Code)
	require.NoError(json.NewDecoder(credentialBrowserProblem.Body).Decode(&problem))
	assert.Equal(httpapi.CodeGitCredentialUnavailable, problem.Code)

	// Graceful hub shutdown closes long-lived federation streams
	// before the listener drains; mirror that ordering here before simulating
	// the outage with httptest.Server.Close.
	hubServer.Hub().Close()
	hub.Close()
	outage := testutil.DoJSON(
		t, nodeServer, http.MethodGet,
		"/api/v1/pulls/github/acme/widgets/1/diff", nil)

	require.Equal(http.StatusServiceUnavailable, outage.Code, outage.Body.String())
	require.NoError(json.NewDecoder(outage.Body).Decode(&problem))
	assert.Equal(httpapi.CodeHubUnavailable, problem.Code)
	browserOutage := testutil.DoJSON(
		t, nodeServer, http.MethodGet,
		"/api/v1/repo/github/acme/widgets/browser/refs", nil)

	require.Equal(http.StatusServiceUnavailable, browserOutage.Code)
	require.NoError(json.NewDecoder(browserOutage.Body).Decode(&problem))
	assert.Equal(httpapi.CodeHubUnavailable, problem.Code)

	localCtx := gitclone.WithRepositoryIdentity(
		t.Context(), diffRepo.PlatformRepoID,
	)
	localDiff, err := nodeClones.Diff(
		localCtx, "github", "github.com", "acme", "widgets",
		diffRepo.BaseSHA, diffRepo.HeadSHA, false,
	)
	require.NoError(err)
	assert.NotEmpty(localDiff.Files,
		"workspace-local Git reads remain available without the hub")
}

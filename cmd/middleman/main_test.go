package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	gh "github.com/google/go-github/v84/github"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/cli/serve"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/runtimelock"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/tokenauth"
)

func TestMain(m *testing.M) {
	if os.Getenv("TELEMETRY_ENABLED") == "" {
		if err := os.Setenv("TELEMETRY_ENABLED", "0"); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}

func TestConfigureLoggingRedactsTokens(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
	var buf bytes.Buffer

	closeLog, err := configureLogging(&buf)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(closeLog()) })

	slog.Error(
		"request failed with ghp_message_secret",
		"err", errors.New("https://x-access-token:ghp_error_secret@github.com/acme/widgets.git failed"),
		"token", "plain-provider-secret",
	)

	out := buf.String()
	require.NotEmpty(out)
	for _, secret := range []string{
		"ghp_message_secret",
		"ghp_error_secret",
		"plain-provider-secret",
		"x-access-token",
	} {
		assert.NotContains(out, secret)
	}
	assert.Contains(out, "[REDACTED]")
}

func TestConfigureLoggingRedactsTokensInConfiguredLogFile(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
	var stderr bytes.Buffer
	logFile := filepath.Join(t.TempDir(), "middleman.log")
	t.Setenv("MIDDLEMAN_LOG_FILE", logFile)

	closeLog, err := configureLogging(&stderr)
	require.NoError(err)

	slog.Error(
		"request failed with glpat-message-secret",
		"err", errors.New("https://oauth2:glpat_url_secret@gitlab.example.com/acme/widgets.git failed"),
		"authorization", "Bearer plain-provider-secret",
	)
	require.NoError(closeLog())

	fileOut, err := os.ReadFile(logFile)
	require.NoError(err)
	for _, out := range []string{stderr.String(), string(fileOut)} {
		require.NotEmpty(out)
		for _, secret := range []string{
			"glpat-message-secret",
			"glpat_url_secret",
			"plain-provider-secret",
			"oauth2",
		} {
			assert.NotContains(out, secret)
		}
		assert.Contains(out, "[REDACTED]")
	}
}

func mainTestTokenSource(
	t *testing.T,
	platformName, host, envName, token string,
) tokenauth.Source {
	t.Helper()
	t.Setenv(envName, token)
	return tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: platformName, Host: host},
		Candidates: []tokenauth.Candidate{{
			Kind:    tokenauth.SourceKindEnv,
			EnvName: envName,
		}},
	}, tokenauth.Options{})
}

func TestWriteRuntimeMetadataRecordsBoundTCPPort(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	dataDir := t.TempDir()
	lockHandle, err := runtimelock.Acquire(dataDir)
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(lockHandle.Release())
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(ln.Close())
	})

	require.NoError(writeRuntimeMetadata(lockHandle, ln))

	data, err := os.ReadFile(runtimelock.MetadataPath(dataDir))
	require.NoError(err)
	var meta runtimelock.Metadata
	require.NoError(json.Unmarshal(data, &meta))
	tcpAddr := ln.Addr().(*net.TCPAddr)
	assert.Equal(tcpAddr.Port, meta.Port)
	assert.Equal("127.0.0.1", meta.Host)
	assert.Equal(ln.Addr().String(), meta.ListenAddr)
}

func TestWriteRuntimeMetadataRejectsNonTCPListener(t *testing.T) {
	dataDir := t.TempDir()
	lockHandle, err := runtimelock.Acquire(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, lockHandle.Release())
	})

	err = writeRuntimeMetadata(lockHandle, fakeListener{addr: fakeAddr("not-an-address")})
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "non-TCP")
}

func TestResolveStartupReposExpandsConfiguredGlobs(t *testing.T) {
	assert := Assert.New(t)
	cfg := &config.Config{
		Repos: []config.Repo{{Owner: "roborev-dev", Name: "*"}},
	}
	client := &testutil.FixtureClient{
		ReposByOwner: map[string][]*gh.Repository{
			"roborev-dev": {
				{
					Name:     new("middleman"),
					Archived: new(false),
				},
				{
					Name:     new("archived"),
					Archived: new(true),
				},
			},
		},
	}

	repos := resolveStartupRepos(
		t.Context(),
		cfg,
		mustProviderRegistry(t, map[string]ghclient.Client{"github.com": client}),
		nil,
	)

	assert.Equal([]ghclient.RepoRef{{
		Owner:        "roborev-dev",
		Name:         "middleman",
		PlatformHost: "github.com",
		RepoPath:     "roborev-dev/middleman",
	}}, repos)
}

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }

func (a fakeAddr) String() string { return string(a) }

type fakeListener struct {
	addr net.Addr
}

func (l fakeListener) Accept() (net.Conn, error) { return nil, errors.New("not implemented") }

func (l fakeListener) Close() error { return nil }

func (l fakeListener) Addr() net.Addr { return l.addr }

func TestResolveStartupReposKeepsExactReposWhenResolutionFails(t *testing.T) {
	assert := Assert.New(t)
	cfg := &config.Config{
		Repos: []config.Repo{{Owner: "roborev-dev", Name: "middleman"}},
	}

	repos := resolveStartupRepos(
		t.Context(),
		cfg,
		mustProviderRegistry(t, nil),
		nil,
	)

	assert.Equal([]ghclient.RepoRef{{
		Owner:        "roborev-dev",
		Name:         "middleman",
		PlatformHost: "github.com",
		RepoPath:     "roborev-dev/middleman",
	}}, repos)
}

func TestResolveStartupReposFallsBackToDBForOfflineGlobs(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)

	ctx := t.Context()
	_, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	_, err = database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "tools"))
	require.NoError(err)

	cfg := &config.Config{
		Repos: []config.Repo{{Owner: "acme", Name: "*"}},
	}

	repos := resolveStartupRepos(
		ctx, cfg, mustProviderRegistry(t, nil), database,
	)

	assert.Len(repos, 2)
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	assert.ElementsMatch([]string{"widgets", "tools"}, names)
}

func TestResolveStartupReposUsesProviderRegistryForGitLab(t *testing.T) {
	assert := Assert.New(t)
	cfg := &config.Config{
		Repos: []config.Repo{{
			Platform:     "gitlab",
			PlatformHost: "gitlab.com",
			Owner:        "group/subgroup",
			Name:         "project",
		}},
	}
	registry := mustProviderRegistry(t, nil, mainTestRepositoryReader{
		kind: platform.KindGitLab,
		host: "gitlab.com",
	})

	repos := resolveStartupRepos(t.Context(), cfg, registry, nil)

	assert.Equal([]ghclient.RepoRef{{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.com",
		Owner:        "group/subgroup",
		Name:         "project",
		RepoPath:     "group/subgroup/project",
	}}, repos)
}

func TestValidateProviderHostKeysRejectsMixedProvidersOnSameHostWithDifferentTokens(t *testing.T) {
	assert := Assert.New(t)
	err := validateProviderHostKeys(map[string]string{
		providerHostKey("github", "code.example.com"): "github-token",
		providerHostKey("gitlab", "code.example.com"): "gitlab-token",
	})

	require.Error(t, err)
	assert.Contains(err.Error(), "code.example.com")
}

func TestValidateProviderHostKeysAllowsMixedProvidersOnSameHostWithSameToken(t *testing.T) {
	err := validateProviderHostKeys(map[string]string{
		providerHostKey("github", "code.example.com"): "shared-token",
		providerHostKey("gitlab", "code.example.com"): "shared-token",
	})

	require.NoError(t, err)
}

func TestValidateProviderHostKeysRejectsMixedProviderSourcesOnSameHost(t *testing.T) {
	assert := Assert.New(t)
	host := "code.example.com"
	err := validateProviderHostKeys(map[string]tokenauth.Source{
		providerHostKey("github", host): tokenauth.NewManagedSource(
			tokenauth.Descriptor{
				Key: tokenauth.Key{Platform: "github", Host: host},
				Candidates: []tokenauth.Candidate{{
					Kind:    tokenauth.SourceKindEnv,
					EnvName: "GITHUB_TOKEN",
				}},
			},
			tokenauth.Options{},
		),
		providerHostKey("gitlab", host): tokenauth.NewManagedSource(
			tokenauth.Descriptor{
				Key: tokenauth.Key{Platform: "gitlab", Host: host},
				Candidates: []tokenauth.Candidate{{
					Kind:    tokenauth.SourceKindEnv,
					EnvName: "GITLAB_TOKEN",
				}},
			},
			tokenauth.Options{},
		),
	})

	require.Error(t, err)
	assert.Contains(err.Error(), host)
}

func TestValidateProviderHostKeysAllowsMixedProviderSourcesOnSameHostWithSameDescriptor(t *testing.T) {
	host := "code.example.com"
	githubSource := tokenauth.NewManagedSource(
		tokenauth.Descriptor{
			Key: tokenauth.Key{Platform: "github", Host: host},
			Candidates: []tokenauth.Candidate{{
				Kind:    tokenauth.SourceKindEnv,
				EnvName: "SHARED_TOKEN",
			}},
		},
		tokenauth.Options{},
	)
	gitlabSource := tokenauth.NewManagedSource(
		tokenauth.Descriptor{
			Key: tokenauth.Key{Platform: "gitlab", Host: host},
			Candidates: []tokenauth.Candidate{{
				Kind:    tokenauth.SourceKindEnv,
				EnvName: "SHARED_TOKEN",
			}},
		},
		tokenauth.Options{},
	)

	err := validateProviderHostKeys(map[string]tokenauth.Source{
		providerHostKey("github", host): githubSource,
		providerHostKey("gitlab", host): gitlabSource,
	})

	require.NoError(t, err)
}

func TestValidateProviderHostKeysAllowsEquivalentSourceChainsOnSameHost(t *testing.T) {
	host := "code.example.com"
	// A repo-level override that repeats the platform fallback yields the
	// chain env:SHARED -> env:SHARED, which resolves identically to a plain
	// env:SHARED. The canonical comparison must treat them as the same clone
	// token instead of reporting a spurious conflict.
	repeated := tokenauth.NewManagedSource(
		tokenauth.Descriptor{
			Key: tokenauth.Key{Platform: "github", Host: host},
			Candidates: []tokenauth.Candidate{
				{Kind: tokenauth.SourceKindEnv, EnvName: "SHARED_TOKEN"},
				{Kind: tokenauth.SourceKindEnv, EnvName: "SHARED_TOKEN"},
			},
		},
		tokenauth.Options{},
	)
	single := tokenauth.NewManagedSource(
		tokenauth.Descriptor{
			Key: tokenauth.Key{Platform: "gitlab", Host: host},
			Candidates: []tokenauth.Candidate{{
				Kind:    tokenauth.SourceKindEnv,
				EnvName: "SHARED_TOKEN",
			}},
		},
		tokenauth.Options{},
	)

	err := validateProviderHostKeys(map[string]tokenauth.Source{
		providerHostKey("github", host): repeated,
		providerHostKey("gitlab", host): single,
	})

	require.NoError(t, err)
}

func TestDefaultProviderFactoriesRegisterForgejoAndGitea(t *testing.T) {
	factories := defaultProviderFactories()

	assert := Assert.New(t)
	assert.Contains(factories, string(platform.KindForgejo))
	assert.Contains(factories, string(platform.KindGitea))
}

func TestBuildProviderStartupKeepsForgeProviderHostsDistinct(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	database := dbtest.Open(t)

	callsByProvider := map[string][]providerFactoryInput{}
	factories := map[string]providerFactory{
		string(platform.KindForgejo): func(input providerFactoryInput) (providerFactoryOutput, error) {
			callsByProvider[string(platform.KindForgejo)] = append(
				callsByProvider[string(platform.KindForgejo)], input,
			)
			return providerFactoryOutput{provider: mainTestRepositoryReader{
				kind: platform.KindForgejo,
				host: input.host,
			}}, nil
		},
		string(platform.KindGitea): func(input providerFactoryInput) (providerFactoryOutput, error) {
			callsByProvider[string(platform.KindGitea)] = append(
				callsByProvider[string(platform.KindGitea)], input,
			)
			return providerFactoryOutput{provider: mainTestRepositoryReader{
				kind: platform.KindGitea,
				host: input.host,
			}}, nil
		},
	}

	set := tokenauth.NewSourceSet(tokenauth.Options{})
	startup, err := buildProviderStartup(
		database,
		&config.Config{SyncBudgetPerHour: 200},
		set,
		map[string]tokenauth.Source{
			providerHostKey(string(platform.KindForgejo), "codeberg.org"): mainTestTokenSource(
				t, string(platform.KindForgejo), "codeberg.org", "FORGEJO_TEST_TOKEN", "codeberg-token",
			),
			providerHostKey(string(platform.KindGitea), "gitea.example.com"): mainTestTokenSource(
				t, string(platform.KindGitea), "gitea.example.com", "GITEA_TEST_TOKEN", "gitea-token",
			),
		},
		factories,
	)
	require.NoError(err)

	forgejoCalls := callsByProvider[string(platform.KindForgejo)]
	giteaCalls := callsByProvider[string(platform.KindGitea)]
	require.Len(forgejoCalls, 1)
	require.Len(giteaCalls, 1)
	assert.Equal("codeberg.org", forgejoCalls[0].host)
	forgejoFactoryToken, err := forgejoCalls[0].tokenSource.Token(t.Context())
	require.NoError(err)
	assert.Equal("codeberg-token", forgejoFactoryToken)
	assert.Equal("gitea.example.com", giteaCalls[0].host)
	giteaFactoryToken, err := giteaCalls[0].tokenSource.Token(t.Context())
	require.NoError(err)
	assert.Equal("gitea-token", giteaFactoryToken)
	assert.NotSame(forgejoCalls[0].rateTracker, giteaCalls[0].rateTracker)
	assert.NotSame(forgejoCalls[0].budget, giteaCalls[0].budget)
	forgejoCloneSource := startup.cloneAuth["codeberg.org"]
	giteaCloneSource := startup.cloneAuth["gitea.example.com"]
	require.NotNil(forgejoCloneSource)
	require.NotNil(giteaCloneSource)
	forgejoToken, err := forgejoCloneSource.Token(t.Context())
	require.NoError(err)
	giteaToken, err := giteaCloneSource.Token(t.Context())
	require.NoError(err)
	assert.Equal("codeberg-token", forgejoToken)
	assert.Equal("gitea-token", giteaToken)
	assert.Same(
		forgejoCalls[0].tokenSource,
		startup.cloneSources[tokenauth.Key{Platform: string(platform.KindForgejo), Host: "codeberg.org"}],
	)
	assert.Same(
		giteaCalls[0].tokenSource,
		startup.cloneSources[tokenauth.Key{Platform: string(platform.KindGitea), Host: "gitea.example.com"}],
	)
	// Clone auth is the dedicated host-level source registered in the
	// SourceSet, not the provider's own source, so config reload can
	// re-point it via tokenauth.CloneKey.
	forgejoCloneManaged, ok := set.Get(tokenauth.CloneKey("codeberg.org"))
	require.True(ok)
	assert.Same(forgejoCloneManaged, forgejoCloneSource)
	giteaCloneManaged, ok := set.Get(tokenauth.CloneKey("gitea.example.com"))
	require.True(ok)
	assert.Same(giteaCloneManaged, giteaCloneSource)

	forgejoReader, err := startup.registry.RepositoryReader(platform.KindForgejo, "codeberg.org")
	require.NoError(err)
	giteaReader, err := startup.registry.RepositoryReader(platform.KindGitea, "gitea.example.com")
	require.NoError(err)
	assert.NotNil(forgejoReader)
	assert.NotNil(giteaReader)
}

func TestBuildProviderStartupUsesRegisteredFactoryForFutureProvider(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	database := dbtest.Open(t)

	called := false
	set := tokenauth.NewSourceSet(tokenauth.Options{})
	codebergSource := mainTestTokenSource(
		t, "codeberg", "codeberg.org", "CODEBERG_TEST_TOKEN", "codeberg-token",
	)
	startup, err := buildProviderStartup(
		database,
		&config.Config{},
		set,
		map[string]tokenauth.Source{
			providerHostKey("codeberg", "codeberg.org"): codebergSource,
		},
		map[string]providerFactory{
			"codeberg": func(input providerFactoryInput) (providerFactoryOutput, error) {
				called = true
				assert.Equal("codeberg.org", input.host)
				token, err := input.tokenSource.Token(t.Context())
				require.NoError(err)
				assert.Equal("codeberg-token", token)
				return providerFactoryOutput{
					provider: mainTestRepositoryReader{
						kind: platform.Kind("codeberg"),
						host: input.host,
					},
				}, nil
			},
		},
	)
	require.NoError(err)
	assert.True(called)
	src := startup.cloneAuth["codeberg.org"]
	require.NotNil(src)
	token, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("codeberg-token", token)
	assert.Same(
		codebergSource,
		startup.cloneSources[tokenauth.Key{Platform: "codeberg", Host: "codeberg.org"}],
	)
	cloneManaged, ok := set.Get(tokenauth.CloneKey("codeberg.org"))
	require.True(ok)
	assert.Same(cloneManaged, src)

	reader, err := startup.registry.RepositoryReader(platform.Kind("codeberg"), "codeberg.org")
	require.NoError(err)
	assert.NotNil(reader)
}

func TestBuildProviderStartupSharedHostCloneAuthUsesHostLevelSource(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	database := dbtest.Open(t)
	t.Setenv("SHARED_FORGE_TOKEN", "shared-token")
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")

	// Two providers on one host with the same credential chain, mirroring
	// the only multi-provider-per-host layout startup validation accepts.
	cfg := &config.Config{
		Platforms: []config.PlatformConfig{
			{
				Type:     string(platform.KindForgejo),
				Host:     "code.example.com",
				TokenEnv: "SHARED_FORGE_TOKEN",
			},
			{
				Type:     string(platform.KindGitea),
				Host:     "code.example.com",
				TokenEnv: "SHARED_FORGE_TOKEN",
			},
		},
	}
	set := tokenauth.NewSourceSet(tokenauth.Options{})
	providerSources, err := collectProviderTokenSources(t.Context(), cfg, set)
	require.NoError(err)
	require.Len(providerSources, 2)

	factories := map[string]providerFactory{
		string(platform.KindForgejo): func(input providerFactoryInput) (providerFactoryOutput, error) {
			return providerFactoryOutput{provider: mainTestRepositoryReader{
				kind: platform.KindForgejo,
				host: input.host,
			}}, nil
		},
		string(platform.KindGitea): func(input providerFactoryInput) (providerFactoryOutput, error) {
			return providerFactoryOutput{provider: mainTestRepositoryReader{
				kind: platform.KindGitea,
				host: input.host,
			}}, nil
		},
	}
	startup, err := buildProviderStartup(database, cfg, set, providerSources, factories)
	require.NoError(err)

	// Clone auth must be the host-level source under tokenauth.CloneKey,
	// not whichever provider source map iteration yielded first: reload
	// updates the host key from the config's effective per-host chain, so
	// pointing git at a provider source would detach clone auth from
	// reload whenever that provider entry is removed or loses its token.
	cloneSource := startup.cloneAuth["code.example.com"]
	require.NotNil(cloneSource)
	cloneManaged, ok := set.Get(tokenauth.CloneKey("code.example.com"))
	require.True(ok)
	assert.Same(cloneManaged, cloneSource)
	forgejoKey := providerHostKey(string(platform.KindForgejo), "code.example.com")
	giteaKey := providerHostKey(string(platform.KindGitea), "code.example.com")
	assert.NotSame(providerSources[forgejoKey], cloneSource)
	assert.NotSame(providerSources[giteaKey], cloneSource)
	token, err := cloneSource.Token(t.Context())
	require.NoError(err)
	assert.Equal("shared-token", token)
}

func TestStartupFallbackKeepsPersistedGlobMatchesInAPIs(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	database := dbtest.Open(t)

	_, err := database.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("github.com", "roborev-dev", "middleman"),
	)
	require.NoError(err)
	_, err = database.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("github.com", "roborev-dev", "worker"),
	)
	require.NoError(err)

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := &config.Config{
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Host:           "127.0.0.1",
		Port:           8091,
		BasePath:       "/",
		DataDir:        dir,
		Repos: []config.Repo{
			{Owner: "roborev-dev", Name: "*"},
		},
		Activity: config.Activity{
			ViewMode:  "flat",
			TimeRange: "7d",
		},
	}
	require.NoError(cfg.Save(cfgPath))

	client := &testutil.FixtureClient{
		ListRepositoriesByOwnerFn: func(
			context.Context, string,
		) ([]*gh.Repository, error) {
			return nil, errors.New("offline")
		},
	}
	repos := resolveStartupRepos(
		t.Context(),
		cfg,
		mustProviderRegistry(t, map[string]ghclient.Client{"github.com": client}),
		database,
	)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": client},
		database, nil, repos, 0, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{},
	)

	reposReq := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
	reposReq.Host = "127.0.0.1:8091"
	reposRR := httptest.NewRecorder()
	srv.ServeHTTP(reposRR, reposReq)
	require.Equal(http.StatusOK, reposRR.Code, reposRR.Body.String())

	var listed []struct {
		Owner string `json:"owner"`
		Name  string `json:"name"`
	}
	require.NoError(json.NewDecoder(reposRR.Body).Decode(&listed))
	require.Len(listed, 2)
	assert.ElementsMatch([]string{"middleman", "worker"}, []string{
		listed[0].Name,
		listed[1].Name,
	})

	settingsReq := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	settingsReq.Host = "127.0.0.1:8091"
	settingsRR := httptest.NewRecorder()
	srv.ServeHTTP(settingsRR, settingsReq)
	require.Equal(http.StatusOK, settingsRR.Code, settingsRR.Body.String())

	var settings struct {
		Repos []struct {
			Owner            string `json:"owner"`
			Name             string `json:"name"`
			MatchedRepoCount int    `json:"matched_repo_count"`
		} `json:"repos"`
	}
	require.NoError(json.NewDecoder(settingsRR.Body).Decode(&settings))
	require.Len(settings.Repos, 1)
	assert.Equal("roborev-dev", settings.Repos[0].Owner)
	assert.Equal("*", settings.Repos[0].Name)
	assert.Equal(2, settings.Repos[0].MatchedRepoCount)
}

func mustProviderRegistry(
	t *testing.T,
	clients map[string]ghclient.Client,
	providers ...platform.Provider,
) *platform.Registry {
	t.Helper()
	registry, err := ghclient.NewProviderRegistry(clients, providers...)
	require.NoError(t, err)
	return registry
}

type mainTestRepositoryReader struct {
	kind platform.Kind
	host string
}

func (r mainTestRepositoryReader) Platform() platform.Kind {
	return r.kind
}

func (r mainTestRepositoryReader) Host() string {
	return r.host
}

func (r mainTestRepositoryReader) Capabilities() platform.Capabilities {
	return platform.Capabilities{ReadRepositories: true}
}

func (r mainTestRepositoryReader) GetRepository(
	_ context.Context,
	ref platform.RepoRef,
) (platform.Repository, error) {
	return platform.Repository{Ref: ref}, nil
}

func (r mainTestRepositoryReader) ListRepositories(
	_ context.Context,
	owner string,
	_ platform.RepositoryListOptions,
) ([]platform.Repository, error) {
	return []platform.Repository{{
		Ref: platform.RepoRef{
			Platform: r.kind,
			Host:     r.host,
			Owner:    owner,
			Name:     "project",
			RepoPath: owner + "/project",
		},
	}}, nil
}

func TestRunCLIConfigReadPort(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, []byte("port = 9123\n"), 0o644))

	var stdout bytes.Buffer
	err := runCLI([]string{"config", "read", "-config", cfgPath, "port"}, &stdout)
	require.NoError(err)
	assert.Equal("9123\n", stdout.String())
}

func TestRunCLIConfigReadPortCreatesDefaultConfig(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	var stdout bytes.Buffer
	err := runCLI([]string{"config", "read", "-config", cfgPath, "port"}, &stdout)
	require.NoError(err)
	assert.Equal("8091\n", stdout.String())

	content, err := os.ReadFile(cfgPath)
	require.NoError(err)
	assert.Contains(string(content), "port = 8091")
}

func TestRunCLIDefaultsToServe(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	original := runServer
	t.Cleanup(func() { runServer = original })
	var got serve.Options
	runServer = func(opts serve.Options) error {
		got = opts
		return nil
	}

	var stdout bytes.Buffer
	err := runCLI(nil, &stdout)

	require.NoError(err)
	assert.Equal(config.DefaultConfigPath(), got.ConfigPath)
	assert.Empty(got.ProfilerAddr)
	assert.Empty(stdout.String())
}

func TestRunCLIServeSubcommandUsesServerRunner(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	original := runServer
	t.Cleanup(func() { runServer = original })
	var got serve.Options
	runServer = func(opts serve.Options) error {
		got = opts
		return nil
	}

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	var stdout bytes.Buffer
	err := runCLI([]string{"serve", "-config", cfgPath}, &stdout)

	require.NoError(err)
	assert.Equal(cfgPath, got.ConfigPath)
	assert.Empty(got.ProfilerAddr)
	assert.Empty(stdout.String())
}

func TestRunCLIServePassesProfilerAddress(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	original := runServer
	t.Cleanup(func() { runServer = original })
	t.Setenv("MIDDLEMAN_PPROF_ADDR", "127.0.0.1:6060")
	var got serve.Options
	runServer = func(opts serve.Options) error {
		got = opts
		return nil
	}

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	var stdout bytes.Buffer
	err := runCLI([]string{
		"serve",
		"-config", cfgPath,
		"-pprof-addr", "127.0.0.1:7070",
	}, &stdout)

	require.NoError(err)
	assert.Equal(cfgPath, got.ConfigPath)
	assert.Equal("127.0.0.1:7070", got.ProfilerAddr)
	assert.Empty(stdout.String())
}

func TestRunCLIServeDefaultsProfilerAddressFromEnv(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	original := runServer
	t.Cleanup(func() { runServer = original })
	t.Setenv("MIDDLEMAN_PPROF_ADDR", "127.0.0.1:6060")
	var got serve.Options
	runServer = func(opts serve.Options) error {
		got = opts
		return nil
	}

	var stdout bytes.Buffer
	err := runCLI([]string{"serve"}, &stdout)

	require.NoError(err)
	assert.Equal(config.DefaultConfigPath(), got.ConfigPath)
	assert.Equal("127.0.0.1:6060", got.ProfilerAddr)
	assert.Empty(stdout.String())
}

func TestRunCLIControlCommandsDoNotStartServer(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	original := runServer
	t.Cleanup(func() { runServer = original })
	runServer = func(serve.Options) error {
		return errors.New("serve should not start")
	}

	var stdout bytes.Buffer
	err := runCLI([]string{"--server", "http://middleman.test", "quickstart"}, &stdout)

	require.NoError(err)
	assert.Contains(stdout.String(), `"api_base_url": "http://middleman.test/api/v1"`)
	assert.Contains(stdout.String(), "middleman api GET /pulls")
}

func TestRunCLIPtyOwnerRejectsMissingRequiredFlags(t *testing.T) {
	var stdout bytes.Buffer

	err := runCLI([]string{"pty-owner"}, &stdout)

	require.Error(t, err)
	require.Contains(t, err.Error(), "session")
}

func TestRunCLIPtyOwnerParsesBeforeServerStartup(t *testing.T) {
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")
	var stdout bytes.Buffer

	err := runCLI([]string{
		"pty-owner",
		"-root", t.TempDir(),
		"-session", "bad/session",
		"-cwd", t.TempDir(),
		"-command-json", `["sh","-c","exit 0"]`,
	}, &stdout)

	require.Error(t, err)
	require.Contains(t, err.Error(), "unsafe pty owner session")
}

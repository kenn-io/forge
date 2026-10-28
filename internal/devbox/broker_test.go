package devbox

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
)

func TestBrokerAdmissionAndCredentialProtocol(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	var member atomic.Bool
	member.Store(true)
	var permission atomic.Value
	permission.Store("write")
	var excessiveScope atomic.Bool
	var repositoryScope atomic.Value
	repositoryScope.Store("")
	var repositoryID atomic.Int64
	repositoryID.Store(42)
	var tokenRepositoryID atomic.Int64
	tokenRepositoryID.Store(42)
	var staleLookupStatus atomic.Int64
	var rejectRefreshedLookup atomic.Bool
	var revokeRefreshedMembership atomic.Bool
	var minted atomic.Int32
	var lookupMinted atomic.Int32
	var forkLookups atomic.Int32
	var installationLookups atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app/installations/23" {
			installationLookups.Add(1)
		}
		if strings.HasPrefix(r.URL.Path, "/repos/contributor-a/") {
			forkLookups.Add(1)
		}
		switch r.URL.Path {
		case "/repos/contributor-a/project-fork":
			assert.Equal("Bearer fixture-lookup-token", r.Header.Get("Authorization"))
			_, _ = fmt.Fprint(w, `{"id":143,"parent":{"name":"project-b","owner":{"id":99}},"source":{"name":"outside","owner":{"id":100}}}`)
		case "/repos/contributor-a/project":
			assert.Equal("Bearer fixture-lookup-token", r.Header.Get("Authorization"))
			writeForkMetadataFixture(w, "project", "fork-main")
		case "/repos/contributor-a/nested":
			assert.Equal("Bearer fixture-lookup-token", r.Header.Get("Authorization"))
			_, _ = fmt.Fprint(w, `{"id":144,"parent":{"name":"another-fork","owner":{"id":100,"login":"contributor-b"}},"source":{"name":"outside","owner":{"id":101}}}`)
		case "/repos/contributor-b/another-fork":
			assert.Equal("Bearer fixture-lookup-token", r.Header.Get("Authorization"))
			_, _ = fmt.Fprint(w, `{"id":147,"parent":{"name":"project","owner":{"id":99}},"source":{"name":"outside","owner":{"id":101}}}`)
		case "/repos/contributor-a/unrelated":
			_, _ = fmt.Fprint(w, `{"id":145,"parent":{"name":"outside","owner":{"id":100}},"source":{"name":"outside","owner":{"id":101}}}`)
		case "/repos/contributor-a/non-fork":
			_, _ = fmt.Fprint(w, `{"id":146,"fork":false}`)
		case "/repos/contributor-a/unavailable":
			w.WriteHeader(http.StatusInternalServerError)
		case "/app/installations/23":
			_, _ = fmt.Fprint(w, `{"id":23,"app_id":17,"account":{"id":99,"login":"example-org"},"repository_selection":"selected","permissions":{"workflows":"write"}}`)
		case "/app/installations/23/access_tokens":
			var request struct {
				Repositories  []string          `json:"repositories"`
				RepositoryIDs []int64           `json:"repository_ids"`
				Permissions   map[string]string `json:"permissions"`
			}
			if !assert.NoError(json.UnmarshalRead(r.Body, &request)) {
				return
			}
			if len(request.Repositories) == 0 {
				assert.Empty(request.RepositoryIDs)
				assert.Equal(map[string]string{"metadata": "read"}, request.Permissions)
				assert.NotEmpty(r.Header.Get("Authorization"))
				lookupMinted.Add(1)
				writeLookupTokenFixture(t, w, request.Permissions)
				return
			}
			if !assert.Len(request.Repositories, 1) {
				return
			}
			assert.Empty(request.RepositoryIDs)
			if request.Permissions["contents"] == "write" {
				assert.Equal("write", request.Permissions["workflows"])
			} else {
				assert.NotContains(request.Permissions, "workflows")
			}
			var repo githubapp.Repository
			token := "fixture-installation-token"
			switch request.Repositories[0] {
			case "project":
				repo = githubapp.Repository{ID: tokenRepositoryID.Load(), Name: "project"}
				if repo.ID == 44 {
					token = "fixture-replacement-token"
				}
			case "project-b":
				repo = githubapp.Repository{ID: 43, Name: "project-b"}
				token = "fixture-project-b-token"
			default:
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = fmt.Fprint(w, `{"message":"Repository not accessible to installation"}`)
				return
			}
			minted.Add(1)
			permissions := request.Permissions
			if excessiveScope.Load() {
				permissions["administration"] = "write"
			}
			repositories := []githubapp.Repository{repo}
			switch repositoryScope.Load() {
			case "multiple":
				repositories = append(repositories, githubapp.Repository{ID: 44, Name: "another-project"})
			case "wrong name":
				repositories[0].Name = "another-project"
			case "missing ID":
				repositories[0].ID = 0
			case "changed ID":
				if request.Permissions["contents"] != "" {
					repositories[0].ID = 44
				}
			}
			data, err := json.Marshal(githubapp.InstallationToken{
				Token: token, ExpiresAt: time.Now().Add(time.Hour),
				Repositories: repositories, Permissions: permissions,
			})
			if assert.NoError(err) {
				_, _ = w.Write(data)
			}
		case "/user/1234":
			_, _ = fmt.Fprint(w, `{"id":1234,"login":"developer-a"}`)
		case "/orgs/example-org/members/developer-a":
			if member.Load() && (!revokeRefreshedMembership.Load() || r.Header.Get("Authorization") != "Bearer fixture-replacement-token") {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case "/repos/example-org/project":
			if status := int(staleLookupStatus.Load()); status != 0 &&
				(r.Header.Get("Authorization") == "Bearer fixture-installation-token" || rejectRefreshedLookup.Load()) {
				w.WriteHeader(status)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":%d,"name":"project","owner":{"id":99},"default_branch":"main"}`, repositoryID.Load())
		case "/repos/example-org/project/collaborators/developer-a/permission":
			_, _ = fmt.Fprintf(w, `{"permission":%q,"user":{"id":1234}}`, permission.Load())
		case "/repos/example-org/project-b":
			_, _ = fmt.Fprint(w, `{"id":43,"name":"project-b","owner":{"id":99},"default_branch":"main"}`)
		case "/repos/example-org/project-b/collaborators/developer-a/permission":
			_, _ = fmt.Fprint(w, `{"permission":"write","user":{"id":1234}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(err)
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	require.NoError(os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}), 0o600))
	uid := uint32(os.Getuid())
	if uid == 0 {
		uid = 1000
	}
	broker, err := NewBroker(BrokerConfig{
		Socket: filepath.Join(t.TempDir(), "broker.sock"), AppID: 17, InstallationID: 23,
		Organization: "example-org", OrganizationID: 99, PrivateKeyFile: keyPath,
		Accounts: []Account{{UID: uid, GitHubUserID: 1234}},
	})
	require.NoError(err)
	broker.base, err = url.Parse(api.URL + "/")
	require.NoError(err)
	broker.apps = githubapp.NewClientWithBase(api.URL)
	request := CredentialRequest{Repository: "example-org/project", Profile: "git"}
	credential, err := broker.Credential(t.Context(), uid, request)
	require.NoError(err)
	assert.True(credential.Writable)
	assert.Equal(int64(1234), credential.GitHubUserID)
	assert.Equal(int64(42), credential.RepositoryID)
	assert.Equal(int32(2), minted.Load())

	_, err = broker.Credential(t.Context(), uid, request)
	require.NoError(err)
	assert.Equal(int32(2), minted.Load(), "tokens are reused after fresh authorization")
	credential, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: "example-org/project-b", Profile: "git"})
	require.NoError(err)
	assert.Equal(int64(43), credential.RepositoryID)
	assert.Equal("fixture-project-b-token", credential.Token)
	assert.Equal(int32(4), minted.Load())
	credential, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: "contributor-a/project-fork", Profile: "push"})
	require.NoError(err)
	assert.Equal("fixture-project-b-token", credential.Token)
	assert.Equal(int64(143), credential.RepositoryID)
	for _, test := range []struct {
		repository string
		id         int64
	}{
		{"contributor-a/project", 142},
		{"contributor-a/nested", 144},
	} {
		credential, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: test.repository, Profile: "push"})
		require.NoError(err, test.repository)
		assert.Equal("fixture-installation-token", credential.Token)
		assert.Equal(test.id, credential.RepositoryID)
		if test.id == 142 {
			assert.Equal("fork-main", credential.DefaultBranch)
		}
	}
	assert.Equal(int32(4), minted.Load(), "forks reuse the upstream tokens")
	assert.Equal(int32(1), lookupMinted.Load(), "fork lookups reuse their metadata token")
	for _, test := range []struct{ repository, message string }{
		{"contributor-a/non-fork", "not admitted"},
		{"contributor-a/unrelated", "not admitted"},
		{"contributor-a/missing", "not admitted"},
		{"contributor-a/unavailable", "GitHub fork unavailable"},
	} {
		before := installationLookups.Load()
		beforeMinted := minted.Load()
		_, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: test.repository, Profile: "push"})
		require.ErrorContains(err, test.message, test.repository)
		assert.Equal(before, installationLookups.Load(), "refused fork lookups make no installation lookup")
		assert.Equal(beforeMinted, minted.Load(), "refused fork lookups mint no repository token")
	}
	_, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: "example-org/not-installed", Profile: "git"})
	require.ErrorContains(err, "Repository not accessible to installation")
	assert.Equal(int32(4), minted.Load(), "an unavailable repository receives no token")
	credential, err = broker.Credential(t.Context(), uid, request)
	require.NoError(err)
	assert.Equal("fixture-installation-token", credential.Token)
	assert.Equal(int32(4), minted.Load(), "another repository must not replace the cached credential")
	repositoryID.Store(44)
	beforeMismatch := minted.Load()
	_, err = broker.Credential(t.Context(), uid, request)
	require.ErrorContains(err, "repository identity")
	assert.Equal(beforeMismatch+1, minted.Load(), "a fresh identity mismatch stops after one refresh")
	repositoryID.Store(42)
	member.Store(false)
	_, err = broker.Credential(t.Context(), uid, request)
	require.ErrorContains(err, "not a member")
	member.Store(true)
	permission.Store("read")
	credential, err = broker.Credential(t.Context(), uid, request)
	require.NoError(err)
	assert.False(credential.Writable)
	_, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: request.Repository, Profile: "push"})
	require.ErrorContains(err, "write permission")
	_, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: "contributor-a/project", Profile: "push"})
	require.ErrorContains(err, "write permission")
	_, err = broker.Credential(t.Context(), uid+1, request)
	require.ErrorContains(err, "not enrolled")
	_, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: "personal/project", Profile: "git"})
	require.ErrorContains(err, "not admitted")

	require.NoError(broker.Erase(t.Context(), uid, request.Repository))
	excessiveScope.Store(true)
	_, err = broker.Credential(t.Context(), uid, request)
	require.ErrorContains(err, "unexpected repository scope")
	excessiveScope.Store(false)
	for _, test := range []struct{ scope, message string }{
		{"multiple", "unexpected repository scope"},
		{"wrong name", "unexpected repository scope"},
		{"missing ID", "unexpected repository scope"},
		{"changed ID", "credential repository differs"},
	} {
		require.NoError(broker.Erase(t.Context(), uid, request.Repository))
		repositoryScope.Store(test.scope)
		credential, err := broker.Credential(t.Context(), uid, request)
		require.ErrorContains(err, test.message, test.scope)
		assert.Nil(credential, "a credential for a different repository must not reach the caller")
	}
	repositoryScope.Store("")
	require.NoError(broker.Erase(t.Context(), uid, "EXAMPLE-ORG/PROJECT"))
	beforeUnrelated := minted.Load()
	credential, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: "example-org/project-b", Profile: "git"})
	require.NoError(err)
	assert.Equal("fixture-project-b-token", credential.Token)
	assert.Equal(beforeUnrelated, minted.Load(), "erasing one repository preserves unrelated cached credentials")
	{
		forkRequest := CredentialRequest{Repository: "contributor-a/project", Profile: "git"}
		_, err := broker.Credential(t.Context(), uid, forkRequest)
		require.NoError(err)
		before := minted.Load()
		beforeLookup := lookupMinted.Load()
		beforeRequests := forkLookups.Load()
		require.NoError(broker.Erase(t.Context(), uid, forkRequest.Repository))
		_, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: "example-org/project-b", Profile: "git"})
		require.NoError(err)
		assert.Equal(before, minted.Load(), "erasing a fork preserves unrelated repository tokens")
		assert.Equal(beforeRequests+1, forkLookups.Load(), "erasing a fork resolves its upstream")
		_, err = broker.Credential(t.Context(), uid, forkRequest)
		require.NoError(err)
		assert.Equal(before+2, minted.Load(), "erasing a fork discards its upstream credentials")
		assert.Equal(beforeLookup, lookupMinted.Load(), "erasing a fork preserves the lookup token")
	}
	{
		before := minted.Load()
		require.NoError(broker.Erase(t.Context(), uid, "contributor-a/project-fork"))
		_, err = broker.Credential(t.Context(), uid, request)
		require.NoError(err)
		assert.Equal(before, minted.Load(), "erasing a renamed fork preserves other repositories")
		_, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: "example-org/project-b", Profile: "git"})
		require.NoError(err)
		assert.Equal(before+2, minted.Load(), "erasing a renamed fork evicts its parent's tokens")
	}
	for _, test := range []struct{ repository, message string }{
		{"contributor-a/non-fork", "not admitted"},
		{"contributor-a/unavailable", "GitHub fork unavailable"},
	} {
		before := minted.Load()
		require.ErrorContains(broker.Erase(t.Context(), uid, test.repository), test.message)
		for _, repository := range []string{"example-org/project", "example-org/project-b"} {
			_, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: repository, Profile: "git"})
			require.NoError(err)
		}
		assert.Equal(before, minted.Load(), "a failed fork erase preserves cached credentials")
	}

	for _, test := range []struct {
		name             string
		status           int
		rejectRefreshed  bool
		revokeMembership bool
		wantError        string
		wantMints        int32
	}{
		{name: "reused private name", status: http.StatusNotFound, wantMints: 2},
		{name: "changed repository identity", wantMints: 2},
		{name: "still unavailable", status: http.StatusNotFound, rejectRefreshed: true, wantError: "repository unavailable", wantMints: 1},
		{name: "upstream failure", status: http.StatusServiceUnavailable, wantError: "repository unavailable"},
		{name: "membership revoked during refresh", status: http.StatusNotFound, revokeMembership: true, wantError: "not a member", wantMints: 1},
	} {
		require.NoError(broker.Erase(t.Context(), uid, request.Repository))
		repositoryID.Store(42)
		tokenRepositoryID.Store(42)
		staleLookupStatus.Store(0)
		rejectRefreshedLookup.Store(false)
		revokeRefreshedMembership.Store(false)
		credential, err := broker.Credential(t.Context(), uid, request)
		require.NoError(err)
		assert.Equal(int64(42), credential.RepositoryID)

		repositoryID.Store(44)
		tokenRepositoryID.Store(44)
		staleLookupStatus.Store(int64(test.status))
		rejectRefreshedLookup.Store(test.rejectRefreshed)
		revokeRefreshedMembership.Store(test.revokeMembership)
		before := minted.Load()
		credential, err = broker.Credential(t.Context(), uid, request)
		assert.Equal(before+test.wantMints, minted.Load(), test.name)
		if test.wantError != "" {
			require.ErrorContains(err, test.wantError, test.name)
			assert.Nil(credential)
			continue
		}
		require.NoError(err, test.name)
		assert.Equal(int64(44), credential.RepositoryID)
		assert.Equal("fixture-replacement-token", credential.Token)
		credential, err = broker.Credential(t.Context(), uid, request)
		require.NoError(err)
		assert.Equal(int64(44), credential.RepositoryID)
		assert.Equal(before+2, minted.Load(), "the refreshed credentials are cached")
	}
	require.NoError(broker.Erase(t.Context(), uid, request.Repository))
	repositoryID.Store(42)
	tokenRepositoryID.Store(42)
	staleLookupStatus.Store(0)
	revokeRefreshedMembership.Store(false)
	{
		forkRequest := CredentialRequest{Repository: "contributor-a/project", Profile: "git"}
		_, err := broker.Credential(t.Context(), uid, forkRequest)
		require.NoError(err)
		repositoryID.Store(44)
		tokenRepositoryID.Store(44)
		before := forkLookups.Load()
		credential, err := broker.Credential(t.Context(), uid, forkRequest)
		require.NoError(err)
		assert.Equal("fixture-replacement-token", credential.Token)
		assert.Equal(int64(142), credential.RepositoryID)
		assert.Equal(before+2, forkLookups.Load(), "refresh resolves the fork once per credential attempt")
		require.NoError(broker.Erase(t.Context(), uid, request.Repository))
		repositoryID.Store(42)
		tokenRepositoryID.Store(42)
	}

	if runtime.GOOS != "linux" || os.Getuid() == 0 {
		return // The deployed socket admission uses Linux non-root peer credentials.
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", broker.config.Socket)
	require.NoError(err)
	t.Cleanup(func() { _ = listener.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveLocal(ctx, listener, &http.Server{Handler: brokerHandler(broker), ConnContext: unixPeerContext})
	}()
	t.Cleanup(func() { cancel(); assert.NoError(<-serverDone) })
	client := NewBrokerClient(broker.config.Socket)
	t.Cleanup(client.Close)
	var output, stderr strings.Builder
	err = RunCredential(t.Context(), client, "get", strings.NewReader("protocol=https\nhost=github.com\npath=example-org/project.git\n\n"), &output, &stderr)
	require.NoError(err)
	assert.Contains(output.String(), "password=fixture-installation-token")
	assert.Contains(stderr.String(), "read-only")
	output.Reset()
	err = RunCredential(t.Context(), client, "get", strings.NewReader("protocol=https\nhost=github.com\npath=contributor-a/project.git\n\n"), &output, &stderr)
	require.NoError(err)
	assert.Contains(output.String(), "password=fixture-installation-token")
	beforeErase := minted.Load()
	err = RunCredential(t.Context(), client, "erase", strings.NewReader("protocol=https\nhost=github.com\npath=contributor-a/project.git\n\n"), &output, &stderr)
	require.NoError(err)
	output.Reset()
	err = RunCredential(t.Context(), client, "get", strings.NewReader("protocol=https\nhost=github.com\npath=contributor-a/project.git\n\n"), &output, &stderr)
	require.NoError(err)
	assert.Contains(output.String(), "password=fixture-installation-token")
	assert.Equal(beforeErase+2, minted.Load(), "Git erase renews the fork's upstream credentials")
	err = RunCredential(t.Context(), client, "get", strings.NewReader("protocol=https\nhost=example.com\npath=example-org/project\n\n"), &output, &stderr)
	require.ErrorContains(err, "HTTPS github.com")

	// A TCP request cannot claim a UID through an HTTP header.
	remote := httptest.NewServer(brokerHandler(broker))
	t.Cleanup(remote.Close)
	resp, err := remote.Client().Post(remote.URL+"/v1/credentials", "application/json", strings.NewReader(`{"repository":"example-org/project","profile":"git"}`))
	require.NoError(err)
	defer resp.Body.Close()
	assert.Equal(http.StatusUnauthorized, resp.StatusCode)
	assert.Equal("application/problem+json", resp.Header.Get("Content-Type"))
	var problem struct {
		Code string `json:"code"`
	}
	require.NoError(json.UnmarshalRead(resp.Body, &problem))
	assert.Equal("unauthorized", problem.Code)
}

type serviceFixture struct {
	broker         *Broker
	uid            uint32
	humanChecks    atomic.Int32
	repositoryID   atomic.Int64
	tokenID        atomic.Int64
	mintedPerms    atomic.Value
	contentsMinted atomic.Int32
	workflows      atomic.Value
}

func TestBrokerLookupTokenDiscardsRepositoryInventory(t *testing.T) {
	assert := assert.New(t)
	var minted atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/app/installations/23/access_tokens", r.URL.Path)
		minted.Add(1)
		assert.NoError(json.MarshalWrite(w, githubapp.InstallationToken{
			Token: "lookup-token", ExpiresAt: time.Now().Add(time.Hour),
			Permissions:  map[string]string{"metadata": "read"},
			Repositories: []githubapp.Repository{{ID: 42, Name: "project-a"}},
		}))
	}))
	t.Cleanup(api.Close)
	broker := &Broker{
		config: BrokerConfig{InstallationID: 23},
		apps:   githubapp.NewClientWithBase(api.URL),
		cache:  make(map[brokerCacheKey]*githubapp.InstallationToken),
	}
	for range 2 {
		token, err := broker.token(t.Context(), "app-jwt", "", "lookup", map[string]string{"metadata": "read"})
		require.NoError(t, err)
		assert.Nil(token.Repositories)
	}
	assert.Equal(int32(1), minted.Load())
}

func TestBrokerForkLookupRenewsRejectedToken(t *testing.T) {
	for _, operation := range []string{"lookup", "erase"} {
		t.Run(operation, func(t *testing.T) {
			assert := assert.New(t)
			fixture := newServiceAccountFixture(t)
			var minted, lookups atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/app/installations/23/access_tokens":
					var request githubapp.InstallationTokenRequest
					if !assert.NoError(json.UnmarshalRead(r.Body, &request)) {
						return
					}
					assert.Empty(request.Repositories)
					assert.Equal(map[string]string{"metadata": "read"}, request.Permissions)
					assert.NoError(json.MarshalWrite(w, githubapp.InstallationToken{
						Token: fmt.Sprintf("lookup-%d", minted.Add(1)), ExpiresAt: time.Now().Add(time.Hour),
						Permissions: request.Permissions,
					}))
				case "/repos/contributor-a/project-a":
					lookups.Add(1)
					if r.Header.Get("Authorization") == "Bearer lookup-1" {
						http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
						return
					}
					assert.Equal("Bearer lookup-2", r.Header.Get("Authorization"))
					writeForkMetadataFixture(w, "project-a", "main")
				default:
					assert.Fail("unexpected lookup request", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(api.Close)
			fixture.broker.base, _ = url.Parse(api.URL + "/")
			fixture.broker.apps = githubapp.NewClientWithBase(api.URL)
			request := func() error {
				if operation == "erase" {
					return fixture.broker.Erase(t.Context(), fixture.uid, "contributor-a/project-a")
				}
				_, _, err := fixture.broker.forkUpstream(t.Context(), "app-jwt", "contributor-a", "project-a")
				return err
			}
			require.ErrorContains(t, request(), "GitHub fork unavailable")
			require.NoError(t, request())
			assert.Equal(int32(2), minted.Load())
			assert.Equal(int32(2), lookups.Load())
		})
	}
}

// serviceRepositoryIDs are the fixture installation's repositories: project-a is
// the service account's writable repository, project-b its read-only dependency,
// and project-c is installed but not listed for it.
var serviceRepositoryIDs = map[string]int64{"project-a": 42, "project-b": 43, "project-c": 44}

func newServiceAccountFixture(t *testing.T) *serviceFixture {
	t.Helper()
	fixture := &serviceFixture{uid: 2001}
	fixture.repositoryID.Store(42)
	fixture.workflows.Store("")
	idOf := func(name string) int64 {
		if name == "project-a" {
			return fixture.repositoryID.Load()
		}
		return serviceRepositoryIDs[name]
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		name, isRepo := strings.CutPrefix(r.URL.Path, "/repos/example-org/")
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/contributor-a/"):
			assert.Equal(t, "Bearer fixture-lookup-token", r.Header.Get("Authorization"))
			writeForkMetadataFixture(w, strings.TrimPrefix(r.URL.Path, "/repos/contributor-a/"), "")
		case r.URL.Path == "/app/installations/23":
			permissions := "{}"
			if grant := fixture.workflows.Load().(string); grant != "" {
				permissions = fmt.Sprintf(`{"workflows":%q}`, grant)
			}
			_, _ = fmt.Fprintf(w, `{"id":23,"app_id":17,"account":{"id":99,"login":"example-org"},"repository_selection":"selected","permissions":%s}`, permissions)
		case r.URL.Path == "/app/installations/23/access_tokens":
			var request githubapp.InstallationTokenRequest
			if !assert.NoError(t, json.UnmarshalRead(r.Body, &request)) {
				return
			}
			if len(request.Repositories) == 0 {
				assert.Empty(t, request.RepositoryIDs)
				assert.Equal(t, map[string]string{"metadata": "read"}, request.Permissions)
				writeLookupTokenFixture(t, w, request.Permissions)
				return
			}
			if !assert.Len(t, request.Repositories, 1) {
				return
			}
			repository := request.Repositories[0]
			id := serviceRepositoryIDs[repository]
			if repository == "project-a" && fixture.tokenID.Load() != 0 {
				id = fixture.tokenID.Load()
			}
			if request.Permissions["contents"] != "" {
				fixture.contentsMinted.Add(1)
				fixture.mintedPerms.Store(request.Permissions)
			}
			data, err := json.Marshal(githubapp.InstallationToken{
				Token: "fixture-installation-token", ExpiresAt: time.Now().Add(time.Hour),
				Repositories: []githubapp.Repository{{ID: id, Name: repository}},
				Permissions:  request.Permissions,
			})
			if assert.NoError(t, err) {
				_, _ = w.Write(data)
			}
		case r.URL.Path == "/user/5555",
			strings.HasPrefix(r.URL.Path, "/orgs/example-org/members/"),
			strings.Contains(r.URL.Path, "/collaborators/"):
			fixture.humanChecks.Add(1)
			http.NotFound(w, r)
		case isRepo && serviceRepositoryIDs[name] != 0:
			_, _ = fmt.Fprintf(w, `{"id":%d,"name":%q,"owner":{"id":99},"default_branch":"main"}`, idOf(name), name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	keyPath := writeAppKey(t)
	var err error
	fixture.broker, err = NewBroker(BrokerConfig{
		Socket: filepath.Join(t.TempDir(), "broker.sock"), AppID: 17, InstallationID: 23,
		Organization: "example-org", OrganizationID: 99, PrivateKeyFile: keyPath,
		Accounts: []Account{{
			UID: fixture.uid, GitHubUserID: 5555, Login: "example-app[bot]", Service: true,
			WriteRepositoryIDs: []int64{42}, ReadRepositoryIDs: []int64{43},
		}},
	})
	require.NoError(t, err)
	fixture.broker.base, err = url.Parse(api.URL + "/")
	require.NoError(t, err)
	fixture.broker.apps = githubapp.NewClientWithBase(api.URL)
	return fixture
}

func writeForkMetadataFixture(w http.ResponseWriter, parent, defaultBranch string) {
	_, _ = fmt.Fprintf(w, `{"id":142,"parent":{"name":%q,"owner":{"id":99}}`, parent)
	if defaultBranch != "" {
		_, _ = fmt.Fprintf(w, `,"default_branch":%q`, defaultBranch)
	}
	_, _ = fmt.Fprint(w, `}`)
}

func writeLookupTokenFixture(t *testing.T, w http.ResponseWriter, permissions map[string]string) {
	t.Helper()
	data, err := json.Marshal(githubapp.InstallationToken{
		Token: "fixture-lookup-token", ExpiresAt: time.Now().Add(time.Hour),
		Permissions: permissions,
	})
	if assert.NoError(t, err) {
		_, _ = w.Write(data)
	}
}

func writeAppKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}), 0o600))
	return keyPath
}

func TestBrokerServiceAccountMintsWritableTokenWithoutMembership(t *testing.T) {
	fixture := newServiceAccountFixture(t)
	for _, profile := range []string{"git", "push", "pr"} {
		credential, err := fixture.broker.Credential(t.Context(), fixture.uid,
			CredentialRequest{Repository: "example-org/project-a", Profile: profile})
		require.NoError(t, err, profile)
		assert.NotEmpty(t, credential.Token, profile)
		assert.True(t, credential.Writable, profile)
		assert.Equal(t, int64(5555), credential.GitHubUserID, profile)
		assert.Equal(t, int64(42), credential.RepositoryID, profile)
		assert.Equal(t, "main", credential.DefaultBranch, profile)
	}
	assert.Zero(t, fixture.humanChecks.Load(), "service accounts skip user, membership, and permission checks")
}

func TestBrokerWorkflowPermissions(t *testing.T) {
	for _, profile := range []string{"git", "pr"} {
		t.Run(profile, func(t *testing.T) {
			fixture := newServiceAccountFixture(t)
			request := CredentialRequest{Repository: "example-org/project-a", Profile: profile}
			for _, grant := range []string{"", "read", "write", ""} {
				fixture.workflows.Store(grant)
				_, err := fixture.broker.Credential(t.Context(), fixture.uid, request)
				require.NoError(t, err)
				want := map[string]string{"metadata": "read", "contents": "write"}
				if profile == "pr" {
					want["pull_requests"] = "write"
					want["checks"], want["statuses"] = "read", "read"
				}
				if grant == "write" {
					want["workflows"] = "write"
				}
				permissions, ok := fixture.mintedPerms.Load().(map[string]string)
				require.True(t, ok)
				assert.Equal(t, want, permissions, "installation workflows grant %q", grant)
			}
			assert.Equal(t, int32(3), fixture.contentsMinted.Load(), "permission changes replace cached tokens")
		})
	}
}

func TestBrokerServiceAccountStillChecksRepositoryIdentity(t *testing.T) {
	fixture := newServiceAccountFixture(t)
	fixture.repositoryID.Store(43)
	_, err := fixture.broker.Credential(t.Context(), fixture.uid,
		CredentialRequest{Repository: "example-org/project-a", Profile: "pr"})
	require.ErrorContains(t, err, "repository identity")
	assert.Zero(t, fixture.humanChecks.Load())
}

func TestBrokerServiceAccountRejectsAnotherOrganizationsRepository(t *testing.T) {
	fixture := newServiceAccountFixture(t)
	_, err := fixture.broker.Credential(t.Context(), fixture.uid,
		CredentialRequest{Repository: "personal/project-a", Profile: "pr"})
	require.ErrorContains(t, err, "not admitted")
}

func TestBrokerServiceAccountForkUsesUpstreamGrant(t *testing.T) {
	fixture := newServiceAccountFixture(t)
	credential, err := fixture.broker.Credential(t.Context(), fixture.uid,
		CredentialRequest{Repository: "contributor-a/project-a", Profile: "push"})
	require.NoError(t, err)
	assert.Equal(t, int64(142), credential.RepositoryID)
	assert.True(t, credential.Writable)
	_, err = fixture.broker.Credential(t.Context(), fixture.uid,
		CredentialRequest{Repository: "contributor-a/project-c", Profile: "push"})
	require.ErrorContains(t, err, "repository is not admitted for this service account")
}

func TestBrokerServiceAccountReadsOnlyItsReadRepositories(t *testing.T) {
	fixture := newServiceAccountFixture(t)
	fixture.workflows.Store("write")
	credential, err := fixture.broker.Credential(t.Context(), fixture.uid,
		CredentialRequest{Repository: "example-org/project-b", Profile: "git"})
	require.NoError(t, err)
	assert.False(t, credential.Writable)
	assert.Equal(t, int64(43), credential.RepositoryID)
	permissions, ok := fixture.mintedPerms.Load().(map[string]string)
	require.True(t, ok)
	assert.Equal(t, map[string]string{"metadata": "read", "contents": "read"}, permissions)
	for _, profile := range []string{"push", "pr"} {
		before := fixture.contentsMinted.Load()
		credential, err := fixture.broker.Credential(t.Context(), fixture.uid,
			CredentialRequest{Repository: "example-org/project-b", Profile: profile})
		require.ErrorContains(t, err, "read-only access", profile)
		assert.Nil(t, credential, profile)
		assert.Equal(t, before, fixture.contentsMinted.Load(), "a refused %s request mints no repository token", profile)
	}
}

func TestBrokerServiceAccountRejectsUnlistedRepository(t *testing.T) {
	fixture := newServiceAccountFixture(t)
	for _, profile := range []string{"git", "push", "pr"} {
		credential, err := fixture.broker.Credential(t.Context(), fixture.uid,
			CredentialRequest{Repository: "example-org/project-c", Profile: profile})
		require.ErrorContains(t, err, "not admitted for this service account", profile)
		assert.Nil(t, credential, profile)
	}
	assert.Zero(t, fixture.contentsMinted.Load(), "an installed but unlisted repository receives no token")
}

func TestBrokerServiceAccountFollowsRepositoryIDNotName(t *testing.T) {
	fixture := newServiceAccountFixture(t)
	// The name project-a now belongs to a repository the account may not use.
	fixture.repositoryID.Store(44)
	fixture.tokenID.Store(44)
	_, err := fixture.broker.Credential(t.Context(), fixture.uid,
		CredentialRequest{Repository: "example-org/project-a", Profile: "push"})
	require.ErrorContains(t, err, "not admitted for this service account")
	assert.Zero(t, fixture.contentsMinted.Load())
}

func TestNewBrokerValidatesServiceRepositoryLists(t *testing.T) {
	keyPath := writeAppKey(t)
	for _, test := range []struct {
		name    string
		account Account
		want    string
	}{
		{"service without writable repositories", Account{UID: 2001, GitHubUserID: 5555, Service: true, ReadRepositoryIDs: []int64{43}}, "require write_repository_ids"},
		{"repeated repository", Account{UID: 2001, GitHubUserID: 5555, Service: true, WriteRepositoryIDs: []int64{42}, ReadRepositoryIDs: []int64{42}}, "listed once"},
		{"non-positive repository", Account{UID: 2001, GitHubUserID: 5555, Service: true, WriteRepositoryIDs: []int64{0}}, "positive"},
		{"developer with a repository list", Account{UID: 1001, GitHubUserID: 1234, WriteRepositoryIDs: []int64{42}}, "only to service accounts"},
	} {
		_, err := NewBroker(BrokerConfig{
			Socket: filepath.Join(t.TempDir(), "broker.sock"), AppID: 17, InstallationID: 23,
			Organization: "example-org", OrganizationID: 99, PrivateKeyFile: keyPath,
			Accounts: []Account{test.account},
		})
		require.ErrorContains(t, err, test.want, test.name)
	}
}

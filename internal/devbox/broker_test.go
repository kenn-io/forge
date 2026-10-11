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
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/installations/23":
			_, _ = fmt.Fprint(w, `{"id":23,"app_id":17,"account":{"id":99,"login":"example-org"},"repository_selection":"selected"}`)
		case "/app/installations/23/access_tokens":
			var request struct {
				Repositories  []string          `json:"repositories"`
				RepositoryIDs []int64           `json:"repository_ids"`
				Permissions   map[string]string `json:"permissions"`
			}
			if !assert.NoError(json.UnmarshalRead(r.Body, &request)) {
				return
			}
			if !assert.Len(request.Repositories, 1) {
				return
			}
			assert.Empty(request.RepositoryIDs)
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
	_, err = broker.Credential(t.Context(), uid+1, request)
	require.ErrorContains(err, "not enrolled")
	_, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: "personal/project", Profile: "git"})
	require.ErrorContains(err, "not admitted")

	require.NoError(broker.Erase(uid, request.Repository))
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
		require.NoError(broker.Erase(uid, request.Repository))
		repositoryScope.Store(test.scope)
		credential, err := broker.Credential(t.Context(), uid, request)
		require.ErrorContains(err, test.message, test.scope)
		assert.Nil(credential, "a credential for a different repository must not reach the caller")
	}
	repositoryScope.Store("")
	require.NoError(broker.Erase(uid, "EXAMPLE-ORG/PROJECT"))
	beforeUnrelated := minted.Load()
	credential, err = broker.Credential(t.Context(), uid, CredentialRequest{Repository: "example-org/project-b", Profile: "git"})
	require.NoError(err)
	assert.Equal("fixture-project-b-token", credential.Token)
	assert.Equal(beforeUnrelated, minted.Load(), "erasing one repository preserves unrelated cached credentials")

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
		require.NoError(broker.Erase(uid, request.Repository))
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
	require.NoError(broker.Erase(uid, request.Repository))
	repositoryID.Store(42)
	tokenRepositoryID.Store(42)
	staleLookupStatus.Store(0)
	revokeRefreshedMembership.Store(false)

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

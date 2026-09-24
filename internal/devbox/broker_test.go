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
	var minted atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/installations/23":
			_, _ = fmt.Fprint(w, `{"id":23,"app_id":17,"account":{"id":99,"login":"example-org"},"repository_selection":"selected"}`)
		case "/app/installations/23/access_tokens":
			var request githubapp.InstallationTokenRequest
			if !assert.NoError(json.UnmarshalRead(r.Body, &request)) {
				return
			}
			assert.Equal([]int64{42}, request.RepositoryIDs)
			minted.Add(1)
			permissions := request.Permissions
			if excessiveScope.Load() {
				permissions["administration"] = "write"
			}
			data, err := json.Marshal(githubapp.InstallationToken{
				Token: "fixture-installation-token", ExpiresAt: time.Now().Add(time.Hour),
				Repositories: []githubapp.Repository{{ID: 42}}, Permissions: permissions,
			})
			if assert.NoError(err) {
				_, _ = w.Write(data)
			}
		case "/user/1234":
			_, _ = fmt.Fprint(w, `{"id":1234,"login":"developer-a"}`)
		case "/orgs/example-org/members/developer-a":
			if member.Load() {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case "/repos/example-org/project":
			_, _ = fmt.Fprint(w, `{"id":42,"node_id":"R_kgDOExample","name":"project","owner":{"id":99},"default_branch":"main"}`)
		case "/repos/example-org/project/collaborators/developer-a/permission":
			_, _ = fmt.Fprintf(w, `{"permission":%q,"user":{"id":1234}}`, permission.Load())
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
		Accounts: []Account{{UID: uid, GitHubUserID: 1234}}, Repositories: []Repository{{ID: 42, Name: "project"}},
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
	assert.Equal("R_kgDOExample", credential.RepositoryNodeID)
	assert.Equal(int32(2), minted.Load())

	_, err = broker.Credential(t.Context(), uid, request)
	require.NoError(err)
	assert.Equal(int32(2), minted.Load(), "tokens are reused after fresh authorization")
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

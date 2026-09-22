package devbox

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/tokenauth"
)

func TestAttributionUsesControllerCredentialAndChecksGitHubHead(t *testing.T) {
	assert := assert.New(t)
	state := PushState{Repository: "example-org/project", Branch: "work/topic", OID: strings.Repeat("a", 40), Pushed: true}
	githubOID, authorID, committerID := state.OID, 42, 42
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("GET", r.Method, "verification must never repeat the push")
		assert.Equal("/repos/example-org/project/commits/work/topic", r.URL.Path)
		assert.Equal("Bearer controller-read-token", r.Header.Get("Authorization"))
		_, _ = fmt.Fprintf(w, `{"sha":%q,"author":{"id":%d},"committer":{"id":%d},"commit":{"author":{"name":"Developer A","email":"42+developer-a@users.noreply.github.com"},"committer":{"name":"Developer A","email":"42+developer-a@users.noreply.github.com"}}}`, githubOID, authorID, committerID)
	}))
	t.Cleanup(api.Close)
	connections, err := OpenConnections(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(connections.Close)
	connections.items = []savedConnection{{ID: "target", Profile: Profile{Assignment: Assignment{WorkerIdentity: WorkerIdentity{GitHubUserID: 42}}, Token: "worker-token"}}}
	transport := api.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, api.Listener.Addr().String())
	}
	connections.client.Transport = transport
	t.Setenv("DEVBOX_TEST_READ_TOKEN", "controller-read-token")
	source := tokenauth.NewManagedSource(tokenauth.Descriptor{Candidates: []tokenauth.Candidate{{Kind: tokenauth.SourceKindEnv, EnvName: "DEVBOX_TEST_READ_TOKEN"}}}, tokenauth.Options{})
	assert.Equal("matched", connections.CheckAttribution(t.Context(), "target", state, source).Status)
	for _, test := range []struct {
		author, committer int
		status            string
	}{
		{7, 42, "preserved_author"}, {0, 42, "mismatch"}, {42, 7, "mismatch"},
	} {
		connections.attribution = nil
		authorID, committerID = test.author, test.committer
		assert.Equal(test.status, connections.CheckAttribution(t.Context(), "target", state, source).Status)
	}
	connections.attribution = nil
	githubOID = strings.Repeat("b", 40)
	assert.Equal("unverified", connections.CheckAttribution(t.Context(), "target", state, source).Status)
	api.Close()
	assert.Equal("unverified", connections.CheckAttribution(t.Context(), "target", state, source).Status)
}

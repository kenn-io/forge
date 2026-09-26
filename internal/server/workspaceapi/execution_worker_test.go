package workspaceapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"
)

// serveWorkerCredential answers broker credential requests on a Unix socket
// with a credential for the given integer GitHub repository ID.
func serveWorkerCredential(t *testing.T, repositoryID int64) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "forge-broker-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "broker.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	require.NoError(t, err)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			assert.NoError(t, json.NewEncoder(w).Encode(devbox.Credential{
				Token: "fixture-token", ExpiresAt: time.Now().Add(time.Hour),
				Writable: true, GitHubUserID: 1234, RepositoryID: repositoryID,
				DefaultBranch: "main",
			}))
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}

func TestAdmitWorkerRepositoryUsesIntegerRepositoryID(t *testing.T) {
	tests := []struct {
		name           string
		suppliedRepoID int64
		wantAdmitted   bool
	}{
		{name: "matching supplied id", suppliedRepoID: 4242, wantAdmitted: true},
		{name: "no supplied id", wantAdmitted: true},
		{name: "different supplied id", suppliedRepoID: 5151},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			handler := New(Deps{
				DB: database,
				ExecutionWorker: config.ExecutionWorker{
					Enabled: true, GitHubUserID: 1234,
					BrokerSocket: serveWorkerCredential(t, 4242),
				},
				EnrichmentDisabled: true,
			})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
				defer cancel()
				require.NoError(handler.Shutdown(ctx))
			})

			credential, err := handler.admitWorkerRepository(t.Context(), db.WorkspaceLaunchRepository{
				Provider: "github", PlatformHost: "github.com",
				PlatformRepoID: tt.suppliedRepoID, Owner: "acme", Name: "widget",
			})

			entry, lookupErr := database.GetRepositoryByProviderID(t.Context(), platform.RepositoryIdentity{
				Provider: "github", PlatformHost: "github.com", PlatformRepoID: 4242,
			})
			require.NoError(lookupErr)
			if !tt.wantAdmitted {
				var problem *httpapi.ProblemError
				require.ErrorAs(err, &problem)
				assert.Equal(http.StatusBadRequest, problem.Status)
				assert.Nil(entry, "a rejected repository must not be recorded")
				return
			}
			require.NoError(err)
			assert.Equal(int64(4242), credential.RepositoryID)
			require.NotNil(entry)
			assert.Equal("acme", entry.Repository.Owner)
			assert.Equal("widget", entry.Repository.Name)
			assert.Equal("main", entry.Repository.DefaultBranch)
		})
	}
}

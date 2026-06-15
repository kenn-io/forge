package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
)

// hubServer builds a Server whose fleet self key is "hub" (via
// config), with no peers configured — exercising the self and
// unknown-host branches of the project write dispatch.
func hubServer() *Server {
	cfg := &config.Config{}
	cfg.Fleet.Key = "hub"
	return &Server{cfg: cfg}
}

func TestServeFleetProjectWrite_SelfRoutesToLocalHandler(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	s := hubServer()
	var localPath string
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"prj_local"}`))
	})

	r := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/hosts/hub/projects",
		strings.NewReader(`{"local_path":"/local/repo"}`))
	r.SetPathValue("host_key", "hub")
	w := httptest.NewRecorder()

	s.serveFleetProjectWrite(w, r, "/api/v1/projects")

	assert.Equal("/api/v1/projects", localPath, "self routes to the local project handler")
	require.Equal(http.StatusCreated, w.Code)
	assert.JSONEq(`{"id":"prj_local"}`, w.Body.String())
}

func TestServeFleetProjectWrite_UnknownHostIs404(t *testing.T) {
	s := hubServer()

	r := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/hosts/spoke/projects",
		strings.NewReader(`{"local_path":"/x"}`))
	r.SetPathValue("host_key", "spoke")
	w := httptest.NewRecorder()

	s.serveFleetProjectWrite(w, r, "/api/v1/projects")

	assert.Equal(t, http.StatusNotFound, w.Code,
		"a host that is neither local nor a configured peer is unreachable")
}

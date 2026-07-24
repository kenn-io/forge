package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKataProxyForwardsNonJSONMutationWithSameOriginFetchSite(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	var receivedContentType, receivedBody string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		receivedContentType = r.Header.Get("Content-Type")
		receivedBody = string(body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	}))
	t.Cleanup(daemon.Close)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeRootKataCatalog(t, home, daemon.URL)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/kata/proxy/api/v1/files", strings.NewReader("raw markdown"))
	req.Header.Set("Content-Type", "text/markdown")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())
	assert.Equal("text/markdown", receivedContentType)
	assert.Equal("raw markdown", receivedBody)
	assert.Equal("created", strings.TrimSpace(rr.Body.String()))
}

func TestKataProxyRejectsNonJSONMutationWithoutFetchSiteProof(t *testing.T) {
	require := require.New(t)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(daemon.Close)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeRootKataCatalog(t, home, daemon.URL)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/kata/proxy/api/v1/files", strings.NewReader("raw markdown"))
	req.Header.Set("Content-Type", "text/markdown")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusUnsupportedMediaType, rr.Code, rr.Body.String())
}

func TestKataProxyRejectsCrossSiteMutationBeforeForwarding(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var reached bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(daemon.Close)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeRootKataCatalog(t, home, daemon.URL)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/kata/proxy/api/v1/files", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusForbidden, rr.Code, rr.Body.String())
	assert.False(reached)
}

func writeRootKataCatalog(t *testing.T, home, daemonURL string) {
	t.Helper()
	body := "[[daemon]]\nname = \"home\"\nurl = \"" + daemonURL + "\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600))
}

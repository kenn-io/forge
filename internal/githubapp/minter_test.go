package githubapp

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/platform"
)

func TestMintInstallationTokenPreservesError(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(http.MethodPost, r.Method)
		assert.Equal("/app/installations/11/access_tokens", r.URL.Path)
		http.Error(w, "installation missing", http.StatusNotFound)
	}))
	t.Cleanup(api.Close)
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	key := generateTestKey(t)
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600))

	_, _, err := mintInstallationToken(t.Context(), api.URL, 7, keyPath, 11)
	require.Error(t, err)
	var status *githubapp.StatusError
	assert.ErrorAs(err, &status)
	assert.NotErrorIs(err, platform.ErrNotFound)
	assert.EqualError(err, "creating installation token for installation 11: github api status 404: installation missing")
}

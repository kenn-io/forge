package githubapp

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/githubapp/githubapptest"
)

// submitManifest plays the browser role: POST a manifest form to the
// fake's web surface and return the conversion code from the redirect.
func submitManifest(t *testing.T, fake *githubapptest.Fake, manifest Manifest) string {
	t.Helper()
	manifestJSON, err := manifest.JSON()
	require.NoError(t, err)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.PostForm(
		fake.URL()+"/settings/apps/new?state=test-state",
		url.Values{"manifest": {manifestJSON}},
	)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "test-state", loc.Query().Get("state"))
	code := loc.Query().Get("code")
	require.NotEmpty(t, code)
	return code
}

func TestConvertManifest(t *testing.T) {
	t.Parallel()
	req := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	manifest, err := NewManifest("middleman-conv", "", "http://127.0.0.1:1/callback")
	req.NoError(err)
	code := submitManifest(t, fake, manifest)

	client := NewClientWithBase(fake.APIBase())
	creds, err := client.ConvertManifest(context.Background(), code)
	req.NoError(err)

	check := assert.New(t)
	check.Equal("middleman-conv", creds.Slug)
	check.Positive(creds.ID)
	check.Contains(creds.PEM, "RSA PRIVATE KEY")
	check.NotEmpty(creds.ClientSecret)
	_, parseErr := ParsePrivateKey([]byte(creds.PEM))
	req.NoError(parseErr)

	// Conversion codes are single use; replay must fail loudly so the
	// CLI reports a stale callback instead of silently re-creating.
	_, err = client.ConvertManifest(context.Background(), code)
	check.True(IsStatus(err, http.StatusNotFound), "got %v", err)
}

func TestMintInstallationToken(t *testing.T) {
	t.Parallel()
	req := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	manifest, err := NewManifest("middleman-mint", "", "http://127.0.0.1:1/callback")
	req.NoError(err)
	code := submitManifest(t, fake, manifest)
	client := NewClientWithBase(fake.APIBase())
	creds, err := client.ConvertManifest(context.Background(), code)
	req.NoError(err)
	installID, err := fake.Install(creds.ID, "kenn-io")
	req.NoError(err)

	keyPath := filepath.Join(t.TempDir(), "app.pem")
	req.NoError(os.WriteFile(keyPath, []byte(creds.PEM), 0o600))

	token, expires, err := mintInstallationToken(
		context.Background(), fake.APIBase(), creds.ID, keyPath, installID,
	)
	req.NoError(err)
	check := assert.New(t)
	check.True(strings.HasPrefix(token, "ghs_"), "token %q", token)
	check.Greater(time.Until(expires), 50*time.Minute)

	// The minted token must be usable as a plain bearer credential.
	rate, err := client.CoreRateLimit(context.Background(), token)
	req.NoError(err)
	check.Equal(5000, rate.Limit)
}

func TestMintInstallationTokenRejectsWrongKey(t *testing.T) {
	t.Parallel()
	req := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	manifest, err := NewManifest("middleman-badkey", "", "http://127.0.0.1:1/callback")
	req.NoError(err)
	client := NewClientWithBase(fake.APIBase())
	creds, err := client.ConvertManifest(
		context.Background(), submitManifest(t, fake, manifest),
	)
	req.NoError(err)
	installID, err := fake.Install(creds.ID, "kenn-io")
	req.NoError(err)

	// A key that does not match the app must be rejected by signature
	// verification, not just shape checks.
	otherKey := generateTestKey(t)
	wrongJWT, err := SignAppJWT(creds.ID, otherKey, time.Now())
	req.NoError(err)
	_, err = client.CreateInstallationToken(context.Background(), wrongJWT, installID)
	assert.True(t, IsStatus(err, http.StatusUnauthorized), "got %v", err)
}

func TestAPIBaseForHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		host string
		want string
	}{
		{host: "", want: "https://api.github.com"},
		{host: "github.com", want: "https://api.github.com"},
		{host: "github.example.com", want: "https://github.example.com/api/v3"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, APIBaseForHost(tt.host), "host %q", tt.host)
	}
}

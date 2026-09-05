package platform_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/forgejo"
	"go.kenn.io/forge/platform/gitea"
	"go.kenn.io/forge/platform/gitlab"
)

func TestPublicConstructorsRequireCallerTransportAndClock(t *testing.T) {
	factories := map[string]func(string, http.RoundTripper, func() time.Time) (platform.Provider, error){
		"gitlab": func(host string, transport http.RoundTripper, clock func() time.Time) (platform.Provider, error) {
			return gitlab.NewClient(host, nil, gitlab.WithTransport(transport), gitlab.WithClock(clock))
		},
		"gitea": func(host string, transport http.RoundTripper, clock func() time.Time) (platform.Provider, error) {
			return gitea.NewClient(host, nil, gitea.WithTransport(transport), gitea.WithClock(clock))
		},
		"forgejo": func(host string, transport http.RoundTripper, clock func() time.Time) (platform.Provider, error) {
			return forgejo.NewClient(host, nil, forgejo.WithTransport(transport), forgejo.WithClock(clock))
		},
	}
	for name, create := range factories {
		t.Run(name, func(t *testing.T) {
			require := require.New(t)
			_, err := create("code.example.org", nil, time.Now)
			require.ErrorIs(err, platform.ErrInvalidArgument)
			_, err = create("code.example.org", http.DefaultTransport, nil)
			require.ErrorIs(err, platform.ErrInvalidArgument)
			client, err := create(" HTTPS://CODE.EXAMPLE.ORG:8443/ ", http.DefaultTransport, time.Now)
			require.NoError(err)
			assert.Equal(t, "code.example.org:8443", client.Host())
		})
	}
}

package platform_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

type rotatingCredential struct{ token string }

func (source *rotatingCredential) Token(context.Context) (string, error) { return source.token, nil }
func (source *rotatingCredential) Invalidate(rejected string) {
	if source.token == rejected {
		source.token = "second"
	}
}

func TestExplicitCredentialRetriesRejectedToken(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var sent []string
	transport := platform.AuthTransport{
		Source:              &rotatingCredential{token: "first"},
		SetHeader:           platform.BearerAuthHeader,
		AllowedOrigin:       "https://code.example.org",
		RetryOnUnauthorized: true,
		Base: platform.RoundTripFunc(func(request *http.Request) (*http.Response, error) {
			sent = append(sent, request.Header.Get("Authorization"))
			status := http.StatusOK
			if request.Header.Get("Authorization") == "Bearer first" {
				status = http.StatusUnauthorized
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		}),
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://code.example.org/user", nil)
	require.NoError(err)
	response, err := transport.RoundTrip(request)
	require.NoError(err)
	defer response.Body.Close()
	assert.Equal(http.StatusOK, response.StatusCode)
	assert.Equal([]string{"Bearer first", "Bearer second"}, sent)
}

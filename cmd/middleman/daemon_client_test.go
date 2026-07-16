package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBearerTransportDoesNotForwardTokenAcrossOrigins(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var redirectedAuthorization string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)
	var originAuthorization string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originAuthorization = r.Header.Get("Authorization")
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	client := &http.Client{Transport: bearerTransport{
		token: "daemon-secret", origin: origin.URL, base: http.DefaultTransport,
	}}
	resp, err := client.Get(origin.URL)
	require.NoError(err)
	require.NoError(resp.Body.Close())
	assert.Equal("Bearer daemon-secret", originAuthorization)
	assert.Empty(redirectedAuthorization)
}

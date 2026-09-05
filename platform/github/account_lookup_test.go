package github_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/github"
)

func TestAccountLookupPreservesIdentityAndBoundsReads(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		bytes      int64
		want       platform.AccountType
		failure    error
	}{
		{"user", `{"id":9007199254740993,"login":"user-a","type":"User"}`, 1024, platform.AccountUser, nil},
		{"bot", `{"id":9007199254740993,"login":"user-a","type":"Bot"}`, 1024, platform.AccountBot, nil},
		{"organization", `{"id":9007199254740993,"login":"user-a","type":"Organization"}`, 1024, platform.AccountOrganization, nil},
		{"unknown", `{"id":9007199254740993,"login":"user-a"}`, 1024, platform.AccountUnknown, nil},
		{"oversized", `{"id":9007199254740993,"login":"user-a"}`, 8, "", platform.ErrPageLimit},
		{"wrong identity", `{"id":12,"login":"user-a","type":"User"}`, 1024, "", platform.ErrProviderContract},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Equal("https://api.github.com/user/9007199254740993", req.URL.String())
				return &http.Response{StatusCode: 200, Header: make(http.Header), Request: req, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			client, err := github.NewClient(github.ClientConfig{Host: "github.com", Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
			require.NoError(err)
			provider, err := github.NewProvider(github.ProviderConfig{Host: "github.com", Client: client, Clock: time.Now})
			require.NoError(err)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			account, err := provider.GetAccount(ctx, "9007199254740993", platform.Budget{MaxRecords: 1, MaxNodes: 1, MaxBytes: tc.bytes, MaxOutputBytes: 1024})
			if tc.failure != nil {
				require.ErrorIs(err, tc.failure)
				return
			}
			require.NoError(err)
			assert.Equal("9007199254740993", account.ID)
			assert.Equal(tc.want, account.Type)
		})
	}
}

func TestAccountLookupReturnsTypedProviderFailures(t *testing.T) {
	for _, tc := range []struct {
		status  int
		failure error
	}{
		{404, platform.ErrNotFound}, {403, platform.ErrPermissionDenied}, {429, platform.ErrRateLimited},
	} {
		hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Header: make(http.Header), Request: req, Body: io.NopCloser(strings.NewReader(`{"message":"fixture"}`))}, nil
		})}
		client, err := github.NewClient(github.ClientConfig{Host: "github.com", Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		_, err = client.LookupAccount(ctx, "user-a", platform.Budget{MaxRecords: 1, MaxNodes: 1, MaxBytes: 1024, MaxOutputBytes: 1024})
		cancel()
		assert.ErrorIs(t, err, tc.failure)
	}
}

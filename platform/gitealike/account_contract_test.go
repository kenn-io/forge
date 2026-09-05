package gitealike_test

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
	"go.kenn.io/forge/platform/forgejo"
	"go.kenn.io/forge/platform/gitea"
)

type accountToken struct{}

func (accountToken) Token(context.Context) (string, error) { return "test-token", nil }
func (accountToken) Invalidate(string)                     {}

func TestAccountLookupUsesExactRouteAndUnknownType(t *testing.T) {
	for _, kind := range []platform.Kind{platform.KindForgejo, platform.KindGitea} {
		t.Run(string(kind), func(t *testing.T) {
			require := require.New(t)
			for _, lookupID := range []bool{false, true} {
				calls := 0
				transport := platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					assert.Equal(t, "token test-token", req.Header.Get("Authorization"))
					body := `{"id":21,"login":"user-a","full_name":"User A","is_admin":true}`
					if lookupID {
						assert.Equal(t, "/api/v1/users/search", req.URL.Path)
						assert.Equal(t, "21", req.URL.Query().Get("uid"))
						body = `{"ok":true,"data":[` + body + `]}`
					} else {
						assert.Equal(t, "/api/v1/users/user-a", req.URL.Path)
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Request: req, Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				var reader platform.AccountReader
				var err error
				if kind == platform.KindForgejo {
					reader, err = forgejo.NewClient("code.example.org", accountToken{}, forgejo.WithTransport(transport), forgejo.WithClock(time.Now))
				} else {
					reader, err = gitea.NewClient("code.example.org", accountToken{}, gitea.WithTransport(transport), gitea.WithClock(time.Now))
				}
				require.NoError(err)
				require.Zero(calls)
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				budget := platform.Budget{MaxRecords: 1, MaxNodes: 1, MaxBytes: 1024, MaxOutputBytes: 1024}
				var account platform.Account
				if lookupID {
					account, err = reader.GetAccount(ctx, "21", budget)
				} else {
					account, err = reader.LookupAccount(ctx, "user-a", budget)
				}
				cancel()
				require.NoError(err)
				assert.Equal(t, platform.Account{ID: "21", Login: "user-a", DisplayName: "User A", Type: platform.AccountUnknown}, account)
			}
		})
	}
}

package github

import (
	"encoding/json/v2"
	"testing"

	gh "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestAccountUsesRESTIdentityAndExplicitType(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       platform.AccountType
	}{
		{"user", `{"id":9007199254740993,"node_id":"not-the-key","login":"user-a","type":"User"}`, platform.AccountUser},
		{"bot", `{"id":9007199254740993,"login":"automation","type":"Bot"}`, platform.AccountBot},
		{"organization", `{"id":9007199254740993,"login":"team-a","type":"Organization"}`, platform.AccountOrganization},
		{"omitted", `{"id":9007199254740993,"login":"robot[bot]"}`, platform.AccountUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			var user gh.User
			require.NoError(json.Unmarshal([]byte(tc.body), &user))
			account := NormalizeAccount(&user)
			require.NotNil(account)
			assert.Equal("9007199254740993", account.ID)
			assert.Equal(tc.want, account.Type)
		})
	}
	assert.Nil(t, NormalizeAccount(nil))
}

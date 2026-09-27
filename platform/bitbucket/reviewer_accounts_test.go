package bitbucket_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewerAccountsKeepNamesSeparateFromActionIDs(t *testing.T) {
	c := client(t, "secret", func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case "/2.0/repositories/team/{11111111-1111-4111-8111-111111111111}/pullrequests/7":
			return 200, `{"reviewers":[{"uuid":"{22222222-2222-4222-8222-222222222222}","display_name":"Alex Example","nickname":"alex"}]}`
		case "/2.0/workspaces/team/members":
			return 200, `{"values":[{"user":{"uuid":"{22222222-2222-4222-8222-222222222222}","display_name":"Alex Example","nickname":"alex"}}],"next":"https://api.bitbucket.org/2.0/member-page-two"}`
		case "/2.0/member-page-two":
			return 200, `{"values":[{"user":{"uuid":"{33333333-3333-4333-8333-333333333333}","display_name":"Alex Other","nickname":"alex","links":{"avatar":{"href":"https://example.com/avatar.png"}}}}]}`
		default:
			require.FailNow(t, "unexpected request", "%s", r.URL)
			return 500, ""
		}
	})
	got, err := c.ListReviewerAccounts(t.Context(), ref, 7)
	require.NoError(t, err)
	require.Len(t, got.Accounts, 2)
	assert.Equal(t, "Alex Example", got.Accounts[0].DisplayName)
	assert.Equal(t, "{33333333-3333-4333-8333-333333333333}", got.Accounts[1].ID)
	assert.Equal(t, "Alex Other", got.Accounts[1].DisplayName)
	assert.Equal(t, "alex", got.Accounts[1].Nickname)
	assert.Equal(t, "https://example.com/avatar.png", got.Accounts[1].AvatarURL)
}

func TestReviewerAccountsRetainCurrentNamesWhenDirectoryDenied(t *testing.T) {
	c := client(t, "secret", func(r *http.Request) (int, string) {
		if r.URL.Path == "/2.0/workspaces/team/members" {
			return 403, `{"error":{"message":"missing scope"}}`
		}
		return 200, `{"reviewers":[{"uuid":"{22222222-2222-4222-8222-222222222222}","display_name":"Alex Example"}]}`
	})
	got, err := c.ListReviewerAccounts(t.Context(), ref, 7)
	require.NoError(t, err)
	require.Len(t, got.Accounts, 1)
	assert.Equal(t, "Alex Example", got.Accounts[0].DisplayName)
	assert.Contains(t, got.CandidateError, "read:workspace:bitbucket")
}

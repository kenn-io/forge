package bitbucket_test

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucket"
)

type credential string

func (c credential) Token(context.Context) (string, error) { return string(c), nil }
func (credential) Invalidate(string)                       {}

var cloudRepositoryUUID = uuid.MustParse("11111111-1111-4111-8111-111111111111")

var ref = platform.RepoRef{Platform: platform.KindBitbucket, Host: "bitbucket.org", Owner: "team", Name: "widgets", BitbucketRepositoryUUID: cloudRepositoryUUID}

func client(t *testing.T, token string, handle func(*http.Request) (int, string)) *bitbucket.Client {
	assert := assert.New(t)
	t.Helper()
	c, err := bitbucket.NewClient("bitbucket.org", credential(token), platform.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.Equal("api.bitbucket.org", r.URL.Host)
		code, body := handle(r)
		return &http.Response{StatusCode: code, Status: http.StatusText(code), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}), nil)
	require.NoError(t, err)
	return c
}

func TestRepositoryIdentityAndAuthentication(t *testing.T) {
	assert := assert.New(t)
	for _, token := range []string{"bearer-secret", "user@example.com:api-secret"} {
		t.Run(token, func(t *testing.T) {
			c := client(t, token, func(r *http.Request) (int, string) {
				if strings.Contains(r.URL.Path, "/permissions/") {
					assert.Equal("/2.0/user/workspaces/new-team/permissions/repositories", r.URL.Path)
					return 403, `{}`
				}
				assert.Equal("/2.0/repositories/{}/{11111111-1111-4111-8111-111111111111}", r.URL.Path)
				if strings.Contains(token, ":") {
					u, p, ok := r.BasicAuth()
					assert.True(ok)
					assert.Equal("user@example.com", u)
					assert.Equal("api-secret", p)
				} else {
					assert.Equal("Bearer bearer-secret", r.Header.Get("Authorization"))
				}
				return 200, `{"uuid":"{11111111-1111-4111-8111-111111111111}","full_name":"new-team/renamed","has_issues":false,"mainbranch":{"name":"main"},"links":{"html":{"href":"https://bitbucket.org/new-team/renamed"},"clone":[{"name":"https","href":"https://bitbucket.org/new-team/renamed.git"}]}}`
			})
			r, err := c.GetRepository(t.Context(), ref)
			require.NoError(t, err)
			assert.Equal(cloudRepositoryUUID, r.Ref.BitbucketRepositoryUUID)
			assert.Equal("new-team/renamed", r.Ref.RepoPath)
			assert.Equal("https://bitbucket.org/new-team/renamed.git", r.CloneURL)
			assert.Equal("main", r.DefaultBranch)
		})
	}
}

func TestRepositoryLookupWithoutSavedIdentity(t *testing.T) {
	lookup := ref
	lookup.BitbucketRepositoryUUID = uuid.Nil()
	c := client(t, "secret", func(r *http.Request) (int, string) {
		assert.Equal(t, "/2.0/repositories/team/widgets", r.URL.Path)
		return 200, `{"uuid":"{11111111-1111-4111-8111-111111111111}","full_name":"team/widgets"}`
	})
	repo, err := c.GetRepository(t.Context(), lookup)
	require.NoError(t, err)
	assert.Equal(t, cloudRepositoryUUID, repo.Ref.BitbucketRepositoryUUID)
}

func TestRepositoryLookupRejectsDifferentIdentity(t *testing.T) {
	c := client(t, "secret", func(r *http.Request) (int, string) {
		assert.Equal(t, "/2.0/repositories/{}/{11111111-1111-4111-8111-111111111111}", r.URL.Path)
		return 200, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"new-team/renamed"}`
	})
	repo, err := c.GetRepository(t.Context(), ref)
	require.ErrorIs(t, err, platform.ErrProviderContract)
	assert.Equal(t, uuid.Nil(), repo.Ref.BitbucketRepositoryUUID)
}

func TestPullPaginationAndNormalization(t *testing.T) {
	assert := assert.New(t)
	calls := 0
	c := client(t, "secret", func(r *http.Request) (int, string) {
		calls++
		assert.Equal("Bearer secret", r.Header.Get("Authorization"))
		if calls == 1 {
			assert.Equal("OPEN", r.URL.Query().Get("state"))
			return 200, `{"values":[{"id":7,"state":"OPEN","title":"First","author":{"uuid":"{author}","display_name":"User A"},"source":{"branch":{"name":"feature"},"commit":{"hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},"destination":{"branch":{"name":"main"},"commit":{"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},"created_on":"2026-09-01T14:00:00+02:00"}],"next":"https://api.bitbucket.org/2.0/next-page"}`
		}
		assert.Equal("/2.0/next-page", r.URL.Path)
		return 200, `{"values":[{"id":8,"state":"OPEN","title":"Second"}]}`
	})
	rows, err := c.ListOpenMergeRequests(t.Context(), ref)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(7, rows[0].Number)
	assert.Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", rows[0].HeadSHA)
	assert.Equal("{author}", rows[0].Author)
	assert.Equal("2026-09-01T12:00:00Z", rows[0].CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	assert.Equal(8, rows[1].Number)
	assert.Equal(2, calls)
}

func TestIncompletePagesReturnNoInventory(t *testing.T) {
	assert := assert.New(t)
	for _, next := range []string{"https://api.bitbucket.org/2.0/broken", "https://other.example.test/steal"} {
		t.Run(next, func(t *testing.T) {
			calls := 0
			c := client(t, "secret", func(r *http.Request) (int, string) {
				calls++
				if calls == 1 {
					return 200, `{"values":[{"id":7,"state":"OPEN"}],"next":"` + next + `"}`
				}
				return 200, `{malformed`
			})
			rows, err := c.ListOpenMergeRequests(t.Context(), ref)
			require.Error(t, err)
			assert.Nil(rows)
			if strings.Contains(next, "other.example.test") {
				assert.Equal(1, calls)
			}
		})
	}
}

func TestCommentsPreserveThreadsAndSkipDeleted(t *testing.T) {
	assert := assert.New(t)
	c := client(t, "secret", func(r *http.Request) (int, string) {
		assert.Equal("/2.0/repositories/team/{11111111-1111-4111-8111-111111111111}/pullrequests/7/comments/", r.URL.Path)
		return 200, `{"values":[{"id":10,"deleted":true},{"id":12,"content":{"raw":"reply"},"parent":{"id":10},"user":{"uuid":"{user}"}},{"id":13,"deleted":true},{"id":14,"inline":{"path":"main.go","to":2}}]}`
	})
	rows, err := c.ListMergeRequestEvents(t.Context(), ref, 7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal("10", rows[0].ThreadID)
	assert.Equal("reply", rows[0].Body)
}

func TestMergeAndStaleHead(t *testing.T) {
	assert := assert.New(t)
	for _, head := range []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} {
		t.Run(head, func(t *testing.T) {
			writes := 0
			c := client(t, "secret", func(r *http.Request) (int, string) {
				if r.Method == http.MethodGet {
					return 200, `{"id":7,"state":"OPEN","source":{"commit":{"hash":"` + head + `"}}}`
				}
				writes++
				assert.Equal("/2.0/repositories/team/{11111111-1111-4111-8111-111111111111}/pullrequests/7/merge", r.URL.Path)
				var body map[string]any
				require.NoError(t, json.UnmarshalRead(r.Body, &body))
				assert.Equal("squash", body["merge_strategy"])
				assert.Equal("Title\n\nDetails", body["message"])
				return 200, `{"id":7,"state":"MERGED","merge_commit":{"hash":"merged"}}`
			})
			result, err := c.MergeMergeRequest(t.Context(), ref, 7, "Title", "Details", "squash", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			if head == "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
				require.ErrorIs(t, err, platform.ErrStaleState)
				assert.Zero(writes)
			} else {
				require.NoError(t, err)
				assert.True(result.Merged)
				assert.Equal("merged", result.SHA)
				assert.Equal(1, writes)
			}
		})
	}
}

func TestErrorMappingAndCancellation(t *testing.T) {
	assert := assert.New(t)
	for code, want := range map[int]error{401: platform.ErrPermissionDenied, 403: platform.ErrPermissionDenied, 404: platform.ErrNotFound, 429: platform.ErrRateLimited} {
		c := client(t, "secret", func(*http.Request) (int, string) { return code, `{}` })
		_, err := c.GetMergeRequest(t.Context(), ref, 7)
		require.ErrorIs(t, err, want)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c, err := bitbucket.NewClient("bitbucket.org", credential("secret"), platform.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	}), nil)
	require.NoError(t, err)
	_, err = c.GetMergeRequest(ctx, ref, 7)
	assert.ErrorIs(err, context.Canceled)
}

func TestInlineRepliesStayInReviewThreads(t *testing.T) {
	assert := assert.New(t)
	c := client(t, "secret", func(*http.Request) (int, string) {
		return 200, `{"values":[{"id":10,"inline":{"path":"main.go","from":3},"content":{"raw":"change this"}},{"id":12,"parent":{"id":10},"content":{"raw":"fixed"}}]}`
	})
	events, err := c.ListMergeRequestEvents(t.Context(), ref, 7)
	require.NoError(t, err)
	assert.Empty(events)
	threads, err := c.ListMergeRequestReviewThreads(t.Context(), ref, 7)
	require.NoError(t, err)
	require.Len(t, threads, 2)
	assert.Equal("10", threads[1].ProviderThreadID)
	assert.Equal("fixed", threads[1].Body)
	assert.Equal("LEFT", threads[1].Range.Side)
	assert.Equal(3, threads[1].Range.Line)
}

func TestApprovalRevokedAfterHeadChanges(t *testing.T) {
	assert := assert.New(t)
	reads, writes := 0, []string{}
	c := client(t, "secret", func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet {
			reads++
			head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			if reads > 1 {
				head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			}
			return 200, `{"id":7,"state":"OPEN","source":{"commit":{"hash":"` + head + `"}}}`
		}
		assert.Equal("/2.0/repositories/team/{11111111-1111-4111-8111-111111111111}/pullrequests/7/approve", r.URL.Path)
		writes = append(writes, r.Method)
		if r.Method == http.MethodDelete {
			return 204, ""
		}
		return 200, `{"user":{"uuid":"{reviewer}"},"approved":true,"participated_on":"2026-09-01T12:00:00Z"}`
	})
	_, err := c.ApproveMergeRequest(t.Context(), ref, 7, "", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.ErrorIs(t, err, platform.ErrStaleState)
	assert.Equal([]string{"POST", "DELETE"}, writes)
}

func TestIssueCreationAndCommentWrites(t *testing.T) {
	assert := assert.New(t)
	c := client(t, "secret", func(r *http.Request) (int, string) {
		assert.Equal("POST", r.Method)
		var body map[string]any
		require.NoError(t, json.UnmarshalRead(r.Body, &body))
		if strings.HasSuffix(r.URL.Path, "/issues") {
			assert.Equal("Bug", body["title"])
			assert.Equal(map[string]any{"raw": "Details"}, body["content"])
			return 201, `{"id":4,"state":"new","title":"Bug","content":{"raw":"Details"}}`
		}
		assert.Equal(map[string]any{"raw": "Reply"}, body["content"])
		return 201, `{"id":5,"content":{"raw":"Reply"}}`
	})
	issue, err := c.CreateIssue(t.Context(), ref, "Bug", "Details")
	require.NoError(t, err)
	assert.Equal(4, issue.Number)
	event, err := c.CreateIssueComment(t.Context(), ref, 4, "Reply")
	require.NoError(t, err)
	assert.Equal("Reply", event.Body)
	assert.Equal(int64(5), event.PlatformID)
}

func TestPullResolvesEmbeddedSourceRepositoryAndCommit(t *testing.T) {
	assert := assert.New(t)
	c := client(t, "secret", func(r *http.Request) (int, string) {
		if strings.Contains(r.URL.Path, "/commit/") {
			assert.Equal("/2.0/repositories/contributor/{33333333-3333-4333-8333-333333333333}/commit/abcdef012345", r.URL.Path)
			return 200, `{"hash":"abcdef0123456789abcdef0123456789abcdef01"}`
		}
		return 200, `{"id":7,"state":"OPEN","source":{"branch":{"name":"feature"},"commit":{"hash":"abcdef012345"},"repository":{"uuid":"{33333333-3333-4333-8333-333333333333}","full_name":"contributor/widgets"}}}`
	})
	pull, err := c.GetMergeRequest(t.Context(), ref, 7)
	require.NoError(t, err)
	assert.Equal("abcdef0123456789abcdef0123456789abcdef01", pull.HeadSHA)
	assert.Equal("https://bitbucket.org/contributor/widgets.git", pull.HeadRepoCloneURL)
	assert.False(pull.HeadRepoCloneURLUnknown)
}

func TestReviewerUpdatesPreserveExistingUsers(t *testing.T) {
	assert := assert.New(t)
	c := client(t, "secret", func(r *http.Request) (int, string) {
		assert.Equal("/2.0/repositories/team/{11111111-1111-4111-8111-111111111111}/pullrequests/7", r.URL.Path)
		if r.Method == http.MethodGet {
			return 200, `{"reviewers":[{"uuid":"{22222222-2222-4222-8222-222222222222}"}]}`
		}
		assert.Equal(http.MethodPut, r.Method)
		var body struct {
			Reviewers []struct {
				UUID string `json:"uuid"`
			} `json:"reviewers"`
		}
		require.NoError(t, json.UnmarshalRead(r.Body, &body))
		require.Len(t, body.Reviewers, 2)
		assert.Equal("{22222222-2222-4222-8222-222222222222}", body.Reviewers[0].UUID)
		assert.Equal("{33333333-3333-4333-8333-333333333333}", body.Reviewers[1].UUID)
		return 200, `{"reviewers":[{"uuid":"{22222222-2222-4222-8222-222222222222}"},{"uuid":"{33333333-3333-4333-8333-333333333333}"}]}`
	})
	reviewers, err := c.RequestMergeRequestReviewers(t.Context(), ref, 7, []string{"{33333333-3333-4333-8333-333333333333}", "{22222222-2222-4222-8222-222222222222}"})
	require.NoError(t, err)
	assert.Equal([]string{"{22222222-2222-4222-8222-222222222222}", "{33333333-3333-4333-8333-333333333333}"}, reviewers)
}

func TestThreadResolutionUsesRootComment(t *testing.T) {
	var methods []string
	c := client(t, "secret", func(r *http.Request) (int, string) {
		assert.Equal(t, "/2.0/repositories/team/{11111111-1111-4111-8111-111111111111}/pullrequests/7/comments/12/resolve", r.URL.Path)
		methods = append(methods, r.Method)
		return 204, ""
	})
	require.NoError(t, c.ResolveDiffReviewThread(t.Context(), ref, 7, "12"))
	require.NoError(t, c.UnresolveDiffReviewThread(t.Context(), ref, 7, "12"))
	assert.Equal(t, []string{"POST", "DELETE"}, methods)
}

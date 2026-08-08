package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMCPTestServer(t *testing.T, mux *http.ServeMux) *Server {
	t.Helper()
	require := require.New(t)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	cfg := writeFakeDaemonFiles(t, ts, "")
	s, err := New(Options{ConfigPath: cfg, Version: "test"})
	require.NoError(err)
	s.agentHandoffPollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		require.NoError(s.Close())
	})
	return s
}

func TestRepoFilterInputQueryValue(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	tests := []struct {
		name    string
		filter  repoFilterInput
		want    string
		wantErr string
	}{
		{
			name:   "github default host",
			filter: repoFilterInput{Provider: "github", Owner: "acme", Name: "widget"},
			want:   "github|github.com/acme/widget",
		},
		{
			name:   "provider alias",
			filter: repoFilterInput{Provider: "GH", Owner: "acme", Name: "widget"},
			want:   "github|github.com/acme/widget",
		},
		{name: "empty filter"},
		{
			name:    "missing provider",
			filter:  repoFilterInput{Owner: "acme", Name: "widget"},
			wantErr: "provider",
		},
		{
			name:    "missing name",
			filter:  repoFilterInput{Provider: "github", Owner: "acme"},
			wantErr: "name",
		},
		{
			name:    "unknown provider without host",
			filter:  repoFilterInput{Provider: "nonesuch", Owner: "a", Name: "b"},
			wantErr: "nonesuch",
		},
		{
			name: "unknown provider with host",
			filter: repoFilterInput{
				Provider: "nonesuch", PlatformHost: "git.example.com", Owner: "a", Name: "b",
			},
			wantErr: "nonesuch",
		},
		{
			name: "gitlab explicit host",
			filter: repoFilterInput{
				Provider: "gitlab", PlatformHost: "git.example.com", Owner: "a", Name: "b",
			},
			want: "gitlab|git.example.com/a/b",
		},
		{
			name: "gitlab nested repo path",
			filter: repoFilterInput{
				Provider: "gitlab", PlatformHost: "git.example.com", RepoPath: "Group/SubGroup/Project",
			},
			want: "gitlab|git.example.com/Group/SubGroup/Project",
		},
		{
			name: "repo path takes precedence over owner name",
			filter: repoFilterInput{
				Provider: "gitlab", PlatformHost: "git.example.com",
				RepoPath: "Group/SubGroup/Project", Owner: "wrong", Name: "wrong",
			},
			want: "gitlab|git.example.com/Group/SubGroup/Project",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.filter.queryValue()
			if tt.wantErr != "" {
				require.Error(err)
				assert.Contains(err.Error(), tt.wantErr)
				return
			}
			require.NoError(err)
			assert.Equal(tt.want, got)
		})
	}
}

func TestListReposTool(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/summary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"repo": {"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","owner":"acme","name":"widget",
			"open_pr_count":3,"open_issue_count":2,
			"last_sync_completed_at":"2026-07-01T10:00:00Z","last_sync_error":""
		}]`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.listRepos(t.Context(), listReposInput{})
	require.NoError(err)
	require.Len(out.Repos, 1)
	assert.Equal("github", out.Repos[0].Provider)
	assert.Equal("acme/widget", out.Repos[0].RepoPath)
	assert.Equal(3, out.Repos[0].OpenPRCount)
	assert.Equal(2, out.Repos[0].OpenIssueCount)
	assert.Equal("2026-07-01T10:00:00Z", out.Repos[0].LastSyncCompletedAt)
}

func TestSearchItemsMergesAndOrders(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal("retry", query.Get("q"))
		assert.Equal("open", query.Get("state"))
		assert.Equal("26", query.Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"Number":42,"Title":"Retry budget","State":"open","Author":"alice",
			"URL":"https://example.test/pr/42","IsDraft":false,
			"KanbanStatus":"reviewing","LastActivityAt":"2026-07-01T14:00:00Z",
			"Body":"must not be decoded into MCP output",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		}]`))
	})
	mux.HandleFunc("/api/v1/issues", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal("retry", query.Get("q"))
		assert.Equal("open", query.Get("state"))
		assert.Equal("26", query.Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"Number":7,"Title":"Retry docs","State":"open","Author":"bob",
			"URL":"https://example.test/issues/7","WorkflowStatus":"",
			"LastActivityAt":"2026-07-01T15:00:00Z",
			"Body":"must not be decoded into MCP output",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		}]`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.searchItems(t.Context(), searchItemsInput{Query: "retry"})
	require.NoError(err)
	require.Len(out.Results, 2)
	assert.Equal("issue", out.Results[0].Item.Type)
	assert.Equal(7, out.Results[0].Item.Number)
	assert.Equal("new", out.Results[0].WorkflowStatus)
	assert.Equal("pr", out.Results[1].Item.Type)
	assert.Equal("reviewing", out.Results[1].WorkflowStatus)
	assert.False(out.Capped)
	raw, err := json.Marshal(out)
	require.NoError(err)
	assert.NotContains(string(raw), "must not be decoded")
}

func TestSearchItemsMergedStateFilter(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	issueCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("all", r.URL.Query().Get("state"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"Number":1,"Title":"merged","State":"merged","Author":"alice",
			"URL":"https://example.test/pr/1","IsDraft":false,
			"KanbanStatus":"","LastActivityAt":"2026-07-01T14:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		},{
			"Number":2,"Title":"open","State":"open","Author":"alice",
			"URL":"https://example.test/pr/2","IsDraft":false,
			"KanbanStatus":"","LastActivityAt":"2026-07-01T15:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		}]`))
	})
	mux.HandleFunc("/api/v1/issues", func(w http.ResponseWriter, r *http.Request) {
		issueCalls++
		w.WriteHeader(http.StatusInternalServerError)
	})
	s := newMCPTestServer(t, mux)

	out, err := s.searchItems(t.Context(), searchItemsInput{Query: "retry", State: "merged"})
	require.NoError(err)
	require.Len(out.Results, 1)
	assert.Equal("pr", out.Results[0].Item.Type)
	assert.Equal(1, out.Results[0].Item.Number)
	assert.Equal(0, issueCalls)
}

func TestSearchItemsPagesBeforeMergedStateFilter(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var offsets []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal("all", query.Get("state"))
		assert.Equal("2", query.Get("limit"))
		offsets = append(offsets, query.Get("offset"))
		w.Header().Set("Content-Type", "application/json")
		switch query.Get("offset") {
		case "":
			_, _ = w.Write([]byte(`[{
				"Number":1,"Title":"open newer","State":"open","Author":"alice",
				"URL":"https://example.test/pr/1","LastActivityAt":"2026-07-01T15:00:00Z",
				"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"}
			},{
				"Number":2,"Title":"closed newer","State":"closed","Author":"alice",
				"URL":"https://example.test/pr/2","LastActivityAt":"2026-07-01T14:00:00Z",
				"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"}
			}]`))
		case "2":
			_, _ = w.Write([]byte(`[{
				"Number":3,"Title":"merged older","State":"merged","Author":"alice",
				"URL":"https://example.test/pr/3","LastActivityAt":"2026-07-01T13:00:00Z",
				"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"}
			}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	s := newMCPTestServer(t, mux)

	out, err := s.searchItems(t.Context(), searchItemsInput{Query: "retry", State: "merged", Limit: 1})
	require.NoError(err)
	require.Len(out.Results, 1)
	assert.Equal(3, out.Results[0].Item.Number)
	assert.False(out.Capped)
	assert.Equal([]string{"", "2"}, offsets)
}

func TestSearchItemsReportsCappedWhenSourceHasMoreMatches(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal("3", query.Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"Number":1,"Title":"first","State":"open","Author":"alice",
			"URL":"https://example.test/pr/1","LastActivityAt":"2026-07-01T15:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"}
		},{
			"Number":2,"Title":"second","State":"open","Author":"alice",
			"URL":"https://example.test/pr/2","LastActivityAt":"2026-07-01T14:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"}
		},{
			"Number":3,"Title":"third","State":"open","Author":"alice",
			"URL":"https://example.test/pr/3","LastActivityAt":"2026-07-01T13:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"}
		}]`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.searchItems(t.Context(), searchItemsInput{Query: "retry", ItemTypes: []string{"pr"}, Limit: 2})
	require.NoError(err)
	require.Len(out.Results, 2)
	assert.True(out.Capped)
	assert.Equal(1, out.Results[0].Item.Number)
}

func TestListActivityPassthrough(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/activity", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal("2026-07-01T00:00:00Z", query.Get("since"))
		assert.Equal("github|github.com/acme/widget", query.Get("repo"))
		assert.True(slices.Equal([]string{"comment", "commit"}, query["types"]))
		assert.Equal("retry", query.Get("search"))
		assert.Equal("cursor-1", query.Get("after"))
		assert.Empty(query.Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{
			"id":"a1","cursor":"c1","activity_type":"comment",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"pr","item_number":42,"item_title":"Retry budget",
			"item_url":"https://example.test/pr/42","item_state":"open",
			"author":"bob","item_author":"alice",
			"created_at":"2026-07-01T15:00:00Z","body_preview":"please retry"
		},{
			"id":"a2","cursor":"c2","activity_type":"commit",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"pr","item_number":42,"item_title":"Retry budget",
			"item_url":"https://example.test/pr/42","item_state":"open",
			"author":"alice","item_author":"alice",
			"created_at":"2026-07-01T14:00:00Z","body_preview":"pushed retry fix"
		}],"capped":false}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.listActivity(t.Context(), listActivityInput{
		Since:  "2026-07-01T00:00:00Z",
		Repo:   repoFilterInput{Provider: "github", Owner: "acme", Name: "widget"},
		Types:  []string{"comment", "commit"},
		Search: "retry",
		Limit:  1,
		After:  "cursor-1",
	})
	require.NoError(err)
	require.Len(out.Items, 1)
	assert.True(out.Capped)
	assert.Equal("comment", out.Items[0].ActivityType)
	assert.Equal("pr", out.Items[0].Item.Type)
	assert.Equal(42, out.Items[0].Item.Number)
	assert.Equal("please retry", out.Items[0].BodyPreview)
}

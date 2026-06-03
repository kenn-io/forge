package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time assertion that liveClient satisfies Client.
var _ Client = (*liveClient)(nil)

func (m *mockClient) ListPullRequestTimelineEvents(
	_ context.Context, _, _ string, _ int,
) ([]PullRequestTimelineEvent, error) {
	m.trackCall()
	if m.timelineEventsErr != nil {
		return nil, m.timelineEventsErr
	}
	return m.timelineEvents, nil
}

func TestNewClientReturnsNonNil(t *testing.T) {
	c, err := NewClient("fake-token", "", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewClientEnterprise(t *testing.T) {
	c, err := NewClient("test-token", "github.mycompany.com", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewClientGitHubDotCom(t *testing.T) {
	c, err := NewClient("test-token", "github.com", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestNewClientEmptyHost(t *testing.T) {
	c, err := NewClient("test-token", "", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestGraphQLEndpointForHost(t *testing.T) {
	require.Equal(t, "https://api.github.com/graphql", graphQLEndpointForHost(""))
	require.Equal(t, "https://api.github.com/graphql", graphQLEndpointForHost("github.com"))
	require.Equal(t, "https://github.example.com/api/graphql", graphQLEndpointForHost("github.example.com"))
}

func TestClientInterfaceIncludesListForcePushEvents(t *testing.T) {
	_, ok := reflect.TypeFor[Client]().MethodByName("ListForcePushEvents")
	require.True(t, ok)
}

func TestClientInterfaceIncludesListPullRequestTimelineEvents(t *testing.T) {
	_, ok := reflect.TypeFor[Client]().MethodByName("ListPullRequestTimelineEvents")
	require.True(t, ok)
}

func TestClientInterfaceIncludesListPullRequestReviewThreads(t *testing.T) {
	_, ok := reflect.TypeFor[Client]().MethodByName("ListPullRequestReviewThreads")
	require.True(t, ok)
}

func TestListReleasesTracksRate(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	rt := NewRateTracker(database, "github.example.com", "rest")
	resetAt := time.Now().Add(time.Hour).Unix()
	var gotMethod string
	var gotPerPage string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/repos/acme/widgets/releases", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPerPage = r.URL.Query().Get("per_page")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4998")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
		_, _ = w.Write([]byte(`[{"tag_name":"v1.0.0","name":"Release v1.0.0"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ghClient, err := gh.NewClient(srv.Client()).WithEnterpriseURLs(
		srv.URL+"/api/v3/", srv.URL+"/api/uploads/",
	)
	require.NoError(err)
	c := &liveClient{gh: ghClient, rateTracker: rt}

	releases, err := c.ListReleases(t.Context(), "acme", "widgets", 2)
	require.NoError(err)
	require.Len(releases, 1)
	require.Equal(http.MethodGet, gotMethod)
	require.Equal("2", gotPerPage)
	require.Equal("v1.0.0", releases[0].GetTagName())
	require.Equal(1, rt.RequestsThisHour())
	require.Equal(4998, rt.Remaining())
	require.Equal(5000, rt.RateLimit())
}

func TestListTagsTracksRate(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	rt := NewRateTracker(database, "github.example.com", "rest")
	resetAt := time.Now().Add(time.Hour).Unix()
	var gotMethod string
	var gotPerPage string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/repos/acme/widgets/tags", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPerPage = r.URL.Query().Get("per_page")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4997")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
		_, _ = w.Write([]byte(`[{"name":"v1.0.0","commit":{"sha":"abcdef1234567890abcdef1234567890abcdef12"}}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ghClient, err := gh.NewClient(srv.Client()).WithEnterpriseURLs(
		srv.URL+"/api/v3/", srv.URL+"/api/uploads/",
	)
	require.NoError(err)
	c := &liveClient{gh: ghClient, rateTracker: rt}

	tags, err := c.ListTags(t.Context(), "acme", "widgets", 2)
	require.NoError(err)
	require.Len(tags, 1)
	require.Equal(http.MethodGet, gotMethod)
	require.Equal("2", gotPerPage)
	require.Equal("v1.0.0", tags[0].GetName())
	require.Equal("abcdef1234567890abcdef1234567890abcdef12", tags[0].GetCommit().GetSHA())
	require.Equal(1, rt.RequestsThisHour())
	require.Equal(4997, rt.Remaining())
	require.Equal(5000, rt.RateLimit())
}

func TestListOpenIssuesLogsFetchProgressForLargeIssueSet(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)

	var buf bytes.Buffer
	sw := &syncedWriter{w: &buf}
	h := slog.NewTextHandler(sw, &slog.HandlerOptions{Level: slog.LevelInfo})
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || page == 0 {
			page = 1
		}
		if page < 3 {
			nextURL := fmt.Sprintf(
				"%s/api/v3/repos/acme/widgets/issues?page=%d&per_page=100",
				serverURL, page+1,
			)
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, nextURL))
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(testIssuePage(page, now)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	serverURL = srv.URL

	ghClient, err := gh.NewClient(srv.Client()).WithEnterpriseURLs(
		srv.URL+"/api/v3/", srv.URL+"/api/uploads/",
	)
	require.NoError(err)
	c := &liveClient{gh: ghClient}

	issues, err := c.ListOpenIssues(t.Context(), "acme", "widgets")
	require.NoError(err)
	require.Len(issues, 201)

	logs := buf.String()
	assert.Contains(logs, `msg="issue list fetch started"`)
	assert.Contains(logs, "repo=acme/widgets")
	assert.Contains(logs, "platform=github")
	assert.Contains(logs, "host=github.com")
	assert.Contains(logs, "source=rest")
	assert.Contains(logs, "fetched=100")
	assert.Contains(logs, `msg="issue list fetch progress"`)
	assert.Contains(logs, "fetched=200")
	assert.Contains(logs, `msg="issue list fetch completed"`)
	assert.Contains(logs, "fetched=201")
	assert.Contains(logs, "total=201")
}

func testIssuePage(page int, now string) []map[string]any {
	count := 100
	if page == 3 {
		count = 1
	}
	start := ((page - 1) * 100) + 1
	issues := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		number := start + i
		issues = append(issues, map[string]any{
			"id":         number * 1000,
			"number":     number,
			"title":      fmt.Sprintf("Issue %d", number),
			"state":      "open",
			"html_url":   fmt.Sprintf("https://github.com/acme/widgets/issues/%d", number),
			"user":       map[string]any{"login": "alice"},
			"created_at": now,
			"updated_at": now,
		})
	}
	return issues
}

func TestListOpenPullRequestsLogsFetchProgressForLargePullRequestSet(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)

	var buf bytes.Buffer
	sw := &syncedWriter{w: &buf}
	h := slog.NewTextHandler(sw, &slog.HandlerOptions{Level: slog.LevelInfo})
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/repos/conda-forge/staged-recipes/pulls", func(w http.ResponseWriter, r *http.Request) {
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || page == 0 {
			page = 1
		}
		if page < 3 {
			nextURL := fmt.Sprintf(
				"%s/api/v3/repos/conda-forge/staged-recipes/pulls?page=%d&per_page=100",
				serverURL, page+1,
			)
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, nextURL))
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(testPullRequestPage(page, now)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	serverURL = srv.URL

	ghClient, err := gh.NewClient(srv.Client()).WithEnterpriseURLs(
		srv.URL+"/api/v3/", srv.URL+"/api/uploads/",
	)
	require.NoError(err)
	c := &liveClient{gh: ghClient}

	prs, err := c.ListOpenPullRequests(t.Context(), "conda-forge", "staged-recipes")
	require.NoError(err)
	require.Len(prs, 201)

	logs := buf.String()
	assert.Contains(logs, `msg="merge request list fetch started"`)
	assert.Contains(logs, "repo=conda-forge/staged-recipes")
	assert.Contains(logs, "platform=github")
	assert.Contains(logs, "host=github.com")
	assert.Contains(logs, "source=rest")
	assert.Contains(logs, "fetched=100")
	assert.Contains(logs, `msg="merge request list fetch progress"`)
	assert.Contains(logs, "fetched=200")
	assert.Contains(logs, `msg="merge request list fetch completed"`)
	assert.Contains(logs, "fetched=201")
	assert.Contains(logs, "total=201")
}

func testPullRequestPage(page int, now string) []map[string]any {
	count := 100
	if page == 3 {
		count = 1
	}
	start := ((page - 1) * 100) + 1
	prs := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		number := start + i
		prs = append(prs, map[string]any{
			"id":         number * 1000,
			"number":     number,
			"title":      fmt.Sprintf("Pull request %d", number),
			"state":      "open",
			"html_url":   fmt.Sprintf("https://github.com/conda-forge/staged-recipes/pull/%d", number),
			"user":       map[string]any{"login": "alice"},
			"created_at": now,
			"updated_at": now,
			"head":       map[string]any{"ref": "recipe", "sha": "abc123"},
			"base":       map[string]any{"ref": "main", "sha": "def456"},
		})
	}
	return prs
}

func TestListRepositoriesByOwnerUsesAuthenticatedEndpointForViewer(t *testing.T) {
	require := require.New(t)
	var paths []string
	var authenticatedAffiliation string
	var authenticatedType string
	var publicUserEndpointUsed bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"mariusvniekerk"}`))
	})
	mux.HandleFunc("/api/v3/user/repos", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		authenticatedAffiliation = r.URL.Query().Get("affiliation")
		authenticatedType = r.URL.Query().Get("type")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name":     "dotfiles2026",
			"private":  true,
			"fork":     false,
			"owner":    map[string]string{"login": "mariusvniekerk"},
			"archived": false,
		}})
	})
	mux.HandleFunc("/api/v3/users/mariusvniekerk/repos", func(w http.ResponseWriter, r *http.Request) {
		publicUserEndpointUsed = true
		http.Error(w, "unexpected endpoint", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ghClient, err := gh.NewClient(srv.Client()).WithEnterpriseURLs(
		srv.URL+"/api/v3/", srv.URL+"/api/uploads/",
	)
	require.NoError(err)
	c := &liveClient{gh: ghClient}

	repos, err := c.ListRepositoriesByOwner(t.Context(), "mariusvniekerk")
	require.NoError(err)
	require.Len(repos, 1)
	require.Equal("dotfiles2026", repos[0].GetName())
	require.True(repos[0].GetPrivate())
	require.Equal("owner", authenticatedAffiliation)
	require.Empty(authenticatedType)
	require.False(publicUserEndpointUsed)
	require.Equal([]string{
		"/api/v3/user",
		"/api/v3/user/repos?affiliation=owner&per_page=100",
	}, paths)
}

func TestListRepositoriesByOwnerUsesPublicUserEndpointForOtherUsers(t *testing.T) {
	require := require.New(t)
	var paths []string
	var userRepoType string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"mariusvniekerk"}`))
	})
	mux.HandleFunc("/api/v3/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		http.Error(w, "not an org", http.StatusNotFound)
	})
	mux.HandleFunc("/api/v3/users/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.String())
		userRepoType = r.URL.Query().Get("type")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name":  "public-repo",
			"owner": map[string]string{"login": "acme"},
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ghClient, err := gh.NewClient(srv.Client()).WithEnterpriseURLs(
		srv.URL+"/api/v3/", srv.URL+"/api/uploads/",
	)
	require.NoError(err)
	c := &liveClient{gh: ghClient}

	repos, err := c.ListRepositoriesByOwner(t.Context(), "acme")
	require.NoError(err)
	require.Len(repos, 1)
	require.Equal("public-repo", repos[0].GetName())
	require.Equal("owner", userRepoType)
	require.True(strings.HasPrefix(paths[1], "/api/v3/orgs/acme/repos?"))
	require.True(strings.HasPrefix(paths[2], "/api/v3/users/acme/repos?"))
}

func TestListForcePushEvents(t *testing.T) {
	require := require.New(t)
	var calls int
	var methods []string
	var contentTypes []string
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		calls++
		methods = append(methods, r.Method)
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[{"__typename":"HeadRefForcePushedEvent","id":"HFP_1","actor":{"login":"alice"},"beforeCommit":{"oid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"afterCommit":{"oid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"createdAt":"2024-06-01T12:00:00Z","ref":{"name":"feature"}}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"}}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[{"__typename":"HeadRefForcePushedEvent","id":"HFP_2","actor":{"login":"alice"},"beforeCommit":{"oid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"afterCommit":{"oid":"cccccccccccccccccccccccccccccccccccccccc"},"createdAt":"2024-06-01T12:05:00Z","ref":{"name":"feature"}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &liveClient{
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL + "/graphql",
	}

	events, err := c.ListForcePushEvents(t.Context(), "owner", "repo", 42)
	require.NoError(err)
	require.Len(events, 2)
	require.Equal("alice", events[0].Actor)
	require.Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", events[0].BeforeSHA)
	require.Equal("cccccccccccccccccccccccccccccccccccccccc", events[1].AfterSHA)
	require.Equal("feature", events[0].Ref)
	require.Equal(2, calls)
	require.Equal([]string{http.MethodPost, http.MethodPost}, methods)
	require.Equal([]string{"application/json", "application/json"}, contentTypes)
}

func TestListPullRequestTimelineEvents(t *testing.T) {
	require := require.New(t)
	var calls int
	var methods []string
	var contentTypes []string
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		calls++
		methods = append(methods, r.Method)
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[{"__typename":"HeadRefForcePushedEvent","id":"HFP_1","actor":{"login":"alice"},"beforeCommit":{"oid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"afterCommit":{"oid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"createdAt":"2024-06-01T12:00:00Z","ref":{"name":"feature"}},{"__typename":"RenamedTitleEvent","id":"RTE_1","actor":{"login":"bob"},"createdAt":"2024-06-01T12:05:00Z","previousTitle":"Old title","currentTitle":"New title"}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"}}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[{"__typename":"BaseRefChangedEvent","id":"BRC_1","actor":{"login":"carol"},"createdAt":"2024-06-01T12:10:00Z","previousRefName":"main","currentRefName":"release"},{"__typename":"CommentDeletedEvent","id":"CDE_1","actor":{"login":"maintainer"},"createdAt":"2024-06-01T12:12:00Z","deletedCommentAuthor":{"login":"reviewer"}},{"__typename":"CrossReferencedEvent","id":"CRE_1","actor":{"login":"dave"},"createdAt":"2024-06-01T12:15:00Z","isCrossRepository":true,"willCloseTarget":false,"source":{"__typename":"Issue","number":77,"title":"Related bug","url":"https://github.com/other/repo/issues/77","repository":{"owner":{"login":"other"},"name":"repo"}}},{"__typename":"AssignedEvent","id":"AE_1","actor":{"login":"wesm"},"assignee":{"__typename":"User","login":"wesm"},"createdAt":"2024-06-01T12:20:00Z"},{"__typename":"UnassignedEvent","id":"UE_1","actor":{"login":"alice"},"assignee":{"__typename":"User","login":"bob"},"createdAt":"2024-06-01T12:25:00Z"}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &liveClient{
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL + "/graphql",
	}

	events, err := c.ListPullRequestTimelineEvents(t.Context(), "owner", "repo", 42)
	require.NoError(err)
	require.Len(events, 7)
	require.Equal("force_push", events[0].EventType)
	require.Equal("HFP_1", events[0].NodeID)
	require.Equal("alice", events[0].Actor)
	require.Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", events[0].BeforeSHA)
	require.Equal("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", events[0].AfterSHA)
	require.Equal("feature", events[0].Ref)
	require.Equal("renamed_title", events[1].EventType)
	require.Equal("Old title", events[1].PreviousTitle)
	require.Equal("New title", events[1].CurrentTitle)
	require.Equal("base_ref_changed", events[2].EventType)
	require.Equal("main", events[2].PreviousRefName)
	require.Equal("release", events[2].CurrentRefName)
	require.Equal("comment_deleted", events[3].EventType)
	require.Equal("maintainer", events[3].Actor)
	require.Equal("reviewer", events[3].DeletedCommentAuthor)
	require.Equal("cross_referenced", events[4].EventType)
	require.Equal("Issue", events[4].SourceType)
	require.Equal("other", events[4].SourceOwner)
	require.Equal("repo", events[4].SourceRepo)
	require.Equal(77, events[4].SourceNumber)
	require.Equal("Related bug", events[4].SourceTitle)
	require.True(events[4].IsCrossRepository)
	require.False(events[4].WillCloseTarget)
	require.Equal("assigned", events[5].EventType)
	require.Equal("wesm", events[5].Actor)
	require.Equal("wesm", events[5].Assignee)
	require.Equal("unassigned", events[6].EventType)
	require.Equal("alice", events[6].Actor)
	require.Equal("bob", events[6].Assignee)
	require.Equal(2, calls)
	require.Equal([]string{http.MethodPost, http.MethodPost}, methods)
	require.Equal([]string{"application/json", "application/json"}, contentTypes)
}

func TestListPullRequestReviewThreads(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	var calls int
	var methods []string
	var contentTypes []string
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		calls++
		methods = append(methods, r.Method)
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"PRRT_1","isResolved":false,"isOutdated":false,"path":"src/main.go","line":12,"originalLine":12,"startLine":10,"originalStartLine":10,"diffSide":"RIGHT","comments":{"nodes":[{"id":"PRRC_1","databaseId":101,"fullDatabaseId":"3312100450","body":"inline note","path":"src/main.go","line":12,"originalLine":12,"subjectType":"LINE","diffHunk":"@@","url":"https://github.example/pr#discussion_r101","author":{"login":"reviewer"},"commit":{"oid":"head-sha"},"originalCommit":{"oid":"original-sha"},"pullRequestReview":{"databaseId":201},"createdAt":"2026-05-27T16:01:31Z","updatedAt":"2026-05-27T16:02:31Z"}],"pageInfo":{"hasNextPage":true,"endCursor":"comment-cursor-1"}}}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"}}}}}}`))
			return
		}
		if calls == 2 {
			_, _ = w.Write([]byte(`{"data":{"node":{"comments":{"nodes":[{"id":"PRRC_1_REPLY","databaseId":103,"fullDatabaseId":3312100451,"body":"reply note","path":"src/main.go","line":12,"originalLine":12,"subjectType":"LINE","diffHunk":"@@","url":"https://github.example/pr#discussion_r103","author":{"login":"maintainer"},"commit":{"oid":"head-sha"},"originalCommit":{"oid":"original-sha"},"pullRequestReview":{"databaseId":201},"createdAt":"2026-05-27T16:03:31Z","updatedAt":"2026-05-27T16:04:31Z"}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"PRRT_2","isResolved":true,"isOutdated":true,"path":"README.md","line":3,"originalLine":3,"startLine":null,"originalStartLine":null,"diffSide":"LEFT","comments":{"nodes":[{"id":"PRRC_2","databaseId":102,"fullDatabaseId":102,"body":"old note","path":"README.md","line":3,"originalLine":3,"subjectType":"FILE","diffHunk":"","url":"https://github.example/pr#discussion_r102","author":{"login":"maintainer"},"commit":{"oid":"new-head"},"originalCommit":{"oid":"old-head"},"pullRequestReview":{"databaseId":202},"createdAt":"2026-05-27T17:01:31Z","updatedAt":"2026-05-27T17:02:31Z"}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &liveClient{
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL + "/graphql",
	}

	threads, err := c.ListPullRequestReviewThreads(t.Context(), "owner", "repo", 42)
	require.NoError(err)
	require.Len(threads, 2)
	assert.Equal("PRRT_1", threads[0].NodeID)
	assert.False(threads[0].IsResolved)
	assert.False(threads[0].IsOutdated)
	assert.Equal("src/main.go", threads[0].Path)
	assert.Equal("RIGHT", threads[0].Side)
	require.NotNil(threads[0].StartLine)
	assert.Equal(10, *threads[0].StartLine)
	assert.Equal(12, threads[0].Line)
	require.Len(threads[0].Comments, 2)
	assert.Equal(int64(3312100450), threads[0].Comments[0].DatabaseID)
	assert.Equal(int64(201), threads[0].Comments[0].ReviewDatabaseID)
	assert.Equal("inline note", threads[0].Comments[0].Body)
	assert.Equal("LINE", threads[0].Comments[0].SubjectType)
	assert.Equal("reviewer", threads[0].Comments[0].AuthorLogin)
	assert.Equal("head-sha", threads[0].Comments[0].CommitID)
	assert.Equal("original-sha", threads[0].Comments[0].OriginalCommitID)
	assert.Equal(int64(3312100451), threads[0].Comments[1].DatabaseID)
	assert.Equal("reply note", threads[0].Comments[1].Body)
	assert.Equal("maintainer", threads[0].Comments[1].AuthorLogin)
	assert.True(threads[1].IsResolved)
	assert.True(threads[1].IsOutdated)
	assert.Equal("LEFT", threads[1].Side)
	assert.Equal("FILE", threads[1].Comments[0].SubjectType)
	assert.Equal(3, calls)
	assert.Equal([]string{http.MethodPost, http.MethodPost, http.MethodPost}, methods)
	assert.Equal([]string{"application/json", "application/json", "application/json"}, contentTypes)
}

func TestListPullRequestTimelineEventsReturnsGraphQLErrors(t *testing.T) {
	require := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"permission denied"}],"data":{"repository":{"pullRequest":{"timelineItems":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
	}))
	defer srv.Close()

	c := &liveClient{
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL,
	}

	events, err := c.ListPullRequestTimelineEvents(t.Context(), "owner", "repo", 42)
	require.Nil(events)
	require.ErrorContains(err, "permission denied")
}

func TestListIssueTimelineEvents(t *testing.T) {
	require := require.New(t)
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":{"repository":{"issue":{"timelineItems":{"nodes":[{"__typename":"AssignedEvent","id":"AE_1","actor":{"login":"wesm"},"assignee":{"__typename":"User","login":"wesm"},"createdAt":"2024-06-01T12:20:00Z"}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"}}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issue":{"timelineItems":{"nodes":[{"__typename":"UnassignedEvent","id":"UE_1","actor":{"login":"alice"},"assignee":{"__typename":"Mannequin","login":"bob"},"createdAt":"2024-06-01T12:25:00Z"}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &liveClient{
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL + "/graphql",
	}

	events, err := c.ListIssueTimelineEvents(t.Context(), "owner", "repo", 42)
	require.NoError(err)
	require.Len(events, 2)
	require.Equal("assigned", events[0].EventType)
	require.Equal("AE_1", events[0].NodeID)
	require.Equal("wesm", events[0].Actor)
	require.Equal("wesm", events[0].Assignee)
	require.Equal("unassigned", events[1].EventType)
	require.Equal("UE_1", events[1].NodeID)
	require.Equal("alice", events[1].Actor)
	require.Equal("bob", events[1].Assignee)
	require.Equal(2, calls)
}

func TestListPullRequestTimelineEventsRejectsNullGraphQLNodes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "null repository",
			body: `{"data":{"repository":null}}`,
			want: "missing repository",
		},
		{
			name: "null pull request",
			body: `{"data":{"repository":{"pullRequest":null}}}`,
			want: "missing pull request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := &liveClient{
				httpClient:      srv.Client(),
				graphQLEndpoint: srv.URL,
			}

			events, err := c.ListPullRequestTimelineEvents(t.Context(), "owner", "repo", 42)
			require.Nil(events)
			require.ErrorContains(err, tt.want)
		})
	}
}

func TestMarkPullRequestReadyForReviewUsesGraphQLMutation(t *testing.T) {
	require := require.New(t)
	var calls int
	var methods []string
	var contentTypes []string
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		calls++
		methods = append(methods, r.Method)
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_kwDOAAABc84"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"markPullRequestReadyForReview":{"pullRequest":{"databaseId":1001,"number":141,"title":"Ready PR","state":"OPEN","isDraft":false,"body":"body","url":"https://github.com/wesm/middleman/pull/141","author":{"login":"wesm"},"createdAt":"2026-04-14T00:00:00Z","updatedAt":"2026-04-14T00:05:00Z","mergedAt":null,"closedAt":null,"additions":12,"deletions":3,"mergeable":"MERGEABLE","reviewDecision":"APPROVED","headRefName":"feature","baseRefName":"main","headRefOid":"abc123","baseRefOid":"def456","headRepository":{"url":"https://github.com/wesm/middleman"},"labels":{"nodes":[]}}}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ghClient, err := gh.NewClient(srv.Client()).WithEnterpriseURLs(srv.URL+"/api/v3/", srv.URL+"/api/uploads/")
	require.NoError(err)

	c := &liveClient{
		gh:              ghClient,
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL + "/graphql",
	}

	pr, err := c.MarkPullRequestReadyForReview(t.Context(), "wesm", "middleman", 141)
	require.NoError(err)
	require.NotNil(pr)
	require.Equal(141, pr.GetNumber())
	require.Equal("Ready PR", pr.GetTitle())
	require.False(pr.GetDraft())
	require.Equal(2, calls)
	require.Equal([]string{http.MethodPost, http.MethodPost}, methods)
	require.Equal([]string{"application/json", "application/json"}, contentTypes)
}

func TestMarkPullRequestReadyForReviewReturnsTypedStaleStateError(t *testing.T) {
	require := require.New(t)
	call := 0
	var methods []string
	var contentTypes []string
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		call++
		methods = append(methods, r.Method)
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_kwDOAAABc84"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"markPullRequestReadyForReview":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a PullRequest with the global id of 'PR_kwDOAAABc84'."}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ghClient, err := gh.NewClient(srv.Client()).WithEnterpriseURLs(srv.URL+"/api/v3/", srv.URL+"/api/uploads/")
	require.NoError(err)

	c := &liveClient{
		gh:              ghClient,
		httpClient:      srv.Client(),
		graphQLEndpoint: srv.URL + "/graphql",
	}

	pr, err := c.MarkPullRequestReadyForReview(t.Context(), "wesm", "middleman", 141)
	require.Nil(pr)
	require.Error(err)
	require.ErrorContains(err, "Could not resolve to a PullRequest")

	var statusErr interface{ StatusCode() int }
	require.ErrorAs(err, &statusErr, "expected status-bearing error, got %T", err)
	require.Equal(http.StatusNotFound, statusErr.StatusCode())

	var staleErr interface{ IsStaleState() bool }
	require.ErrorAs(err, &staleErr, "expected stale-state error, got %T", err)
	require.True(staleErr.IsStaleState())
	require.Equal(2, call)
	require.Equal([]string{http.MethodPost, http.MethodPost}, methods)
	require.Equal([]string{"application/json", "application/json"}, contentTypes)
}

// TestNewClientWiresETagTransport verifies that NewClient keeps the
// etagTransport in the underlying http.Client's transport chain. The
// transport's behavior is exercised exhaustively in etag_transport_test.go;
// this test guards against the constructor silently dropping the wrap.
func TestNewClientWiresETagTransport(t *testing.T) {
	c, err := NewClient("fake-token", "", nil, nil)
	require.NoError(t, err)
	lc, ok := c.(*liveClient)
	require.Truef(t, ok, "expected *liveClient, got %T", c)
	transport := lc.gh.Client().Transport
	guard, ok := transport.(publicGitHubAPIGuardTransport)
	require.Truef(t, ok, "expected publicGitHubAPIGuardTransport at top of transport chain, got %T", transport)
	_, ok = guard.base.(*etagTransport)
	require.Truef(t, ok, "expected *etagTransport under public GitHub guard, got %T", guard.base)
}

package e2etest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/platform/forgejo"
	gitlabprovider "go.kenn.io/middleman/internal/platform/gitlab"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/tokenauth"
)

type staticTokenSource string

func (s staticTokenSource) Token(context.Context) (string, error) { return string(s), nil }
func (s staticTokenSource) Invalidate()                           {}
func (s staticTokenSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{}
}

type labelWireResponse struct {
	Labels []db.Label `json:"labels"`
}

type repoCapabilitiesWire []struct {
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Capabilities struct {
		ReadLabels    bool `json:"read_labels"`
		LabelMutation bool `json:"label_mutation"`
	} `json:"capabilities"`
}

// assertRepoLabelCapabilities checks the payload the label picker UI is
// gated on: both label capabilities must be advertised for the repo.
func assertRepoLabelCapabilities(t *testing.T, srv http.Handler) {
	t.Helper()
	rr := doJSONRequest(t, srv, http.MethodGet, "/api/v1/repos", nil)
	require.Equal(t, http.StatusOK, rr.Code, "response: %s", rr.Body.String())
	var body repoCapabilitiesWire
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(t, body, 1)
	assert.True(t, body[0].Capabilities.ReadLabels, "read_labels must be advertised")
	assert.True(t, body[0].Capabilities.LabelMutation, "label_mutation must be advertised")
}

func seedProviderRepo(
	t *testing.T,
	database *db.DB,
	kind platform.Kind,
	host string,
) int64 {
	t.Helper()
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform:     string(kind),
		PlatformHost: host,
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(t, err)
	return repoID
}

func seedProviderPRAndIssue(t *testing.T, database *db.DB, repoID int64) {
	t.Helper()
	now := time.Now().UTC()
	_, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         7,
		Title:          "Label target PR",
		Author:         "author",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(t, err)
	_, err = database.UpsertIssue(t.Context(), &db.Issue{
		RepoID:         repoID,
		PlatformID:     3001,
		Number:         11,
		Title:          "Label target issue",
		Author:         "author",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(t, err)
}

func newLabelTestServer(
	t *testing.T,
	database *db.DB,
	provider platform.Provider,
	kind platform.Kind,
	host string,
) *server.Server {
	t.Helper()
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil,
		[]ghclient.RepoRef{{
			Platform:     kind,
			PlatformHost: host,
			Owner:        "acme",
			Name:         "widget",
			RepoPath:     "acme/widget",
		}},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, srv.Shutdown(ctx))
	})
	return srv
}

func doJSONRequest(
	t *testing.T,
	srv http.Handler,
	method, path string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&payload).Encode(body))
	}
	req := httptest.NewRequest(method, path, &payload)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// fakeGitLabAPI serves the minimal GitLab v4 surface the label flows
// touch: project lookup, the project label catalog, and label
// assignment on a merge request and an issue.
type fakeGitLabAPI struct {
	mu             sync.Mutex
	mrLabelBody    map[string]any
	issueLabelBody map[string]any
}

func (f *fakeGitLabAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		key := r.Method + " " + r.URL.EscapedPath()
		switch key {
		case "GET /api/v4/projects/acme%2Fwidget":
			_, _ = w.Write([]byte(`{
				"id": 42,
				"path": "widget",
				"path_with_namespace": "acme/widget",
				"default_branch": "main",
				"web_url": "https://gitlab.com/acme/widget",
				"http_url_to_repo": "https://gitlab.com/acme/widget.git"
			}`))
		case "GET /api/v4/projects/42/labels":
			_, _ = w.Write([]byte(`[
				{"id": 4, "name": "bug", "color": "#d73a4a", "description": "Something is broken"},
				{"id": 5, "name": "triage", "color": "#fbca04", "description": "Needs review"}
			]`))
		case "PUT /api/v4/projects/42/merge_requests/7":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			f.mu.Lock()
			f.mrLabelBody = body
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"id": 1001, "iid": 7, "project_id": 42, "state": "opened", "labels": ["triage"]}`))
		case "PUT /api/v4/projects/42/issues/11":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			f.mu.Lock()
			f.issueLabelBody = body
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"id": 3001, "iid": 11, "project_id": 42, "state": "opened", "labels": ["triage"]}`))
		default:
			http.NotFound(w, r)
		}
	})
}

func setupGitLabLabelStack(t *testing.T) (*server.Server, *db.DB, int64, *fakeGitLabAPI) {
	t.Helper()
	database := dbtest.Open(t)
	fake := &fakeGitLabAPI{}
	upstream := httptest.NewServer(fake.handler(t))
	t.Cleanup(upstream.Close)

	client, err := gitlabprovider.NewClient(
		platform.DefaultGitLabHost,
		staticTokenSource("token"),
		gitlabprovider.WithBaseURLForTesting(upstream.URL+"/api/v4"),
	)
	require.NoError(t, err)

	repoID := seedProviderRepo(t, database, platform.KindGitLab, platform.DefaultGitLabHost)
	seedProviderPRAndIssue(t, database, repoID)
	srv := newLabelTestServer(t, database, client, platform.KindGitLab, platform.DefaultGitLabHost)
	return srv, database, repoID, fake
}

func TestGitLabListRepoLabelsSyncsCatalogFromProvider(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, repoID, _ := setupGitLabLabelStack(t)
	assertRepoLabelCapabilities(t, srv)

	rr := doJSONRequest(t, srv, http.MethodGet, "/api/v1/repo/gitlab/acme/widget/labels", nil)
	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	require.Eventually(func() bool {
		labels, _, err := database.ListRepoLabelCatalog(t.Context(), repoID)
		return err == nil && len(labels) == 2
	}, 2*time.Second, 10*time.Millisecond)

	labels, _, err := database.ListRepoLabelCatalog(t.Context(), repoID)
	require.NoError(err)
	require.Len(labels, 2)
	assert.Equal("bug", labels[0].Name)
	assert.Equal("#d73a4a", labels[0].Color)
	assert.Equal("Something is broken", labels[0].Description)
	assert.Equal("triage", labels[1].Name)
	assert.Equal("Needs review", labels[1].Description)
}

func TestGitLabSetPullLabelsUpdatesProviderAndDB(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, repoID, fake := setupGitLabLabelStack(t)

	rr := doJSONRequest(t, srv, http.MethodPut, "/api/v1/pulls/gitlab/acme/widget/7/labels", map[string][]string{
		"labels": {"triage"},
	})
	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	var body labelWireResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(body.Labels, 1)
	assert.Equal("triage", body.Labels[0].Name)

	fake.mu.Lock()
	sent := fake.mrLabelBody
	fake.mu.Unlock()
	require.NotNil(sent, "provider must receive the label update")
	assert.Equal("triage", sent["labels"])

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	require.Len(mr.Labels, 1)
	assert.Equal("triage", mr.Labels[0].Name)
}

func TestGitLabSetIssueLabelsUpdatesProviderAndDB(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, repoID, fake := setupGitLabLabelStack(t)

	rr := doJSONRequest(t, srv, http.MethodPut, "/api/v1/issues/gitlab/acme/widget/11/labels", map[string][]string{
		"labels": {"triage"},
	})
	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	fake.mu.Lock()
	sent := fake.issueLabelBody
	fake.mu.Unlock()
	require.NotNil(sent, "provider must receive the label update")
	assert.Equal("triage", sent["labels"])

	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 11)
	require.NoError(err)
	require.NotNil(issue)
	require.Len(issue.Labels, 1)
	assert.Equal("triage", issue.Labels[0].Name)
}

// fakeForgejoAPI serves the minimal Forgejo/Gitea v1 surface the label
// flows touch: the repo label catalog and issue-style label replacement
// (shared by pull requests and issues).
type fakeForgejoAPI struct {
	mu          sync.Mutex
	labels      string
	replaceBody map[int]map[string][]int64
}

func (f *fakeForgejoAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widget/labels":
			f.mu.Lock()
			labels := f.labels
			f.mu.Unlock()
			_, _ = w.Write([]byte(labels))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/repos/acme/widget/issues/7/labels",
			r.Method == http.MethodPut && r.URL.Path == "/api/v1/repos/acme/widget/issues/11/labels":
			number := 7
			if r.URL.Path == "/api/v1/repos/acme/widget/issues/11/labels" {
				number = 11
			}
			var body map[string][]int64
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			f.mu.Lock()
			if f.replaceBody == nil {
				f.replaceBody = make(map[int]map[string][]int64)
			}
			f.replaceBody[number] = body
			f.mu.Unlock()
			_, _ = w.Write([]byte(`[{"id": 12, "name": "triage", "color": "fbca04", "description": "Needs review"}]`))
		default:
			http.NotFound(w, r)
		}
	})
}

func setupForgejoLabelStack(t *testing.T) (*server.Server, *db.DB, int64, *fakeForgejoAPI) {
	t.Helper()
	database := dbtest.Open(t)
	fake := &fakeForgejoAPI{
		labels: `[
			{"id": 11, "name": "bug", "color": "d73a4a", "description": "Something is broken"},
			{"id": 12, "name": "triage", "color": "fbca04", "description": "Needs review"}
		]`,
	}
	upstream := httptest.NewServer(fake.handler(t))
	t.Cleanup(upstream.Close)

	client, err := forgejo.NewClient(
		platform.DefaultForgejoHost,
		staticTokenSource("token"),
		forgejo.WithBaseURLForTesting(upstream.URL),
	)
	require.NoError(t, err)

	repoID := seedProviderRepo(t, database, platform.KindForgejo, platform.DefaultForgejoHost)
	seedProviderPRAndIssue(t, database, repoID)
	srv := newLabelTestServer(t, database, client, platform.KindForgejo, platform.DefaultForgejoHost)
	return srv, database, repoID, fake
}

func TestForgejoListRepoLabelsSyncsCatalogFromProvider(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, repoID, _ := setupForgejoLabelStack(t)
	assertRepoLabelCapabilities(t, srv)

	rr := doJSONRequest(t, srv, http.MethodGet, "/api/v1/repo/forgejo/acme/widget/labels", nil)
	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	require.Eventually(func() bool {
		labels, _, err := database.ListRepoLabelCatalog(t.Context(), repoID)
		return err == nil && len(labels) == 2
	}, 2*time.Second, 10*time.Millisecond)

	labels, _, err := database.ListRepoLabelCatalog(t.Context(), repoID)
	require.NoError(err)
	require.Len(labels, 2)
	assert.Equal("bug", labels[0].Name)
	assert.Equal("d73a4a", labels[0].Color)
	assert.Equal("Something is broken", labels[0].Description)
	assert.Equal("triage", labels[1].Name)
}

func TestForgejoSetPullLabelsResolvesIDsAndUpdatesDB(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, repoID, fake := setupForgejoLabelStack(t)

	rr := doJSONRequest(t, srv, http.MethodPut, "/api/v1/pulls/forgejo/acme/widget/7/labels", map[string][]string{
		"labels": {"triage"},
	})
	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	var body labelWireResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(body.Labels, 1)
	assert.Equal("triage", body.Labels[0].Name)
	assert.Equal("fbca04", body.Labels[0].Color)

	fake.mu.Lock()
	sent := fake.replaceBody[7]
	fake.mu.Unlock()
	require.NotNil(sent, "provider must receive the label replacement")
	assert.Equal([]int64{12}, sent["labels"], "names must be resolved to label IDs")

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	require.Len(mr.Labels, 1)
	assert.Equal("triage", mr.Labels[0].Name)
}

func TestForgejoSetIssueLabelsResolvesIDsAndUpdatesDB(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, repoID, fake := setupForgejoLabelStack(t)

	rr := doJSONRequest(t, srv, http.MethodPut, "/api/v1/issues/forgejo/acme/widget/11/labels", map[string][]string{
		"labels": {"triage"},
	})
	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	fake.mu.Lock()
	sent := fake.replaceBody[11]
	fake.mu.Unlock()
	require.NotNil(sent, "provider must receive the label replacement")
	assert.Equal([]int64{12}, sent["labels"], "names must be resolved to label IDs")

	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 11)
	require.NoError(err)
	require.NotNil(issue)
	require.Len(issue.Labels, 1)
	assert.Equal("triage", issue.Labels[0].Name)
}

func TestForgejoSetLabelsFailsWhenCatalogNameVanishedUpstream(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, repoID, fake := setupForgejoLabelStack(t)

	// The DB catalog knows "ghost" (fresh, so no inline refresh runs)
	// but the provider no longer has it: name-to-ID resolution must
	// fail without touching the assignment endpoint.
	now := time.Now().UTC()
	require.NoError(database.ReplaceRepoLabelCatalog(t.Context(), repoID, []db.Label{
		{Name: "ghost", Color: "ffffff", UpdatedAt: now},
	}, now))

	rr := doJSONRequest(t, srv, http.MethodPut, "/api/v1/issues/forgejo/acme/widget/11/labels", map[string][]string{
		"labels": {"ghost"},
	})
	require.Equal(http.StatusNotFound, rr.Code, "response: %s", rr.Body.String())
	assert.Contains(rr.Body.String(), "ghost")

	fake.mu.Lock()
	sent := fake.replaceBody[11]
	fake.mu.Unlock()
	assert.Nil(sent, "assignment endpoint must not be called when resolution fails")

	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 11)
	require.NoError(err)
	require.NotNil(issue)
	assert.Empty(issue.Labels)
}

package kata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	katacatalog "go.kenn.io/forge/internal/kata"
)

const testKataIssueUID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestKataDaemonClientHealthPreservesPathPrefixSchemaVersionAndAuthorization(t *testing.T) {
	assert := assert.New(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/base/api/v1/health", r.URL.Path)
		assert.Equal("application/json", r.Header.Get("Accept"))
		assert.Equal("Bearer example-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"api_schema_version":"0.10.4"}`))
	}))
	defer upstream.Close()

	client := newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "example", URL: upstream.URL + "/base", Token: "example-token",
	})
	health, err := client.Health(t.Context())

	require.NoError(t, err)
	assert.Equal("connected", health.State)
	assert.Equal("0.10.4", health.APISchemaVersion)
}

func TestKataDaemonClientRejectsUnsafeLaunchTargets(t *testing.T) {
	for _, targetURL := range []string{
		"javascript:alert(document.cookie)",
		"/issues/" + testKataIssueUID,
		"https:///issues/" + testKataIssueUID,
		"https://user:password@kata.example.test/issues/" + testKataIssueUID,
	} {
		t.Run(targetURL, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(kataLaunchTarget{Available: true, URL: targetURL})
			}))
			defer upstream.Close()

			_, err := newTestKataDaemonClient(t, katacatalog.Daemon{
				ID: "example", URL: upstream.URL,
			}).LaunchTarget(t.Context(), testKataIssueUID)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid launch target")
		})
	}
}

func TestKataDaemonClientAcceptsHTTPLaunchTargets(t *testing.T) {
	for _, targetURL := range []string{
		"http://127.0.0.1:4222/issues/" + testKataIssueUID,
		"https://kata.example.test/issues/" + testKataIssueUID,
	} {
		t.Run(targetURL, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(kataLaunchTarget{Available: true, URL: targetURL})
			}))
			defer upstream.Close()

			target, err := newTestKataDaemonClient(t, katacatalog.Daemon{
				ID: "example", URL: upstream.URL,
			}).LaunchTarget(t.Context(), testKataIssueUID)

			require.NoError(t, err)
			assert.Equal(t, targetURL, target.URL)
		})
	}
}

func TestKataDaemonClientHealthKeepsMissingSchemaVersionVisible(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	health, err := newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "example", URL: upstream.URL,
	}).Health(t.Context())

	require.NoError(t, err)
	assert.Equal(t, kataDaemonHealth{State: "connected"}, health)
}

func TestKataDaemonClientReferencesPinsFiltersAndCapsLimit(t *testing.T) {
	assert := assert.New(t)
	issueUIDs := []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAA",
		"01ARZ3NDEKTSV4RRFFQ69G5FAB",
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v1/ui/references", r.URL.Path)
		assert.Equal("fix parser", r.URL.Query().Get("q"))
		assert.Equal("01ARZ3NDEKTSV4RRFFQ69G5FAC", r.URL.Query().Get("project_uid"))
		assert.Equal(issueUIDs, r.URL.Query()["issue_uid"])
		assert.Equal("200", r.URL.Query().Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[{"uid":"01ARZ3NDEKTSV4RRFFQ69G5FAA","project_uid":"01ARZ3NDEKTSV4RRFFQ69G5FAC","project_name":"Example","short_id":"5faa","qualified_id":"example#5faa","title":"Fix parser","status":"open"}]}`))
	}))
	defer upstream.Close()

	references, err := newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "example", URL: upstream.URL,
	}).References(t.Context(), kataReferenceQuery{
		Text:       "fix parser",
		ProjectUID: "01ARZ3NDEKTSV4RRFFQ69G5FAC",
		IssueUIDs:  issueUIDs,
		Limit:      500,
	})

	require.NoError(t, err)
	require.Len(t, references, 1)
	assert.Equal(kataIssueReference{
		UID:         "01ARZ3NDEKTSV4RRFFQ69G5FAA",
		ProjectUID:  "01ARZ3NDEKTSV4RRFFQ69G5FAC",
		ProjectName: "Example",
		ShortID:     "5faa",
		QualifiedID: "example#5faa",
		Title:       "Fix parser",
		Status:      "open",
	}, references[0])
}

func TestKataDaemonClientBareReferenceRejectsExactMatchHiddenBeyondReferenceLimit(t *testing.T) {
	assert := assert.New(t)
	var referenceSearchCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/ui/references":
			referenceSearchCalls++
			_, _ = w.Write([]byte(`{"issues":[{"uid":"issue-a","project_uid":"project-a","short_id":"shared"}]}`))
		case "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":1,"uid":"project-a","name":"Project A"},{"id":2,"uid":"project-b","name":"Project B"}]}`))
		case "/api/v1/ui/issue-reference":
			projectID := r.URL.Query().Get("project_id")
			assert.Equal("shared", r.URL.Query().Get("ref"))
			_, _ = w.Write([]byte(`{"issue":{"uid":"issue-` + projectID + `","project_uid":"project-` + projectID + `"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	resolved, found, err := newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "example", URL: upstream.URL,
	}).ResolveIssueReference(t.Context(), "", "shared")

	require.NoError(t, err)
	assert.False(found)
	assert.Equal(kataResolvedIssueReference{}, resolved)
	assert.Zero(referenceSearchCalls)
}

func TestKataDaemonClientQualifiedReferenceUsesCanonicalQualifiedID(t *testing.T) {
	assert := assert.New(t)
	var projectsCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/ui/references":
			assert.Equal("household-identity#rent", r.URL.Query().Get("q"))
			assert.Equal("200", r.URL.Query().Get("limit"))
			_, _ = w.Write([]byte(`{"issues":[{"uid":"issue-rent","project_uid":"project-household","project_name":"Household display name","short_id":"rent","qualified_id":"household-identity#rent","title":"Pay rent","status":"closed"}]}`))
		case "/api/v1/projects":
			projectsCalls++
			http.Error(w, "canonical qualifier is not a project display field", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	resolved, found, err := newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "example", URL: upstream.URL,
	}).ResolveIssueReference(t.Context(), "household-identity", "rent")

	require.NoError(t, err)
	assert.True(found)
	assert.Equal(kataResolvedIssueReference{
		UID: "issue-rent", ProjectUID: "project-household",
	}, resolved)
	assert.Zero(projectsCalls)
}

func TestKataDaemonClientIssueDetailPreservesAdditiveFields(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/issues/"+testKataIssueUID, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uid":"` + testKataIssueUID + `","future_field":{"nested":true}}`))
	}))
	defer upstream.Close()

	detail, err := newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "example", URL: upstream.URL,
	}).IssueDetail(t.Context(), testKataIssueUID)

	require.NoError(t, err)
	assert.JSONEq(t,
		`{"uid":"`+testKataIssueUID+`","future_field":{"nested":true}}`,
		string(detail),
	)
}

func TestKataDaemonClientLaunchTargetReturnsTypedUnavailableState(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/ui/launch-target", r.URL.Path)
		assert.Equal(t, testKataIssueUID, r.URL.Query().Get("issue_uid"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":false,"reason":"browser_origin_unavailable"}`))
	}))
	defer upstream.Close()

	target, err := newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "example", URL: upstream.URL,
	}).LaunchTarget(t.Context(), testKataIssueUID)

	require.NoError(t, err)
	assert.Equal(t, kataLaunchTarget{
		Available: false,
		Reason:    "browser_origin_unavailable",
	}, target)
}

func TestKataDaemonClientRejectsRedirects(t *testing.T) {
	var followed atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		followed.Store(true)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer source.Close()

	_, err := newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "example", URL: source.URL,
	}).Health(t.Context())

	require.Error(t, err)
	assert.False(t, followed.Load())
}

func TestKataDaemonClientHonorsContextCancellation(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "example", URL: upstream.URL,
	}).IssueDetail(ctx, testKataIssueUID)

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, requests.Load())
}

func TestKataDaemonClientRejectsDeclaredOversizeDetail(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "134217729")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, err := newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "example", URL: upstream.URL,
	}).IssueDetail(t.Context(), testKataIssueUID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestKataDaemonClientErrorsRedactDaemonSecrets(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer upstream.Close()
	parsed, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	parsed.User = url.UserPassword("example-user", "example-password")
	parsed.Path = "/private-path"
	parsed.RawQuery = "token=example-query-secret"

	_, err = newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "example", URL: parsed.String(), Token: "example-bearer-secret",
	}).Health(t.Context())

	require.Error(t, err)
	for _, secret := range []string{
		"example-user", "example-password", "private-path",
		"example-query-secret", "example-bearer-secret",
	} {
		assert.NotContains(t, err.Error(), secret)
	}
}

func TestKataDaemonClientReadsDetailOverUnixSocket(t *testing.T) {
	upstream := startTrackedKataUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/issues/"+testKataIssueUID, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uid":"` + testKataIssueUID + `"}`))
	}))

	detail, err := newTestKataDaemonClient(t, katacatalog.Daemon{
		ID: "desktop", URL: upstream.target, Local: true,
	}).IssueDetail(t.Context(), testKataIssueUID)

	require.NoError(t, err)
	assert.JSONEq(t, `{"uid":"`+testKataIssueUID+`"}`, string(detail))
	upstream.requireConnectionsDrained(t)
}

func newTestKataDaemonClient(t *testing.T, daemon katacatalog.Daemon) *kataDaemonClient {
	t.Helper()
	client, baseURL, err := New(Deps{}).kataDaemonHTTPClient(daemon)
	require.NoError(t, err)
	return &kataDaemonClient{daemon: daemon, client: client, baseURL: strings.TrimSuffix(baseURL, "/")}
}

package e2etest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

// fakeGitealikeUserAPI serves the minimal Gitea/Forgejo surface the
// assignee and reviewer mutations touch: pull/issue edits, the
// requested-reviewers endpoints, and the two read-back shapes. Gitea
// reads requested reviewers from the pull request itself; the Forgejo
// SDK lacks that field, so its read-back derives from REQUEST_REVIEW
// review rows. The fake serves both so each variant exercises its own
// path.
type fakeGitealikeUserAPI struct {
	mu                 sync.Mutex
	prAssignees        []string
	issueAssignees     []string
	requestedReviewers []string
	reviewerRequests   []string
}

func gitealikeUsersJSON(names []string) string {
	parts := make([]string, 0, len(names))
	for i, name := range names {
		parts = append(parts, fmt.Sprintf(`{"id": %d, "login": %q}`, 100+i, name))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (f *fakeGitealikeUserAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		key := r.Method + " " + r.URL.Path
		switch key {
		case "GET /api/v1/version":
			_, _ = w.Write([]byte(`{"version": "1.26.0"}`))
		case "PATCH /api/v1/repos/acme/widget/pulls/7":
			var body struct {
				Assignees []string `json:"assignees"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			f.mu.Lock()
			if body.Assignees != nil {
				f.prAssignees = body.Assignees
			}
			assignees := gitealikeUsersJSON(f.prAssignees)
			f.mu.Unlock()
			_, _ = fmt.Fprintf(w,
				`{"id": 1001, "number": 7, "state": "open", "title": "Label target PR", "assignees": %s}`,
				assignees,
			)
		case "PATCH /api/v1/repos/acme/widget/issues/11":
			var body struct {
				Assignees []string `json:"assignees"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			f.mu.Lock()
			if body.Assignees != nil {
				f.issueAssignees = body.Assignees
			}
			assignees := gitealikeUsersJSON(f.issueAssignees)
			f.mu.Unlock()
			_, _ = fmt.Fprintf(w,
				`{"id": 3001, "number": 11, "state": "open", "title": "Label target issue", "assignees": %s}`,
				assignees,
			)
		case "GET /api/v1/repos/acme/widget/pulls/7":
			f.mu.Lock()
			reviewers := gitealikeUsersJSON(f.requestedReviewers)
			assignees := gitealikeUsersJSON(f.prAssignees)
			f.mu.Unlock()
			_, _ = fmt.Fprintf(w,
				`{"id": 1001, "number": 7, "state": "open", "title": "Label target PR", "assignees": %s, "requested_reviewers": %s}`,
				assignees, reviewers,
			)
		case "GET /api/v1/repos/acme/widget/pulls/7/reviews":
			f.mu.Lock()
			parts := make([]string, 0, len(f.requestedReviewers))
			for i, name := range f.requestedReviewers {
				parts = append(parts, fmt.Sprintf(
					`{"id": %d, "user": {"id": %d, "login": %q}, "state": "REQUEST_REVIEW"}`,
					200+i, 100+i, name,
				))
			}
			f.mu.Unlock()
			_, _ = w.Write([]byte("[" + strings.Join(parts, ",") + "]"))
		case "POST /api/v1/repos/acme/widget/pulls/7/requested_reviewers",
			"DELETE /api/v1/repos/acme/widget/pulls/7/requested_reviewers":
			var body struct {
				Reviewers []string `json:"reviewers"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			f.mu.Lock()
			f.reviewerRequests = append(f.reviewerRequests, strings.ToLower(r.Method)+":"+strings.Join(body.Reviewers, ","))
			if r.Method == http.MethodPost {
				for _, name := range body.Reviewers {
					if !slices.Contains(f.requestedReviewers, name) {
						f.requestedReviewers = append(f.requestedReviewers, name)
					}
				}
			} else {
				kept := f.requestedReviewers[:0]
				for _, name := range f.requestedReviewers {
					if !slices.Contains(body.Reviewers, name) {
						kept = append(kept, name)
					}
				}
				f.requestedReviewers = kept
			}
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}

func setupGitealikeUserStack(
	t *testing.T,
	variant gitealikeLabelVariant,
	fake *fakeGitealikeUserAPI,
) (*server.Server, *db.DB, int64) {
	t.Helper()
	database := dbtest.Open(t)
	upstream := httptest.NewServer(fake.handler(t))
	t.Cleanup(upstream.Close)

	client := variant.newClient(t, upstream.URL)

	repoID := seedProviderRepo(t, database, variant.kind, variant.host)
	seedProviderPRAndIssue(t, database, repoID)
	srv := newLabelTestServer(t, database, client, variant.kind, variant.host)
	return srv, database, repoID
}

func TestGitealikeSetPullAssigneesUpdatesProviderAndDB(t *testing.T) {
	for _, variant := range gitealikeLabelVariants() {
		t.Run(variant.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			fake := &fakeGitealikeUserAPI{}
			srv, database, repoID := setupGitealikeUserStack(t, variant, fake)

			rr := doJSONRequest(t, srv, http.MethodPut, "/api/v1/pulls/"+variant.route+"/acme/widget/7/assignees", map[string][]string{
				"assignees": {"alice", "bob"},
			})
			require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

			var body struct {
				Assignees []string `json:"assignees"`
			}
			require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
			assert.Equal([]string{"alice", "bob"}, body.Assignees)
			assert.Equal([]string{"alice", "bob"}, fake.prAssignees)

			pr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
			require.NoError(err)
			require.NotNil(pr)
			assert.Equal([]string{"alice", "bob"}, pr.Assignees)
		})
	}
}

func TestGitealikeSetIssueAssigneesUpdatesProviderAndDB(t *testing.T) {
	for _, variant := range gitealikeLabelVariants() {
		t.Run(variant.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			fake := &fakeGitealikeUserAPI{}
			srv, database, repoID := setupGitealikeUserStack(t, variant, fake)

			rr := doJSONRequest(t, srv, http.MethodPut, "/api/v1/issues/"+variant.route+"/acme/widget/11/assignees", map[string][]string{
				"assignees": {"dana"},
			})
			require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

			var body struct {
				Assignees []string `json:"assignees"`
			}
			require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
			assert.Equal([]string{"dana"}, body.Assignees)
			assert.Equal([]string{"dana"}, fake.issueAssignees)

			issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 11)
			require.NoError(err)
			require.NotNil(issue)
			assert.Equal([]string{"dana"}, issue.Assignees)
		})
	}
}

func TestGitealikeSetPullReviewersRequestsAndRemovesThroughAPI(t *testing.T) {
	for _, variant := range gitealikeLabelVariants() {
		t.Run(variant.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			// carol starts requested; replacing the set with alice must
			// request alice and remove carol via the provider's
			// request/remove endpoints, then persist the read-back.
			fake := &fakeGitealikeUserAPI{requestedReviewers: []string{"carol"}}
			srv, database, repoID := setupGitealikeUserStack(t, variant, fake)

			rr := doJSONRequest(t, srv, http.MethodPut, "/api/v1/pulls/"+variant.route+"/acme/widget/7/reviewers", map[string][]string{
				"reviewers": {"alice"},
			})
			require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

			var body struct {
				Reviewers []string `json:"reviewers"`
			}
			require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
			assert.Equal([]string{"alice"}, body.Reviewers)
			assert.Equal([]string{"post:alice", "delete:carol"}, fake.reviewerRequests)
			assert.Equal([]string{"alice"}, fake.requestedReviewers)

			pr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
			require.NoError(err)
			require.NotNil(pr)
			assert.Equal([]string{"alice"}, pr.RequestedReviewers)
		})
	}
}

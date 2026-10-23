package apitest

import (
	"encoding/json"
	"net/http"
	"testing"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

type membershipChange struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

func labelNamesOf(labels []db.Label) []string {
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		names = append(names, label.Name)
	}
	return names
}

func TestAPIUpdatePullLabelsAppliesToProviderLabels(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, providerClient, _ := setupLabelTestServer(t)
	seedPRWithLabels(t, database, "acme", "widget", 1, []db.Label{{Name: "bug", Color: "d73a4a"}})
	repoID := seedRepoLabelCatalog(t, database, "acme", "widget")
	// Someone added "triage" on the provider after Forge last synced.
	providerClient.PRs["acme/widget"][0].Labels = []*gh.Label{{Name: "bug"}, {Name: "triage"}}
	path := "/api/v1/pulls/github/acme/widget/1/labels"

	rr := doLabelAPIRequest(t, srv, http.MethodPatch, path, membershipChange{Add: []string{}, Remove: []string{"bug"}})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body labelAPIResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal([]string{"triage"}, labelNamesOf(body.Labels))

	rr = doLabelAPIRequest(t, srv, http.MethodPatch, path, membershipChange{Add: []string{"bug"}, Remove: []string{}})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	pr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.ElementsMatch([]string{"triage", "bug"}, labelNamesOf(pr.Labels))
	require.Len(providerClient.PRs["acme/widget"][0].Labels, 2)
}

func TestAPIUpdateIssueLabelsKeepsLabelsTheRequestDoesNotMention(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _, _ := setupLabelTestServer(t)
	seedIssue(t, database, "acme", "widget", 7, "open")
	repoID := seedRepoLabelCatalog(t, database, "acme", "widget")
	path := "/api/v1/issues/github/acme/widget/7/labels"

	rr := doLabelAPIRequest(t, srv, http.MethodPatch, path, membershipChange{Add: []string{"triage"}, Remove: []string{}})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	rr = doLabelAPIRequest(t, srv, http.MethodPatch, path, membershipChange{Add: []string{"bug"}, Remove: []string{}})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(issue)
	assert.ElementsMatch([]string{"triage", "bug"}, labelNamesOf(issue.Labels))
}

func TestAPIUpdatePullLabelsRejectsAddsOutsideCatalog(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, database, providerClient, _ := setupLabelTestServer(t)
	seedPRWithLabels(t, database, "acme", "widget", 1, []db.Label{{Name: "bug", Color: "d73a4a"}})
	seedRepoLabelCatalog(t, database, "acme", "widget")

	rr := doLabelAPIRequest(t, srv, http.MethodPatch, "/api/v1/pulls/github/acme/widget/1/labels",
		membershipChange{Add: []string{"missing"}, Remove: []string{}})
	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Empty(providerClient.PRs["acme/widget"][0].Labels)
}

func TestAPIUpdatePullAssigneesKeepsAssigneesTheRequestDoesNotMention(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _, _ := setupAssigneeTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	path := "/api/v1/pulls/github/acme/widget/1/assignees"

	rr := doLabelAPIRequest(t, srv, http.MethodPut, path, map[string][]string{"assignees": {"alice"}})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	rr = doLabelAPIRequest(t, srv, http.MethodPatch, path, membershipChange{Add: []string{"bob"}, Remove: []string{}})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body assigneesAPIResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal([]string{"alice", "bob"}, body.Assignees)

	// Usernames match without case, as the providers treat them.
	rr = doLabelAPIRequest(t, srv, http.MethodPatch, path, membershipChange{Add: []string{"BOB"}, Remove: []string{"ALICE"}})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal([]string{"bob"}, body.Assignees)
}

func TestAPIUpdateIssueAssigneesAppliesToProviderAssignees(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, providerClient, _ := setupAssigneeTestServer(t)
	seedIssue(t, database, "acme", "widget", 7, "open")
	// Someone assigned "zed" on the provider after Forge last synced.
	providerClient.Issues["acme/widget"][0].Assignees = []*gh.User{{Login: new("zed")}}
	path := "/api/v1/issues/github/acme/widget/7/assignees"

	rr := doLabelAPIRequest(t, srv, http.MethodPatch, path, membershipChange{Add: []string{"dana"}, Remove: []string{}})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	rr = doLabelAPIRequest(t, srv, http.MethodPatch, path, membershipChange{Add: []string{"erin"}, Remove: []string{}})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body assigneesAPIResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal([]string{"zed", "dana", "erin"}, body.Assignees)
	providerIssue := providerClient.Issues["acme/widget"][0]
	require.Len(providerIssue.Assignees, 3)
}

func TestAPIUpdatePullLabelsRequiresAddAndRemove(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, database, _, _ := setupLabelTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := doLabelAPIRequest(t, srv, http.MethodPatch, "/api/v1/pulls/github/acme/widget/1/labels",
		map[string][]string{"add": {"bug"}})
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
}

func TestAPIUpdateIssueAssigneesDoesNotRestoreUpstreamUnassignment(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, providerClient, _ := setupAssigneeTestServer(t)
	seedIssue(t, database, "acme", "widget", 7, "open")
	path := "/api/v1/issues/github/acme/widget/7/assignees"

	rr := doLabelAPIRequest(t, srv, http.MethodPut, path, map[string][]string{"assignees": {"octo"}})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	// Someone unassigned "octo" on the provider; Forge still has the old set.
	providerClient.Issues["acme/widget"][0].Assignees = nil

	rr = doLabelAPIRequest(t, srv, http.MethodPatch, path, membershipChange{Add: []string{"dana"}, Remove: []string{}})
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body assigneesAPIResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal([]string{"dana"}, body.Assignees)
}

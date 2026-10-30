package github

import (
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func requiredChecksClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := newEnterpriseGHClient(server.Client(), server.URL+"/", server.URL+"/")
	require.NoError(t, err)
	return &Client{gh: client, rateTracker: &workflowRateObserver{}}
}

// The payloads follow live responses from both endpoints: a ruleset requiring a
// context and a workflow, and a protection summary in its enabled shape.
func TestGetRequiredChecksCombinesRulesetsAndBranchProtection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var paths []string
	client := requiredChecksClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		switch r.URL.EscapedPath() {
		case "/api/v3/repos/acme/widgets/rules/branches/release%2F1.x":
			_, _ = w.Write([]byte(`[
				{"type":"workflows","ruleset_source":"acme","ruleset_source_type":"Organization","ruleset_id":1,
				 "parameters":{"do_not_enforce_on_create":false,"workflows":[{"path":".github/workflows/gate.yml","ref":"refs/tags/v1","repository_id":7}]}},
				{"type":"required_status_checks","ruleset_source":"acme/widgets","ruleset_source_type":"Repository","ruleset_id":2,
				 "parameters":{"do_not_enforce_on_create":false,"strict_required_status_checks_policy":false,
				  "required_status_checks":[{"context":"deploy-gate","integration_id":42},{"context":"shared"}]}}
			]`))
		case "/api/v3/repos/acme/widgets/branches/release%2F1.x":
			_, _ = w.Write([]byte(`{"name":"release/1.x","protected":true,"protection":{"enabled":true,
				"required_status_checks":{"enforcement_level":"non_admins","contexts":["build","shared"],
				"checks":[{"context":"build","app_id":null},{"context":"shared","app_id":null}]}}}`))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	})

	required, err := client.GetRequiredChecks(t.Context(), "acme", "widgets", "release/1.x")

	require.NoError(err)
	assert.Equal([]string{"build", "deploy-gate", "shared"}, required.Contexts)
	assert.Equal([]string{".github/workflows/gate.yml"}, required.WorkflowPaths)
	assert.Len(paths, 2)
}

func TestGetRequiredChecksTreatsARefusedSourceAsUnknown(t *testing.T) {
	require := require.New(t)
	client := requiredChecksClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/repos/acme/widgets/rules/branches/main" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"name":"main","protection":{"required_status_checks":{"contexts":["build"],"checks":[]}}}`))
	})

	required, err := client.GetRequiredChecks(t.Context(), "acme", "widgets", "main")

	require.NoError(err)
	assert.Equal(t, []string{"build"}, required.Contexts)
}

func TestGetRequiredChecksFailsOnAServerError(t *testing.T) {
	client := requiredChecksClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	_, err := client.GetRequiredChecks(t.Context(), "acme", "widgets", "main")

	require.Error(t, err)
}

func TestWithBlockingChecks(t *testing.T) {
	passing := func() []platform.CICheck {
		return []platform.CICheck{{Name: "build", Status: "completed", Conclusion: "success"}}
	}
	buildRun := []*gh.CheckRun{{Name: new("build"), CheckSuite: &gh.CheckSuite{ID: new(int64(100))}}}
	workflowRun := func(id, workflowID, suiteID int64, event, name, path, conclusion string) *gh.WorkflowRun {
		return &gh.WorkflowRun{
			ID: &id, WorkflowID: &workflowID, CheckSuiteID: &suiteID, Event: &event,
			Name: &name, Path: &path, Status: new("completed"), Conclusion: &conclusion,
			HTMLURL: new("https://example.test/runs/1"),
		}
	}

	type row struct {
		Name, Status, Conclusion, Origin string
		Required                         bool
	}
	tests := []struct {
		name     string
		required RequiredChecks
		runs     []*gh.WorkflowRun
		want     []row
	}{
		{
			name:     "required check that never reported",
			required: RequiredChecks{Contexts: []string{"deploy-gate"}},
			want: []row{
				{Name: "build", Status: "completed", Conclusion: "success"},
				{Name: "deploy-gate", Status: "expected", Origin: "required_rule", Required: true},
			},
		},
		{
			name:     "every required check passing adds nothing",
			required: RequiredChecks{Contexts: []string{"build"}},
			runs:     []*gh.WorkflowRun{workflowRun(1, 10, 100, "pull_request", "CI", ".github/workflows/ci.yml", "success")},
			want:     []row{{Name: "build", Status: "completed", Conclusion: "success", Required: true}},
		},
		{
			name: "run that failed at startup",
			runs: []*gh.WorkflowRun{workflowRun(2, 20, 200, "pull_request_target", "Gate", ".github/workflows/gate.yml", "startup_failure")},
			want: []row{
				{Name: "build", Status: "completed", Conclusion: "success"},
				{Name: "Gate", Status: "completed", Conclusion: "startup_failure", Origin: "workflow_run"},
			},
		},
		{
			name: "invalid workflow file fails with no check",
			runs: []*gh.WorkflowRun{workflowRun(3, 30, 300, "pull_request", ".github/workflows/bad.yml", ".github/workflows/bad.yml", "failure")},
			want: []row{
				{Name: ".github/workflows/bad.yml", Status: "completed", Conclusion: "failure", Origin: "workflow_run"},
				{Name: "build", Status: "completed", Conclusion: "success"},
			},
		},
		{
			name: "failed run whose check runs are already listed adds nothing",
			runs: []*gh.WorkflowRun{workflowRun(4, 40, 100, "pull_request", "CI", ".github/workflows/ci.yml", "failure")},
			want: []row{{Name: "build", Status: "completed", Conclusion: "success"}},
		},
		{
			name: "a later run of the same workflow and trigger replaces the failure",
			runs: []*gh.WorkflowRun{
				workflowRun(6, 20, 601, "pull_request", "Gate", ".github/workflows/gate.yml", "success"),
				workflowRun(5, 20, 500, "pull_request", "Gate", ".github/workflows/gate.yml", "startup_failure"),
			},
			want: []row{{Name: "build", Status: "completed", Conclusion: "success"}},
		},
		{
			name: "repeated failures of one workflow list one entry",
			runs: []*gh.WorkflowRun{
				workflowRun(7, 20, 700, "pull_request_target", "Gate", ".github/workflows/gate.yml", "startup_failure"),
				workflowRun(8, 20, 800, "pull_request_target", "Gate", ".github/workflows/gate.yml", "startup_failure"),
			},
			want: []row{
				{Name: "build", Status: "completed", Conclusion: "success"},
				{Name: "Gate", Status: "completed", Conclusion: "startup_failure", Origin: "workflow_run"},
			},
		},
		{
			name: "cancelled run is not a failure",
			runs: []*gh.WorkflowRun{workflowRun(9, 20, 900, "pull_request", "Gate", ".github/workflows/gate.yml", "cancelled")},
			want: []row{{Name: "build", Status: "completed", Conclusion: "success"}},
		},
		{
			name:     "required workflow with no run",
			required: RequiredChecks{WorkflowPaths: []string{".github/workflows/gate.yml"}},
			want: []row{
				{Name: ".github/workflows/gate.yml", Status: "expected", Origin: "required_rule", Required: true},
				{Name: "build", Status: "completed", Conclusion: "success"},
			},
		},
		{
			name:     "required workflow that failed at startup is required",
			required: RequiredChecks{WorkflowPaths: []string{".github/workflows/gate.yml"}},
			runs:     []*gh.WorkflowRun{workflowRun(10, 20, 1000, "pull_request", "Gate", ".github/workflows/gate.yml", "startup_failure")},
			want: []row{
				{Name: "build", Status: "completed", Conclusion: "success"},
				{Name: "Gate", Status: "completed", Conclusion: "startup_failure", Origin: "workflow_run", Required: true},
			},
		},
		{
			name:     "required workflow that ran adds nothing",
			required: RequiredChecks{WorkflowPaths: []string{".github/workflows/ci.yml"}},
			runs:     []*gh.WorkflowRun{workflowRun(11, 10, 100, "pull_request", "CI", ".github/workflows/ci.yml", "success")},
			want:     []row{{Name: "build", Status: "completed", Conclusion: "success"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WithBlockingChecks(passing(), buildRun, tt.required, tt.runs)
			rows := make([]row, 0, len(got))
			for _, check := range got {
				rows = append(rows, row{check.Name, check.Status, check.Conclusion, check.Origin, check.Required})
			}
			assert.Equal(t, tt.want, rows)
		})
	}
}

func TestBlockingEntriesDecideTheOverallStatus(t *testing.T) {
	buildRuns := []*gh.CheckRun{{Name: new("build"), CheckSuite: &gh.CheckSuite{ID: new(int64(100))}}}
	green := func() []platform.CICheck {
		return []platform.CICheck{{Name: "build", Status: "completed", Conclusion: "success"}}
	}

	missing := WithBlockingChecks(green(), buildRuns, RequiredChecks{Contexts: []string{"deploy-gate"}}, nil)
	assert.Equal(t, "pending", DeriveOverallCIStatus(missing, nil))

	failed := WithBlockingChecks(green(), buildRuns, RequiredChecks{}, []*gh.WorkflowRun{{
		ID: new(int64(1)), CheckSuiteID: new(int64(200)), Name: new("Gate"),
		Status: new("completed"), Conclusion: new("startup_failure"),
	}})
	assert.Equal(t, "failure", DeriveOverallCIStatus(failed, nil))
}

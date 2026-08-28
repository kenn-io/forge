package github

import (
	"context"
	"errors"
	"testing"
	"time"

	gh "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/platform"
)

type workflowProviderFake struct {
	Client
	workflows []*gh.Workflow
	definitions map[string]string
	environments []*gh.Environment
	runs platform.Page[*gh.WorkflowRun]
	jobs []*gh.WorkflowJob
	dispatch *gh.WorkflowDispatchRunDetails
	definitionRefs []string
	calls []string
}

func (f *workflowProviderFake) GetRepository(context.Context, string, string) (*gh.Repository, error) {
	return &gh.Repository{Name: gh.Ptr("widgets"), DefaultBranch: gh.Ptr("trunk")}, nil
}
func (f *workflowProviderFake) ListRepositoryWorkflows(context.Context, string, string) ([]*gh.Workflow, error) {
	f.calls = append(f.calls, "workflows")
	return f.workflows, nil
}
func (f *workflowProviderFake) GetWorkflowDefinition(_ context.Context, _, _, path, ref string) (string, string, error) {
	f.calls = append(f.calls, "definition:"+path+"@"+ref)
	f.definitionRefs = append(f.definitionRefs, ref)
	content, ok := f.definitions[path]
	if !ok { return "", "", errors.New("definition unavailable") }
	return content, "sha-" + path, nil
}
func (f *workflowProviderFake) ListRepositoryEnvironments(context.Context, string, string) ([]*gh.Environment, error) {
	f.calls = append(f.calls, "environments")
	return f.environments, nil
}
func (f *workflowProviderFake) ListManualWorkflowRuns(context.Context, string, string, int64, platform.WorkflowRunQuery) (platform.Page[*gh.WorkflowRun], error) {
	f.calls = append(f.calls, "runs")
	return f.runs, nil
}
func (f *workflowProviderFake) ListManualWorkflowJobs(context.Context, string, string, int64) ([]*gh.WorkflowJob, error) {
	f.calls = append(f.calls, "jobs")
	return f.jobs, nil
}
func (f *workflowProviderFake) DispatchManualWorkflow(context.Context, string, string, int64, gh.CreateWorkflowDispatchEventRequest) (*gh.WorkflowDispatchRunDetails, error) {
	f.calls = append(f.calls, "dispatch")
	return f.dispatch, nil
}

func TestGitHubWorkflowProviderCatalogPreservesPartialAvailability(t *testing.T) {
	fake := &workflowProviderFake{
		workflows: []*gh.Workflow{
			{ID: gh.Ptr(int64(1)), Name: gh.Ptr("Release"), Path: gh.Ptr(".github/workflows/release.yml"), State: gh.Ptr("active"), HTMLURL: gh.Ptr("https://example.test/release")},
			{ID: gh.Ptr(int64(2)), Name: gh.Ptr("Broken"), Path: gh.Ptr(".github/workflows/broken.yml"), State: gh.Ptr("active")},
			{ID: gh.Ptr(int64(3)), Name: gh.Ptr("CI"), Path: gh.Ptr(".github/workflows/ci.yml"), State: gh.Ptr("active")},
			{ID: gh.Ptr(int64(4)), Name: gh.Ptr("Disabled"), Path: gh.Ptr("disabled.yml"), State: gh.Ptr("disabled_manually")},
		},
		definitions: map[string]string{
			".github/workflows/release.yml": "on:\n  workflow_dispatch:\n    inputs:\n      target:\n        type: environment\n",
			".github/workflows/ci.yml": "on: [push]\n",
		},
		environments: []*gh.Environment{{Name: gh.Ptr("production")}},
	}
	provider := &gitHubClientProvider{host: "github.com", client: fake}
	caps := provider.Capabilities()
	assert.True(t, caps.ReadWorkflows)
	assert.True(t, caps.ReadWorkflowRuns)
	assert.True(t, caps.WorkflowDispatch)

	catalog, err := provider.ListManualWorkflows(t.Context(), platform.RepoRef{Owner: "acme", Name: "widgets"})
	require.NoError(t, err)
	require.Len(t, catalog, 2)
	assert.Equal(t, "1", catalog[0].ID)
	assert.True(t, catalog[0].Available)
	assert.Equal(t, "2", catalog[1].ID)
	assert.False(t, catalog[1].Available)
	assert.NotEmpty(t, catalog[1].UnavailableReason)
	assert.Equal(t, []string{"trunk", "trunk", "trunk"}, fake.definitionRefs)

	environments, err := provider.ListWorkflowEnvironments(t.Context(), platform.RepoRef{Owner: "acme", Name: "widgets"})
	require.NoError(t, err)
	assert.Equal(t, []platform.WorkflowEnvironment{{Name: "production"}}, environments)
}

func TestGitHubWorkflowProviderNormalizesRunsJobsAndDispatch(t *testing.T) {
	created := time.Date(2026, 8, 27, 12, 0, 0, 0, time.FixedZone("offset", 3600))
	updated := created.Add(time.Minute)
	started := created.Add(time.Second)
	completed := created.Add(2 * time.Second)
	fake := &workflowProviderFake{
		runs: platform.Page[*gh.WorkflowRun]{NextCursor: "3", Items: []*gh.WorkflowRun{{
			ID: gh.Ptr(int64(100)), WorkflowID: gh.Ptr(int64(42)), RunNumber: gh.Ptr(7), Name: gh.Ptr("Release"),
			Event: gh.Ptr("workflow_dispatch"), HeadBranch: gh.Ptr("main"), HeadSHA: gh.Ptr("abc"), Actor: &gh.User{Login: gh.Ptr("octocat")},
			Status: gh.Ptr("completed"), Conclusion: gh.Ptr("success"), CreatedAt: &gh.Timestamp{Time: created}, UpdatedAt: &gh.Timestamp{Time: updated}, HTMLURL: gh.Ptr("https://example.test/runs/100"),
		}}},
		jobs: []*gh.WorkflowJob{{ID: gh.Ptr(int64(9)), Name: gh.Ptr("deploy"), Status: gh.Ptr("completed"), Conclusion: gh.Ptr("success"), StartedAt: &gh.Timestamp{Time: started}, CompletedAt: &gh.Timestamp{Time: completed}, HTMLURL: gh.Ptr("https://example.test/jobs/9"), Steps: []*gh.TaskStep{{Number: gh.Ptr(int64(1)), Name: gh.Ptr("ship"), Status: gh.Ptr("completed"), Conclusion: gh.Ptr("success"), StartedAt: &gh.Timestamp{Time: started}, CompletedAt: &gh.Timestamp{Time: completed}}}}},
		dispatch: &gh.WorkflowDispatchRunDetails{WorkflowRunID: gh.Ptr(int64(101)), HTMLURL: gh.Ptr("https://example.test/runs/101")},
	}
	provider := &gitHubClientProvider{host: "github.com", client: fake}
	page, err := provider.ListWorkflowRuns(t.Context(), platform.RepoRef{Owner: "acme", Name: "widgets"}, platform.WorkflowRunQuery{WorkflowID: "42"})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "100", page.Items[0].ID)
	assert.Equal(t, "octocat", page.Items[0].Actor)
	assert.Equal(t, created.UTC(), page.Items[0].CreatedAt)
	assert.Equal(t, "3", page.NextCursor)
	jobs, err := provider.ListWorkflowRunJobs(t.Context(), platform.RepoRef{Owner: "acme", Name: "widgets"}, "100")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "9", jobs[0].ID)
	require.Len(t, jobs[0].Steps, 1)
	assert.Equal(t, 1, jobs[0].Steps[0].Number)
	result, err := provider.DispatchWorkflow(t.Context(), platform.RepoRef{Owner: "acme", Name: "widgets"}, platform.WorkflowDispatchRequest{WorkflowID: "42", Ref: "main"})
	require.NoError(t, err)
	assert.True(t, result.Accepted)
	assert.False(t, result.LocatingRun)
	require.NotNil(t, result.Run)
	assert.Equal(t, "101", result.Run.ID)

	fake.dispatch = nil
	result, err = provider.DispatchWorkflow(t.Context(), platform.RepoRef{Owner: "acme", Name: "widgets"}, platform.WorkflowDispatchRequest{WorkflowID: "42", Ref: "main"})
	require.NoError(t, err)
	assert.True(t, result.LocatingRun)
	_, err = provider.DispatchWorkflow(t.Context(), platform.RepoRef{}, platform.WorkflowDispatchRequest{WorkflowID: "not-decimal"})
	require.ErrorIs(t, err, platform.ErrInvalidArgument)
}

func TestGitHubWorkflowProviderUnsupportedClientsAreTyped(t *testing.T) {
	provider := &gitHubClientProvider{host: "github.com", client: &mockClient{}}
	assert.False(t, provider.Capabilities().ReadWorkflows)
	_, err := provider.ListManualWorkflows(t.Context(), platform.RepoRef{})
	require.ErrorIs(t, err, platform.ErrUnsupportedCapability)
	_, err = provider.ListWorkflowRuns(t.Context(), platform.RepoRef{}, platform.WorkflowRunQuery{})
	require.ErrorIs(t, err, platform.ErrUnsupportedCapability)
	_, err = provider.DispatchWorkflow(t.Context(), platform.RepoRef{}, platform.WorkflowDispatchRequest{})
	require.ErrorIs(t, err, platform.ErrUnsupportedCapability)
}

func TestRoutedClientRoutesWorkflowOperationsByRepository(t *testing.T) {
	fallback := &workflowProviderFake{}
	exact := &workflowProviderFake{
		definitions: map[string]string{"release.yml": "on: workflow_dispatch"},
		dispatch: &gh.WorkflowDispatchRunDetails{WorkflowRunID: gh.Ptr(int64(8))},
	}
	router, err := NewHostRouter(
		"github.com",
		&Route{Key: RouteKey{Host: "github.com"}, Client: fallback},
		&Route{Key: RouteKey{Host: "github.com", Owner: "acme", Name: "widgets"}, Client: exact},
	)
	require.NoError(t, err)
	routed, err := NewRoutedClient(router)
	require.NoError(t, err)
	_, err = routed.ListRepositoryWorkflows(t.Context(), "acme", "widgets")
	require.NoError(t, err)
	_, _, err = routed.GetWorkflowDefinition(t.Context(), "acme", "widgets", "release.yml", "main")
	require.NoError(t, err)
	_, err = routed.ListRepositoryEnvironments(t.Context(), "acme", "widgets")
	require.NoError(t, err)
	_, err = routed.ListManualWorkflowRuns(t.Context(), "acme", "widgets", 42, platform.WorkflowRunQuery{})
	require.NoError(t, err)
	_, err = routed.ListManualWorkflowJobs(t.Context(), "acme", "widgets", 99)
	require.NoError(t, err)
	_, err = routed.DispatchManualWorkflow(t.Context(), "acme", "widgets", 42, gh.CreateWorkflowDispatchEventRequest{Ref: "main"})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"workflows", "definition:release.yml@main", "environments", "runs", "jobs", "dispatch",
	}, exact.calls)
	assert.Empty(t, fallback.calls)
}

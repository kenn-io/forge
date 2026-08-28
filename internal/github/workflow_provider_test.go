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

type workflowCatalogOnlyFake struct{ Client }

func (*workflowCatalogOnlyFake) ListRepositoryWorkflows(context.Context, string, string) ([]*gh.Workflow, error) {
	return nil, nil
}
func (*workflowCatalogOnlyFake) GetWorkflowDefinition(context.Context, string, string, string, string) (string, string, error) {
	return "", "", nil
}
func (*workflowCatalogOnlyFake) ListRepositoryEnvironments(context.Context, string, string) ([]*gh.Environment, error) {
	return nil, nil
}

type workflowRunOnlyFake struct{ Client }

func (*workflowRunOnlyFake) ListManualWorkflowRuns(context.Context, string, string, int64, platform.WorkflowRunQuery) (platform.Page[*gh.WorkflowRun], error) {
	return platform.Page[*gh.WorkflowRun]{}, nil
}
func (*workflowRunOnlyFake) ListManualWorkflowJobs(context.Context, string, string, int64) ([]*gh.WorkflowJob, error) {
	return nil, nil
}

type workflowDispatchOnlyFake struct{ Client }

func (*workflowDispatchOnlyFake) DispatchManualWorkflow(context.Context, string, string, int64, gh.CreateWorkflowDispatchEventRequest) (*gh.WorkflowDispatchRunDetails, error) {
	return nil, nil
}

func TestGitHubWorkflowCapabilitiesAreIndependent(t *testing.T) {
	tests := []struct {
		name string
		client Client
		readCatalog bool
		readRuns bool
		dispatch bool
	}{
		{name: "catalog", client: &workflowCatalogOnlyFake{}, readCatalog: true},
		{name: "runs", client: &workflowRunOnlyFake{}, readRuns: true},
		{name: "dispatch", client: &workflowDispatchOnlyFake{}, dispatch: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &gitHubClientProvider{host: "github.com", client: test.client}
			caps := provider.Capabilities()
			assert.Equal(t, test.readCatalog, caps.ReadWorkflows)
			assert.Equal(t, test.readRuns, caps.ReadWorkflowRuns)
			assert.Equal(t, test.dispatch, caps.WorkflowDispatch)
			if !test.readCatalog {
				_, err := provider.ListManualWorkflows(t.Context(), platform.RepoRef{})
				require.ErrorIs(t, err, platform.ErrUnsupportedCapability)
				_, err = provider.ListWorkflowEnvironments(t.Context(), platform.RepoRef{})
				require.ErrorIs(t, err, platform.ErrUnsupportedCapability)
			}
			if !test.readRuns {
				_, err := provider.ListWorkflowRuns(t.Context(), platform.RepoRef{}, platform.WorkflowRunQuery{})
				require.ErrorIs(t, err, platform.ErrUnsupportedCapability)
				_, err = provider.ListWorkflowRunJobs(t.Context(), platform.RepoRef{}, "1")
				require.ErrorIs(t, err, platform.ErrUnsupportedCapability)
			}
			if !test.dispatch {
				_, err := provider.DispatchWorkflow(t.Context(), platform.RepoRef{}, platform.WorkflowDispatchRequest{})
				require.ErrorIs(t, err, platform.ErrUnsupportedCapability)
			}
		})
	}
}

func TestGitHubWorkflowProviderCatalogPreservesPartialAvailability(t *testing.T) {
	fake := &workflowProviderFake{
		workflows: []*gh.Workflow{
			{ID: gh.Ptr(int64(1)), Name: gh.Ptr("Release"), Path: gh.Ptr(".github/workflows/release.yml"), State: gh.Ptr("active"), HTMLURL: gh.Ptr("https://example.test/release")},
			{ID: gh.Ptr(int64(2)), Name: gh.Ptr("Broken"), Path: gh.Ptr(".github/workflows/broken.yml"), State: gh.Ptr("active")},
			{ID: gh.Ptr(int64(3)), Name: gh.Ptr("CI"), Path: gh.Ptr(".github/workflows/ci.yml"), State: gh.Ptr("active")},
			{ID: gh.Ptr(int64(5)), Name: gh.Ptr("Malformed"), Path: gh.Ptr(".github/workflows/malformed.yml"), State: gh.Ptr("active")},
			{ID: gh.Ptr(int64(4)), Name: gh.Ptr("Disabled"), Path: gh.Ptr("disabled.yml"), State: gh.Ptr("disabled_manually")},
		},
		definitions: map[string]string{
			".github/workflows/release.yml":   "on:\n  workflow_dispatch:\n    inputs:\n      target:\n        type: environment\n",
			".github/workflows/ci.yml":        "on: [push]\n",
			".github/workflows/malformed.yml": "on: workflow_dispatch\n---\non: workflow_dispatch\n",
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
	require.Len(t, catalog, 3)
	assert.Equal(t, "1", catalog[0].ID)
	assert.True(t, catalog[0].Available)
	assert.Equal(t, "2", catalog[1].ID)
	assert.False(t, catalog[1].Available)
	assert.NotEmpty(t, catalog[1].UnavailableReason)
	assert.Equal(t, "5", catalog[2].ID)
	assert.False(t, catalog[2].Available)
	assert.NotEmpty(t, catalog[2].UnavailableReason)
	assert.Equal(t, []string{"trunk", "trunk", "trunk", "trunk"}, fake.definitionRefs)

	environments, err := provider.ListWorkflowEnvironments(t.Context(), platform.RepoRef{Owner: "acme", Name: "widgets"})
	require.NoError(t, err)
	assert.Equal(t, []platform.WorkflowEnvironment{{Name: "production"}}, environments)
}

func TestGitHubWorkflowEnvironmentsSkipTransportWithoutEnvironmentInput(t *testing.T) {
	fake := &workflowProviderFake{
		workflows: []*gh.Workflow{{
			ID: gh.Ptr(int64(1)), Name: gh.Ptr("Release"),
			Path: gh.Ptr(".github/workflows/release.yml"), State: gh.Ptr("active"),
		}},
		definitions: map[string]string{
			".github/workflows/release.yml": "on:\n  workflow_dispatch:\n    inputs:\n      version:\n        type: string\n",
		},
	}
	provider := &gitHubClientProvider{host: "github.com", client: fake}
	environments, err := provider.ListWorkflowEnvironments(
		t.Context(), platform.RepoRef{Owner: "acme", Name: "widgets"},
	)
	require.NoError(t, err)
	assert.Empty(t, environments)
	assert.NotContains(t, fake.calls, "environments")
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
	assert.Equal(t, platform.Page[platform.WorkflowRun]{
		NextCursor: "3",
		Items: []platform.WorkflowRun{{
			ID: "100", WorkflowID: "42", RunNumber: 7, Name: "Release",
			Event: "workflow_dispatch", Ref: "main", HeadSHA: "abc", Actor: "octocat",
			Status: "completed", Conclusion: "success", CreatedAt: created.UTC(),
			UpdatedAt: updated.UTC(), WebURL: "https://example.test/runs/100",
		}},
	}, page)
	jobs, err := provider.ListWorkflowRunJobs(t.Context(), platform.RepoRef{Owner: "acme", Name: "widgets"}, "100")
	require.NoError(t, err)
	assert.Equal(t, []platform.WorkflowRunJob{{
		ID: "9", Name: "deploy", Status: "completed", Conclusion: "success",
		StartedAt: started.UTC(), CompletedAt: completed.UTC(),
		WebURL: "https://example.test/jobs/9",
		Steps: []platform.WorkflowRunStep{{
			Number: 1, Name: "ship", Status: "completed", Conclusion: "success",
			StartedAt: started.UTC(), CompletedAt: completed.UTC(),
		}},
	}}, jobs)
	result, err := provider.DispatchWorkflow(t.Context(), platform.RepoRef{Owner: "acme", Name: "widgets"}, platform.WorkflowDispatchRequest{WorkflowID: "42", Ref: "main"})
	require.NoError(t, err)
	assert.Equal(t, platform.WorkflowDispatchResult{
		Accepted: true,
		Run: &platform.WorkflowRun{
			ID: "101", WorkflowID: "42", WebURL: "https://example.test/runs/101",
		},
	}, result)
	fake.dispatch = nil
	result, err = provider.DispatchWorkflow(t.Context(), platform.RepoRef{Owner: "acme", Name: "widgets"}, platform.WorkflowDispatchRequest{WorkflowID: "42", Ref: "main"})
	require.NoError(t, err)
	assert.Equal(t, platform.WorkflowDispatchResult{Accepted: true, LocatingRun: true}, result)
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

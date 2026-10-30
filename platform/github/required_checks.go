package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/platform"
)

// RequiredChecks lists what a branch's rules require before a merge.
type RequiredChecks struct {
	// Contexts are check run names or commit status contexts.
	Contexts []string
	// WorkflowPaths are workflow files that must run on the pull request.
	WorkflowPaths []string
}

// GetRequiredChecks reads both places GitHub can require checks: active
// rulesets at any level, and classic branch protection. A credential that may
// not read a source leaves its requirements unknown, and the provider's merge
// state still reports the block.
func (c *Client) GetRequiredChecks(ctx context.Context, owner, repo, branch string) (RequiredChecks, error) {
	var required RequiredChecks
	pages, err := collectPages(ctx, func(pageOpts *gh.ListOptions) ([]*gh.BranchRules, *gh.Response, error) {
		page, resp, err := c.gh.Repositories.ListRulesForBranch(ctx, owner, repo, branch, pageOpts)
		if err != nil {
			return nil, resp, fmt.Errorf("listing rules for %s/%s branch %s: %w", owner, repo, branch, err)
		}
		return []*gh.BranchRules{page}, resp, nil
	}, c.trackRate)
	if err != nil && !IsAccessRefusal(err) {
		return RequiredChecks{}, err
	}
	for _, page := range pages {
		for _, rule := range page.RequiredStatusChecks {
			for _, check := range rule.Parameters.RequiredStatusChecks {
				required.Contexts = append(required.Contexts, check.Context)
			}
		}
		for _, rule := range page.Workflows {
			for _, workflow := range rule.Parameters.Workflows {
				required.WorkflowPaths = append(required.WorkflowPaths, workflow.Path)
			}
		}
	}

	protection, err := c.getBranchRequiredStatusChecks(ctx, owner, repo, branch)
	if err != nil && !IsAccessRefusal(err) {
		return RequiredChecks{}, err
	}
	if protection != nil {
		if protection.Contexts != nil {
			required.Contexts = append(required.Contexts, *protection.Contexts...)
		}
		if protection.Checks != nil {
			for _, check := range *protection.Checks {
				required.Contexts = append(required.Contexts, check.Context)
			}
		}
	}

	slices.Sort(required.Contexts)
	required.Contexts = slices.Compact(required.Contexts)
	slices.Sort(required.WorkflowPaths)
	required.WorkflowPaths = slices.Compact(required.WorkflowPaths)
	return required, nil
}

// getBranchRequiredStatusChecks reads the protection summary from the branch
// endpoint, which needs only read access; the protection endpoint needs admin.
// It goes through Do because go-github's GetBranch skips error classification.
func (c *Client) getBranchRequiredStatusChecks(
	ctx context.Context, owner, repo, branch string,
) (*gh.RequiredStatusChecks, error) {
	req, err := c.gh.NewRequest(
		ctx, http.MethodGet,
		fmt.Sprintf("repos/%s/%s/branches/%s", owner, repo, url.PathEscape(branch)),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("building branch request for %s/%s branch %s: %w", owner, repo, branch, err)
	}
	var result gh.Branch
	resp, err := c.gh.Do(req, &result)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf("getting %s/%s branch %s: %w", owner, repo, branch, err)
	}
	if result.Protection == nil {
		return nil, nil
	}
	return result.Protection.RequiredStatusChecks, nil
}

// IsAccessRefusal reports whether GitHub refused a read for this credential,
// as opposed to failing it, so the caller can treat the data as unknown.
func IsAccessRefusal(err error) bool {
	response, ok := errors.AsType[*gh.ErrorResponse](err)
	return ok && response.Response != nil &&
		(response.Response.StatusCode == http.StatusForbidden || response.Response.StatusCode == http.StatusNotFound)
}

// WithBlockingChecks adds the entries a commit's check runs and statuses
// cannot show: required checks that never reported, and failed workflow runs
// that created no check run. It also marks reported checks that are required.
//
// checkRuns are the raw runs behind checks, which link a workflow run to its
// check suite. Matching a required context by name alone is enough here: Forge
// lists the requirement, and GitHub still enforces which app must report it.
func WithBlockingChecks(
	checks []platform.CICheck,
	checkRuns []*gh.CheckRun,
	required RequiredChecks,
	workflowRuns []*gh.WorkflowRun,
) []platform.CICheck {
	reported := make(map[string]bool, len(checks))
	for i := range checks {
		reported[checks[i].Name] = true
		checks[i].Required = slices.Contains(required.Contexts, checks[i].Name)
	}
	for _, context := range required.Contexts {
		if !reported[context] {
			checks = append(checks, platform.CICheck{
				Name:     context,
				Status:   platform.CICheckStatusExpected,
				Required: true,
				Origin:   platform.CICheckOriginRequiredRule,
			})
		}
	}

	suitesWithChecks := make(map[int64]bool, len(checkRuns))
	for _, run := range checkRuns {
		suitesWithChecks[run.GetCheckSuite().GetID()] = true
	}
	ran := make(map[string]bool, len(workflowRuns))
	for _, run := range latestWorkflowRuns(workflowRuns) {
		ran[run.GetPath()] = true
		runRequired := slices.Contains(required.WorkflowPaths, run.GetPath())
		blocking := workflowRunFailed(run) || (runRequired && workflowRunUnresolved(run))
		if run.GetCheckSuiteID() == 0 || suitesWithChecks[run.GetCheckSuiteID()] || !blocking {
			continue
		}
		name := run.GetName()
		if name == "" {
			name = run.GetPath()
		}
		checks = append(checks, platform.CICheck{
			PlatformID: run.GetID(),
			Name:       name,
			Status:     run.GetStatus(),
			Conclusion: run.GetConclusion(),
			URL:        run.GetHTMLURL(),
			App:        "GitHub Actions",
			Required:   runRequired,
			Origin:     platform.CICheckOriginWorkflowRun,
		})
	}
	for _, path := range required.WorkflowPaths {
		if !ran[path] {
			checks = append(checks, platform.CICheck{
				Name:     path,
				Status:   platform.CICheckStatusExpected,
				Required: true,
				Origin:   platform.CICheckOriginRequiredRule,
			})
		}
	}

	sortCIChecksByName(checks)
	return checks
}

// latestWorkflowRuns keeps the newest run of each workflow and trigger, so a
// failure that a later run replaced no longer counts. Run IDs grow over time.
func latestWorkflowRuns(runs []*gh.WorkflowRun) []*gh.WorkflowRun {
	type trigger struct {
		workflowID int64
		event      string
	}
	latest := make(map[trigger]*gh.WorkflowRun, len(runs))
	order := make([]trigger, 0, len(runs))
	for _, run := range runs {
		key := trigger{workflowID: run.GetWorkflowID(), event: run.GetEvent()}
		current, ok := latest[key]
		if !ok {
			order = append(order, key)
		}
		if !ok || run.GetID() > current.GetID() {
			latest[key] = run
		}
	}
	out := make([]*gh.WorkflowRun, 0, len(order))
	for _, key := range order {
		out = append(out, latest[key])
	}
	return out
}

// workflowRunFailed leaves out cancelled runs: GitHub cancels queued runs for
// concurrency groups before they create jobs, which is not a failure.
func workflowRunFailed(run *gh.WorkflowRun) bool {
	switch run.GetConclusion() {
	case "failure", "startup_failure", "timed_out":
		return true
	}
	return false
}

// workflowRunUnresolved reports that a run has not satisfied a rule requiring
// its workflow: it is still queued or running, or it ended without passing.
func workflowRunUnresolved(run *gh.WorkflowRun) bool {
	if run.GetStatus() != "completed" {
		return true
	}
	switch run.GetConclusion() {
	case "success", "neutral", "skipped":
		return false
	}
	return true
}

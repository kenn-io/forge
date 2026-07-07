package github

import gh "github.com/google/go-github/v88/github"

type ghWorkflowRunOpt func(*gh.WorkflowRun)

func ghWorkflowRun(id int64, headSHA string, opts ...ghWorkflowRunOpt) *gh.WorkflowRun {
	run := &gh.WorkflowRun{
		ID:      new(id),
		HeadSHA: new(headSHA),
		Event:   new("pull_request"),
	}
	for _, opt := range opts {
		opt(run)
	}
	return run
}

func withWorkflowPRNumbers(numbers ...int) ghWorkflowRunOpt {
	return func(run *gh.WorkflowRun) {
		run.PullRequests = make([]*gh.PullRequest, 0, len(numbers))
		for _, number := range numbers {
			run.PullRequests = append(run.PullRequests, &gh.PullRequest{Number: new(number)})
		}
	}
}

func withWorkflowHeadRepo(fullName string) ghWorkflowRunOpt {
	return func(run *gh.WorkflowRun) { run.HeadRepository = &gh.Repository{FullName: new(fullName)} }
}

func withWorkflowHeadBranch(branch string) ghWorkflowRunOpt {
	return func(run *gh.WorkflowRun) { run.HeadBranch = new(branch) }
}

package issueapi

import "context"

func (s *Handler) createIssueOnHost(ctx context.Context, input *createIssueHostInput) (*createIssueOutput, error) {
	next := createIssueInput{Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Body: input.Body}
	return s.createIssue(ctx, &next)
}

func (s *Handler) getIssueOnHost(ctx context.Context, input *issueRepoNumberHostInput) (*getIssueOutput, error) {
	return s.getIssue(ctx, &issueRepoNumberInput{
		Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number,
	})
}

func (s *Handler) postIssueCommentOnHost(ctx context.Context, input *postIssueCommentHostInput) (*postIssueCommentOutput, error) {
	next := postIssueCommentInput{Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number, Body: input.Body}
	return s.postIssueComment(ctx, &next)
}

func (s *Handler) editIssueContentOnHost(ctx context.Context, input *editIssueContentHostInput) (*editIssueContentOutput, error) {
	next := editIssueContentInput{Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number, Body: input.Body}
	return s.editIssueContent(ctx, &next)
}

func (s *Handler) editIssueCommentOnHost(ctx context.Context, input *editIssueCommentHostInput) (*editIssueCommentOutput, error) {
	next := editIssueCommentInput{
		Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner,
		Name: input.Name, Number: input.Number, Body: input.Body, CommentID: input.CommentID,
	}
	return s.editIssueComment(ctx, &next)
}

func (s *Handler) deleteIssueCommentOnHost(ctx context.Context, input *deleteIssueCommentHostInput) (*deleteIssueCommentOutput, error) {
	return s.deleteIssueComment(ctx, &deleteIssueCommentInput{
		Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner,
		Name: input.Name, Number: input.Number, CommentID: input.CommentID,
	})
}

func (s *Handler) setIssueLabelsOnHost(ctx context.Context, input *setIssueLabelsHostInput) (*setLabelsOutput, error) {
	next := setIssueLabelsInput{Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number, Body: input.Body}
	return s.setIssueLabels(ctx, &next)
}

func (s *Handler) setIssueAssigneesOnHost(ctx context.Context, input *setIssueAssigneesHostInput) (*setAssigneesOutput, error) {
	next := setIssueAssigneesInput{Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number, Body: input.Body}
	return s.setIssueAssignees(ctx, &next)
}

func (s *Handler) setIssueGitHubStateOnHost(ctx context.Context, input *githubStateHostInput) (*githubStateOutput, error) {
	next := githubStateInput{Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number, Body: input.Body}
	return s.setIssueGitHubState(ctx, &next)
}

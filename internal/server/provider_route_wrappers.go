package server

import (
	"context"
)

type repoNumberHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
}

type resolveItemHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	ItemType     string `query:"item_type" enum:"pr,issue" doc:"Optional item type hint for providers whose issues and merge requests have separate number spaces."`
}

type setKanbanStateHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Status string `json:"status"`
	}
}

type postCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body string `json:"body"`
	}
}

type editCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	CommentID    int64  `path:"comment_id"`
	Body         struct {
		Body string `json:"body"`
	}
}

type replyToDiscussionHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	DiscussionID string `path:"discussion_id"`
	Body         struct {
		Body string `json:"body"`
	}
}

type resolveDiscussionHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	DiscussionID string `path:"discussion_id"`
	Body         struct {
		Resolved bool `json:"resolved"`
	}
}

type createDiffReviewDraftCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body  string              `json:"body"`
		Range diffReviewLineRange `json:"range"`
	}
}

type editDiffReviewDraftCommentHostInput struct {
	Provider       string `path:"provider"`
	PlatformHost   string `path:"platform_host"`
	Owner          string `path:"owner"`
	Name           string `path:"name"`
	Number         int    `path:"number"`
	DraftCommentID string `path:"draft_comment_id"`
	Body           struct {
		Body  string              `json:"body"`
		Range diffReviewLineRange `json:"range"`
	}
}

type deleteDiffReviewDraftCommentHostInput struct {
	Provider       string `path:"provider"`
	PlatformHost   string `path:"platform_host"`
	Owner          string `path:"owner"`
	Name           string `path:"name"`
	Number         int    `path:"number"`
	DraftCommentID string `path:"draft_comment_id"`
}

type publishDiffReviewDraftHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body   string `json:"body,omitempty"`
		Action string `json:"action"`
	}
}

type resolveDiffReviewThreadHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	ThreadID     string `path:"thread_id"`
}
type createIssueHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
}

type postIssueCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body string `json:"body"`
	}
}

type editIssueCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	CommentID    int64  `path:"comment_id"`
	Body         struct {
		Body string `json:"body"`
	}
}

type getRepoHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
}

type setPullLabelsHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setLabelsRequest
}

type setIssueLabelsHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setLabelsRequest
}

type commentAutocompleteHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Trigger      string `query:"trigger"`
	Q            string `query:"q"`
	Limit        int    `query:"limit"`
}

type approvePRHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body string `json:"body"`
	}
}

type mergePRHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		CommitTitle   string `json:"commit_title"`
		CommitMessage string `json:"commit_message"`
		Method        string `json:"method"`
	}
}

type editPRContentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Title *string `json:"title,omitempty"`
		Body  *string `json:"body,omitempty"`
	}
}

type editIssueContentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Title *string `json:"title,omitempty"`
		Body  *string `json:"body,omitempty"`
	}
}

type githubStateHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		State string `json:"state"`
	}
}

type getDiffHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Whitespace   string `query:"whitespace"`
	Commit       string `query:"commit" doc:"Scope to a single commit SHA"`
	From         string `query:"from"   doc:"Start SHA for range diff (inclusive)"`
	To           string `query:"to"     doc:"End SHA for range diff (inclusive)"`
}

type getFilePreviewHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Path         string `query:"path" doc:"Changed file path to preview"`
	Side         string `query:"side" enum:"old,new" doc:"Optional diff side to read for context expansion"`
	Commit       string `query:"commit" doc:"Scope to a single commit SHA"`
	From         string `query:"from"   doc:"Start SHA for range diff (inclusive)"`
	To           string `query:"to"     doc:"End SHA for range diff (inclusive)"`
}

type createIssueWorkspaceHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		GitHeadRef          *string `json:"git_head_ref,omitempty"`
		ReuseExistingBranch bool    `json:"reuse_existing_branch,omitempty"`
	}
}

func repoNumberFromHost(input *repoNumberHostInput) repoNumberInput {
	return repoNumberInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
	}
}

func (s *Server) getPullOnHost(ctx context.Context, input *repoNumberHostInput) (*getPullOutput, error) {
	next := repoNumberFromHost(input)
	return s.getPull(ctx, &next)
}

func (s *Server) getMRImportMetadataOnHost(ctx context.Context, input *repoNumberHostInput) (*getMRImportMetadataOutput, error) {
	next := repoNumberFromHost(input)
	return s.getMRImportMetadata(ctx, &next)
}

func (s *Server) setKanbanStateOnHost(ctx context.Context, input *setKanbanStateHostInput) (*statusOnlyOutput, error) {
	next := setKanbanStateInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.setKanbanState(ctx, &next)
}

func (s *Server) editPRContentOnHost(ctx context.Context, input *editPRContentHostInput) (*editPRContentOutput, error) {
	next := editPRContentInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.editPRContent(ctx, &next)
}

func (s *Server) editIssueContentOnHost(ctx context.Context, input *editIssueContentHostInput) (*editIssueContentOutput, error) {
	next := editIssueContentInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.editIssueContent(ctx, &next)
}

func (s *Server) postCommentOnHost(ctx context.Context, input *postCommentHostInput) (*postCommentOutput, error) {
	next := postCommentInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.postComment(ctx, &next)
}

func (s *Server) editCommentOnHost(ctx context.Context, input *editCommentHostInput) (*editCommentOutput, error) {
	next := editCommentInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		CommentID:    input.CommentID,
		Body:         input.Body,
	}
	return s.editComment(ctx, &next)
}

func (s *Server) replyToDiscussionOnHost(ctx context.Context, input *replyToDiscussionHostInput) (*replyToDiscussionOutput, error) {
	next := replyToDiscussionInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		DiscussionID: input.DiscussionID,
		Body:         input.Body,
	}
	return s.replyToDiscussion(ctx, &next)
}

func (s *Server) resolveDiscussionOnHost(ctx context.Context, input *resolveDiscussionHostInput) (*resolveDiscussionOutput, error) {
	next := resolveDiscussionInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		DiscussionID: input.DiscussionID,
		Body:         input.Body,
	}
	return s.resolveDiscussion(ctx, &next)
}

func (s *Server) setPullLabelsOnHost(ctx context.Context, input *setPullLabelsHostInput) (*setLabelsOutput, error) {
	next := setPullLabelsInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.setPullLabels(ctx, &next)
}

func (s *Server) getDiffReviewDraftOnHost(
	ctx context.Context,
	input *repoNumberHostInput,
) (*getDiffReviewDraftOutput, error) {
	next := repoNumberFromHost(input)
	return s.getDiffReviewDraft(ctx, &next)
}

func (s *Server) createDiffReviewDraftCommentOnHost(
	ctx context.Context,
	input *createDiffReviewDraftCommentHostInput,
) (*createDiffReviewDraftCommentOutput, error) {
	next := createDiffReviewDraftCommentInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.createDiffReviewDraftComment(ctx, &next)
}

func (s *Server) editDiffReviewDraftCommentOnHost(
	ctx context.Context,
	input *editDiffReviewDraftCommentHostInput,
) (*editDiffReviewDraftCommentOutput, error) {
	next := editDiffReviewDraftCommentInput{
		Provider:       input.Provider,
		PlatformHost:   input.PlatformHost,
		Owner:          input.Owner,
		Name:           input.Name,
		Number:         input.Number,
		DraftCommentID: input.DraftCommentID,
		Body:           input.Body,
	}
	return s.editDiffReviewDraftComment(ctx, &next)
}

func (s *Server) deleteDiffReviewDraftCommentOnHost(
	ctx context.Context,
	input *deleteDiffReviewDraftCommentHostInput,
) (*statusOnlyOutput, error) {
	next := deleteDiffReviewDraftCommentInput{
		Provider:       input.Provider,
		PlatformHost:   input.PlatformHost,
		Owner:          input.Owner,
		Name:           input.Name,
		Number:         input.Number,
		DraftCommentID: input.DraftCommentID,
	}
	return s.deleteDiffReviewDraftComment(ctx, &next)
}

func (s *Server) publishDiffReviewDraftOnHost(
	ctx context.Context,
	input *publishDiffReviewDraftHostInput,
) (*actionStatusOutput, error) {
	next := publishDiffReviewDraftInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.publishDiffReviewDraft(ctx, &next)
}

func (s *Server) discardDiffReviewDraftOnHost(
	ctx context.Context,
	input *repoNumberHostInput,
) (*statusOnlyOutput, error) {
	next := repoNumberFromHost(input)
	return s.discardDiffReviewDraft(ctx, &next)
}

func (s *Server) resolveDiffReviewThreadOnHost(
	ctx context.Context,
	input *resolveDiffReviewThreadHostInput,
) (*statusOnlyOutput, error) {
	next := resolveDiffReviewThreadInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		ThreadID:     input.ThreadID,
	}
	return s.resolveDiffReviewThread(ctx, &next)
}

func (s *Server) unresolveDiffReviewThreadOnHost(
	ctx context.Context,
	input *resolveDiffReviewThreadHostInput,
) (*statusOnlyOutput, error) {
	next := resolveDiffReviewThreadInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		ThreadID:     input.ThreadID,
	}
	return s.unresolveDiffReviewThread(ctx, &next)
}

func (s *Server) createIssueOnHost(ctx context.Context, input *createIssueHostInput) (*createIssueOutput, error) {
	next := createIssueInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Body:         input.Body,
	}
	return s.createIssue(ctx, &next)
}

func (s *Server) getIssueOnHost(ctx context.Context, input *repoNumberHostInput) (*getIssueOutput, error) {
	next := issueRepoNumberInput(repoNumberFromHost(input))
	return s.getIssue(ctx, &next)
}

func (s *Server) postIssueCommentOnHost(ctx context.Context, input *postIssueCommentHostInput) (*postIssueCommentOutput, error) {
	next := postIssueCommentInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.postIssueComment(ctx, &next)
}

func (s *Server) editIssueCommentOnHost(ctx context.Context, input *editIssueCommentHostInput) (*editIssueCommentOutput, error) {
	next := editIssueCommentInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		CommentID:    input.CommentID,
		Body:         input.Body,
	}
	return s.editIssueComment(ctx, &next)
}

func (s *Server) setIssueLabelsOnHost(ctx context.Context, input *setIssueLabelsHostInput) (*setLabelsOutput, error) {
	next := setIssueLabelsInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.setIssueLabels(ctx, &next)
}

func (s *Server) resolveItemOnHost(ctx context.Context, input *resolveItemHostInput) (*resolveItemOutput, error) {
	next := resolveItemInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		ItemType:     input.ItemType,
	}
	return s.resolveItem(ctx, &next)
}

func (s *Server) getRepoOnHost(ctx context.Context, input *getRepoHostInput) (*getRepoOutput, error) {
	next := getRepoInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}
	return s.getRepo(ctx, &next)
}

func (s *Server) listRepoLabelsOnHost(ctx context.Context, input *getRepoHostInput) (*listRepoLabelsOutput, error) {
	next := getRepoInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}
	return s.listRepoLabels(ctx, &next)
}

func (s *Server) getCommentAutocompleteOnHost(ctx context.Context, input *commentAutocompleteHostInput) (*commentAutocompleteOutput, error) {
	next := commentAutocompleteInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Trigger:      input.Trigger,
		Q:            input.Q,
		Limit:        input.Limit,
	}
	return s.getCommentAutocomplete(ctx, &next)
}

func (s *Server) approvePROnHost(ctx context.Context, input *approvePRHostInput) (*actionStatusOutput, error) {
	next := approvePRInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.approvePR(ctx, &next)
}

func (s *Server) approveWorkflowsOnHost(ctx context.Context, input *repoNumberHostInput) (*actionStatusOutput, error) {
	next := repoNumberFromHost(input)
	return s.approveWorkflows(ctx, &next)
}

func (s *Server) readyForReviewOnHost(ctx context.Context, input *repoNumberHostInput) (*actionStatusOutput, error) {
	next := repoNumberFromHost(input)
	return s.readyForReview(ctx, &next)
}

func (s *Server) mergePROnHost(ctx context.Context, input *mergePRHostInput) (*mergePROutput, error) {
	next := mergePRInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.mergePR(ctx, &next)
}

func (s *Server) syncPROnHost(ctx context.Context, input *repoNumberHostInput) (*syncPROutput, error) {
	next := repoNumberFromHost(input)
	return s.syncPR(ctx, &next)
}

func (s *Server) syncPRCIOnHost(ctx context.Context, input *repoNumberHostInput) (*syncPRCIOutput, error) {
	next := repoNumberFromHost(input)
	return s.syncPRCI(ctx, &next)
}

func (s *Server) enqueuePRSyncOnHost(ctx context.Context, input *repoNumberHostInput) (*acceptedOutput, error) {
	next := repoNumberFromHost(input)
	return s.enqueuePRSync(ctx, &next)
}

func (s *Server) syncIssueOnHost(ctx context.Context, input *repoNumberHostInput) (*syncIssueOutput, error) {
	next := issueRepoNumberInput(repoNumberFromHost(input))
	return s.syncIssue(ctx, &next)
}

func (s *Server) enqueueIssueSyncOnHost(ctx context.Context, input *repoNumberHostInput) (*acceptedOutput, error) {
	next := issueRepoNumberInput(repoNumberFromHost(input))
	return s.enqueueIssueSync(ctx, &next)
}

func (s *Server) setPRGitHubStateOnHost(ctx context.Context, input *githubStateHostInput) (*githubStateOutput, error) {
	next := githubStateInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.setPRGitHubState(ctx, &next)
}

func (s *Server) setIssueGitHubStateOnHost(ctx context.Context, input *githubStateHostInput) (*githubStateOutput, error) {
	next := githubStateInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.setIssueGitHubState(ctx, &next)
}

func (s *Server) getCommitsOnHost(ctx context.Context, input *repoNumberHostInput) (*getCommitsOutput, error) {
	next := repoNumberFromHost(input)
	return s.getCommits(ctx, &next)
}

func (s *Server) getDiffOnHost(ctx context.Context, input *getDiffHostInput) (*getDiffOutput, error) {
	next := getDiffInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Whitespace:   input.Whitespace,
		Commit:       input.Commit,
		From:         input.From,
		To:           input.To,
	}
	return s.getDiff(ctx, &next)
}

func (s *Server) getFilesOnHost(ctx context.Context, input *repoNumberHostInput) (*getFilesOutput, error) {
	next := getFilesInput(repoNumberFromHost(input))
	return s.getFiles(ctx, &next)
}

func (s *Server) getFilePreviewOnHost(ctx context.Context, input *getFilePreviewHostInput) (*getFilePreviewOutput, error) {
	next := getFilePreviewInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Path:         input.Path,
		Side:         input.Side,
		Commit:       input.Commit,
		From:         input.From,
		To:           input.To,
	}
	return s.getFilePreview(ctx, &next)
}

func (s *Server) getStackForPROnHost(ctx context.Context, input *repoNumberHostInput) (*getStackForPROutput, error) {
	next := repoNumberFromHost(input)
	return s.getStackForPR(ctx, &next)
}

func (s *Server) createIssueWorkspaceOnHost(ctx context.Context, input *createIssueWorkspaceHostInput) (*createWorkspaceOutput, error) {
	next := createIssueWorkspaceInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.createIssueWorkspace(ctx, &next)
}

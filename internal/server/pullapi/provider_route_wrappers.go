package pullapi

import (
	"context"

	"go.kenn.io/middleman/internal/server/httpapi"
)

type repoNumberHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
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

type deleteCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	CommentID    int64  `path:"comment_id"`
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

type requestChangesPRHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body            string `json:"body"`
		ExpectedHeadSHA string `json:"expected_head_sha,omitempty"`
	}
}

type applyReviewSuggestionHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		ExpectedHeadSHA string                             `json:"expected_head_sha,omitempty"`
		Message         string                             `json:"message,omitempty"`
		Suggestions     []applyReviewSuggestionRequestItem `json:"suggestions"`
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
type setPullLabelsHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.SetLabelsRequest
}

type setPullAssigneesHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.SetAssigneesRequest
}

type setPullReviewersHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setReviewersRequest
}

type approvePRHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body string `json:"body"`
		// ExpectedHeadSHA: see approvePRInput.
		ExpectedHeadSHA string `json:"expected_head_sha,omitempty"`
	}
}

type mergePRHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         mergePRInputBody
}

type deferMergePRHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         mergePRInputBody
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

func repoNumberFromHost(input *repoNumberHostInput) repoNumberInput {
	return repoNumberInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
	}
}

func (s *Handler) getPullOnHost(ctx context.Context, input *repoNumberHostInput) (*getPullOutput, error) {
	next := repoNumberFromHost(input)
	return s.getPull(ctx, &next)
}

func (s *Handler) getMRImportMetadataOnHost(ctx context.Context, input *repoNumberHostInput) (*getMRImportMetadataOutput, error) {
	next := repoNumberFromHost(input)
	return s.getMRImportMetadata(ctx, &next)
}

func (s *Handler) setKanbanStateOnHost(ctx context.Context, input *setKanbanStateHostInput) (*statusOnlyOutput, error) {
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

func (s *Handler) editPRContentOnHost(ctx context.Context, input *editPRContentHostInput) (*editPRContentOutput, error) {
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

func (s *Handler) postCommentOnHost(ctx context.Context, input *postCommentHostInput) (*postCommentOutput, error) {
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

func (s *Handler) editCommentOnHost(ctx context.Context, input *editCommentHostInput) (*editCommentOutput, error) {
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

func (s *Handler) deleteCommentOnHost(ctx context.Context, input *deleteCommentHostInput) (*deleteCommentOutput, error) {
	return s.deleteComment(ctx, &deleteCommentInput{
		Provider: input.Provider, PlatformHost: input.PlatformHost,
		Owner: input.Owner, Name: input.Name, Number: input.Number, CommentID: input.CommentID,
	})
}

func (s *Handler) replyToDiscussionOnHost(ctx context.Context, input *replyToDiscussionHostInput) (*replyToDiscussionOutput, error) {
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

func (s *Handler) resolveDiscussionOnHost(ctx context.Context, input *resolveDiscussionHostInput) (*resolveDiscussionOutput, error) {
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

func (s *Handler) setPullLabelsOnHost(ctx context.Context, input *setPullLabelsHostInput) (*setLabelsOutput, error) {
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

func (s *Handler) setPullAssigneesOnHost(ctx context.Context, input *setPullAssigneesHostInput) (*setAssigneesOutput, error) {
	next := setPullAssigneesInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.setPullAssignees(ctx, &next)
}

func (s *Handler) setPullReviewersOnHost(ctx context.Context, input *setPullReviewersHostInput) (*setReviewersOutput, error) {
	next := setPullReviewersInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.setPullReviewers(ctx, &next)
}

func (s *Handler) getDiffReviewDraftOnHost(
	ctx context.Context,
	input *repoNumberHostInput,
) (*getDiffReviewDraftOutput, error) {
	next := repoNumberFromHost(input)
	return s.getDiffReviewDraft(ctx, &next)
}

func (s *Handler) createDiffReviewDraftCommentOnHost(
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

func (s *Handler) editDiffReviewDraftCommentOnHost(
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

func (s *Handler) deleteDiffReviewDraftCommentOnHost(
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

func (s *Handler) publishDiffReviewDraftOnHost(
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

func (s *Handler) discardDiffReviewDraftOnHost(
	ctx context.Context,
	input *repoNumberHostInput,
) (*statusOnlyOutput, error) {
	next := repoNumberFromHost(input)
	return s.discardDiffReviewDraft(ctx, &next)
}

func (s *Handler) applyReviewSuggestionsOnHost(
	ctx context.Context,
	input *applyReviewSuggestionHostInput,
) (*applyReviewSuggestionOutput, error) {
	next := applyReviewSuggestionInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.applyReviewSuggestions(ctx, &next)
}

func (s *Handler) resolveDiffReviewThreadOnHost(
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

func (s *Handler) unresolveDiffReviewThreadOnHost(
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

func (s *Handler) approvePROnHost(ctx context.Context, input *approvePRHostInput) (*actionStatusOutput, error) {
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

func (s *Handler) requestChangesPROnHost(
	ctx context.Context,
	input *requestChangesPRHostInput,
) (*actionStatusOutput, error) {
	next := requestChangesPRInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.requestChangesPR(ctx, &next)
}

func (s *Handler) approveWorkflowsOnHost(ctx context.Context, input *repoNumberHostInput) (*actionStatusOutput, error) {
	next := repoNumberFromHost(input)
	return s.approveWorkflows(ctx, &next)
}

func (s *Handler) readyForReviewOnHost(ctx context.Context, input *repoNumberHostInput) (*actionStatusOutput, error) {
	next := repoNumberFromHost(input)
	return s.readyForReview(ctx, &next)
}

func (s *Handler) mergePROnHost(ctx context.Context, input *mergePRHostInput) (*mergePROutput, error) {
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

func (s *Handler) deferMergePROnHost(ctx context.Context, input *deferMergePRHostInput) (*deferMergePROutput, error) {
	next := deferMergePRInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.deferMergePR(ctx, &next)
}

func (s *Handler) setPRGitHubStateOnHost(ctx context.Context, input *githubStateHostInput) (*githubStateOutput, error) {
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

func (s *Handler) getCommitsOnHost(ctx context.Context, input *repoNumberHostInput) (*getCommitsOutput, error) {
	next := repoNumberFromHost(input)
	return s.getCommits(ctx, &next)
}

func (s *Handler) getDiffOnHost(ctx context.Context, input *getDiffHostInput) (*getDiffOutput, error) {
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

func (s *Handler) getFilesOnHost(ctx context.Context, input *repoNumberHostInput) (*getFilesOutput, error) {
	next := getFilesInput(repoNumberFromHost(input))
	return s.getFiles(ctx, &next)
}

func (s *Handler) getFilePreviewOnHost(ctx context.Context, input *getFilePreviewHostInput) (*getFilePreviewOutput, error) {
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

func (s *Handler) getStackForPROnHost(ctx context.Context, input *repoNumberHostInput) (*getStackForPROutput, error) {
	next := repoNumberFromHost(input)
	return s.getStackForPR(ctx, &next)
}

package gitlab

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"go.kenn.io/middleman/internal/platform"
)

// discussionIDPattern validates GitLab discussion IDs which are 40-char hex strings.
var discussionIDPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

func validateDiscussionID(discussionID string) error {
	if !discussionIDPattern.MatchString(discussionID) {
		return fmt.Errorf("invalid discussion ID format: must be 40-character hex string")
	}
	return nil
}

func (c *Client) ReplyToThread(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	discussionID string,
	body string,
) (platform.MergeRequestEvent, error) {
	if err := validateDiscussionID(discussionID); err != nil {
		return platform.MergeRequestEvent{}, err
	}

	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}

	note, _, err := c.api.Discussions.AddMergeRequestDiscussionNote(
		pid,
		int64(number),
		discussionID,
		&gitlab.AddMergeRequestDiscussionNoteOptions{Body: &body},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.MergeRequestEvent{}, mapGitLabError("reply_to_discussion", err)
	}

	return mergeRequestNoteEvent(normalizedRef, number, note, discussionID), nil
}

func (c *Client) ResolveThread(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	discussionID string,
	resolved bool,
) error {
	if err := validateDiscussionID(discussionID); err != nil {
		return err
	}

	pid, _, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return err
	}

	_, _, err = c.api.Discussions.ResolveMergeRequestDiscussion(
		pid,
		int64(number),
		discussionID,
		&gitlab.ResolveMergeRequestDiscussionOptions{Resolved: &resolved},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return mapGitLabError("resolve_discussion", err)
	}
	return nil
}

func (c *Client) CreateMergeRequestComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.MergeRequestEvent, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	note, _, err := c.api.Notes.CreateMergeRequestNote(
		pid,
		int64(number),
		&gitlab.CreateMergeRequestNoteOptions{Body: &body},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.MergeRequestEvent{}, mapGitLabError("create_merge_request_comment", err)
	}
	return mergeRequestNoteEvent(normalizedRef, number, note, ""), nil
}

func (c *Client) EditMergeRequestComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commentID int64,
	body string,
) (platform.MergeRequestEvent, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}
	note, _, err := c.api.Notes.UpdateMergeRequestNote(
		pid,
		int64(number),
		commentID,
		&gitlab.UpdateMergeRequestNoteOptions{Body: &body},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.MergeRequestEvent{}, mapGitLabError("edit_merge_request_comment", err)
	}
	return mergeRequestNoteEvent(normalizedRef, number, note, ""), nil
}

func (c *Client) CreateIssueComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.IssueEvent, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.IssueEvent{}, err
	}
	note, _, err := c.api.Notes.CreateIssueNote(
		pid,
		int64(number),
		&gitlab.CreateIssueNoteOptions{Body: &body},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.IssueEvent{}, mapGitLabError("create_issue_comment", err)
	}
	return issueNoteEvent(normalizedRef, number, note), nil
}

func (c *Client) EditIssueComment(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commentID int64,
	body string,
) (platform.IssueEvent, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.IssueEvent{}, err
	}
	note, _, err := c.api.Notes.UpdateIssueNote(
		pid,
		int64(number),
		commentID,
		&gitlab.UpdateIssueNoteOptions{Body: &body},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.IssueEvent{}, mapGitLabError("edit_issue_comment", err)
	}
	return issueNoteEvent(normalizedRef, number, note), nil
}

func (c *Client) SetMergeRequestState(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	state string,
) (platform.MergeRequest, error) {
	stateEvent, err := gitlabStateEvent(state)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	mr, _, err := c.api.MergeRequests.UpdateMergeRequest(
		pid,
		int64(number),
		&gitlab.UpdateMergeRequestOptions{StateEvent: &stateEvent},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.MergeRequest{}, mapGitLabError("set_merge_request_state", err)
	}
	return NormalizeDetailedMergeRequest(normalizedRef, mr), nil
}

func (c *Client) SetIssueState(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	state string,
) (platform.Issue, error) {
	stateEvent, err := gitlabStateEvent(state)
	if err != nil {
		return platform.Issue{}, err
	}
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.Issue{}, err
	}
	issue, _, err := c.api.Issues.UpdateIssue(
		pid,
		int64(number),
		&gitlab.UpdateIssueOptions{StateEvent: &stateEvent},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.Issue{}, mapGitLabError("set_issue_state", err)
	}
	return NormalizeIssue(normalizedRef, issue), nil
}

// MergeMergeRequest accepts an MR. GitLab does not take a merge strategy
// per request: squash is a flag on accept, while merge-commit versus
// fast-forward behavior comes from the project's merge method setting.
// "squash" and "merge" map onto the squash flag; "rebase" cannot be
// honored per request and returns a typed capability error.
func (c *Client) MergeMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	commitTitle string,
	commitMessage string,
	method string,
) (platform.MergeResult, error) {
	opts := &gitlab.AcceptMergeRequestOptions{}
	squash := false
	switch method {
	case "squash":
		squash = true
		opts.Squash = &squash
		if message := combineCommitMessage(commitTitle, commitMessage); message != "" {
			opts.SquashCommitMessage = &message
		}
	case "merge":
		opts.Squash = &squash
		if message := combineCommitMessage(commitTitle, commitMessage); message != "" {
			opts.MergeCommitMessage = &message
		}
	default:
		return platform.MergeResult{}, &platform.Error{
			Code:         platform.ErrCodeUnsupportedCapability,
			Provider:     platform.KindGitLab,
			PlatformHost: c.host,
			Capability:   "merge_method_" + method,
		}
	}

	pid, _, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.MergeResult{}, err
	}
	mr, _, err := c.api.MergeRequests.AcceptMergeRequest(pid, int64(number), opts, gitlab.WithContext(ctx))
	if err != nil {
		return platform.MergeResult{}, mapGitLabError("merge_merge_request", err)
	}
	if mr == nil {
		return platform.MergeResult{}, &platform.Error{
			Code:       platform.ErrCodeNotFound,
			Provider:   platform.KindGitLab,
			Capability: "merge_merge_request",
		}
	}
	return platform.MergeResult{
		Merged: normalizeMergeRequestState(mr.State) == "merged",
		SHA:    firstNonEmpty(mr.SquashCommitSHA, mr.MergeCommitSHA, mr.SHA),
	}, nil
}

func (c *Client) CreateIssue(
	ctx context.Context,
	ref platform.RepoRef,
	title string,
	body string,
) (platform.Issue, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.Issue{}, err
	}
	issue, _, err := c.api.Issues.CreateIssue(
		pid,
		&gitlab.CreateIssueOptions{Title: &title, Description: &body},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.Issue{}, mapGitLabError("create_issue", err)
	}
	return NormalizeIssue(normalizedRef, issue), nil
}

func (c *Client) EditMergeRequestContent(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	title *string,
	body *string,
) (platform.MergeRequest, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.MergeRequest{}, err
	}
	mr, _, err := c.api.MergeRequests.UpdateMergeRequest(
		pid,
		int64(number),
		&gitlab.UpdateMergeRequestOptions{Title: title, Description: body},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.MergeRequest{}, mapGitLabError("edit_merge_request_content", err)
	}
	return NormalizeDetailedMergeRequest(normalizedRef, mr), nil
}

func (c *Client) EditIssueContent(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	title *string,
	body *string,
) (platform.Issue, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.Issue{}, err
	}
	issue, _, err := c.api.Issues.UpdateIssue(
		pid,
		int64(number),
		&gitlab.UpdateIssueOptions{Title: title, Description: body},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.Issue{}, mapGitLabError("edit_issue_content", err)
	}
	return NormalizeIssue(normalizedRef, issue), nil
}

// ApproveMergeRequest approves an MR through the GitLab approvals API.
// GitLab approvals carry no body, so a non-empty body is posted as a
// regular MR note before approving. GitLab has no native
// "request changes" review state, so only approval is supported here.
func (c *Client) ApproveMergeRequest(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	body string,
) (platform.MergeRequestEvent, error) {
	pid, normalizedRef, err := c.projectScopedArg(ctx, ref)
	if err != nil {
		return platform.MergeRequestEvent{}, err
	}

	if comment := strings.TrimSpace(body); comment != "" {
		if _, _, err := c.api.Notes.CreateMergeRequestNote(
			pid,
			int64(number),
			&gitlab.CreateMergeRequestNoteOptions{Body: &comment},
			gitlab.WithContext(ctx),
		); err != nil {
			return platform.MergeRequestEvent{}, mapGitLabError("approve_merge_request_comment", err)
		}
	}

	approvals, _, err := c.api.MergeRequestApprovals.ApproveMergeRequest(
		pid,
		int64(number),
		&gitlab.ApproveMergeRequestOptions{},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return platform.MergeRequestEvent{}, mapGitLabError("approve_merge_request", err)
	}

	author := c.currentUsername(ctx)
	createdAt := time.Now().UTC()
	if approvals != nil && approvals.UpdatedAt != nil {
		createdAt = approvals.UpdatedAt.UTC()
	}
	return platform.MergeRequestEvent{
		Repo:               normalizedRef,
		MergeRequestNumber: number,
		EventType:          "review",
		Author:             author,
		Summary:            "approved",
		Body:               body,
		CreatedAt:          createdAt,
		DedupeKey:          noteDedupeKey(normalizedRef, "mr", number, "approval", author),
	}, nil
}

// currentUsername resolves the token's user for attribution of synthesized
// approval events. Attribution is best effort: the approval itself has
// already succeeded, so lookup failures degrade to an empty author rather
// than failing the mutation.
func (c *Client) currentUsername(ctx context.Context) string {
	user, _, err := c.api.Users.CurrentUser(gitlab.WithContext(ctx))
	if err != nil || user == nil {
		return ""
	}
	return user.Username
}

func gitlabStateEvent(state string) (string, error) {
	switch state {
	case "open":
		return "reopen", nil
	case "closed":
		return "close", nil
	default:
		return "", fmt.Errorf("unsupported state %q: must be open or closed", state)
	}
}

func combineCommitMessage(title, message string) string {
	title = strings.TrimSpace(title)
	message = strings.TrimSpace(message)
	switch {
	case title == "":
		return message
	case message == "":
		return title
	default:
		return title + "\n\n" + message
	}
}

func mergeRequestNoteEvent(
	ref platform.RepoRef,
	number int,
	note *gitlab.Note,
	threadID string,
) platform.MergeRequestEvent {
	return platform.MergeRequestEvent{
		Repo:               ref,
		PlatformID:         note.ID,
		PlatformExternalID: strconv.FormatInt(note.ID, 10),
		MergeRequestNumber: number,
		EventType:          "issue_comment",
		Author:             noteAuthorUsername(note),
		Body:               note.Body,
		CreatedAt:          timeValue(note.CreatedAt),
		DedupeKey:          noteDedupeKey(ref, "mr", number, "note", strconv.FormatInt(note.ID, 10)),
		ThreadID:           threadID,
		PositionJSON:       serializeNotePosition(note.Position),
		Resolvable:         note.Resolvable,
		Resolved:           note.Resolved,
	}
}

func issueNoteEvent(
	ref platform.RepoRef,
	number int,
	note *gitlab.Note,
) platform.IssueEvent {
	return platform.IssueEvent{
		Repo:               ref,
		PlatformID:         note.ID,
		PlatformExternalID: strconv.FormatInt(note.ID, 10),
		IssueNumber:        number,
		EventType:          "issue_comment",
		Author:             noteAuthorUsername(note),
		Body:               note.Body,
		CreatedAt:          timeValue(note.CreatedAt),
		DedupeKey:          noteDedupeKey(ref, "issue", number, "note", strconv.FormatInt(note.ID, 10)),
	}
}

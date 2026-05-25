package gitlab

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

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

	return platform.MergeRequestEvent{
		Repo:               normalizedRef,
		PlatformID:         note.ID,
		PlatformExternalID: strconv.FormatInt(note.ID, 10),
		MergeRequestNumber: number,
		EventType:          "issue_comment",
		Author:             noteAuthorUsername(note),
		Body:               note.Body,
		CreatedAt:          timeValue(note.CreatedAt),
		DedupeKey:          noteDedupeKey(normalizedRef, "mr", number, "note", strconv.FormatInt(note.ID, 10)),
		ThreadID:           discussionID,
		PositionJSON:       serializeNotePosition(note.Position),
		Resolvable:         note.Resolvable,
		Resolved:           note.Resolved,
	}, nil
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

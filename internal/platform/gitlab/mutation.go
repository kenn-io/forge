package gitlab

import (
	"context"
	"strconv"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"go.kenn.io/middleman/internal/platform"
)

func (c *Client) ReplyToDiscussion(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	discussionID string,
	body string,
) (platform.MergeRequestEvent, error) {
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
		DiscussionID:       discussionID,
		Resolvable:         note.Resolvable,
		Resolved:           note.Resolved,
	}, nil
}

func (c *Client) ResolveDiscussion(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	discussionID string,
	resolved bool,
) error {
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

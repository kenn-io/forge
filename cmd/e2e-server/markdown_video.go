package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v92/github"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

// The demo video fixture is opt-in so the default e2e capability set and
// seeded item lists stay unchanged for the Playwright suite.
const (
	e2eMarkdownVideoSource      = "https://github.com/user-attachments/assets/e2e-demo-video"
	e2eMarkdownVideoIssueNumber = 14
	e2eMarkdownVideoThreadID    = "e2e-markdown-video-1"
)

// e2eMarkdownVideoClient serves one local clip as the demo GitHub attachment.
// Embedding keeps every optional provider interface of the workflow client.
type e2eMarkdownVideoClient struct {
	*e2eWorkflowClient

	path string
}

func (c *e2eMarkdownVideoClient) OpenMarkdownMedia(
	_ context.Context, _, _, sourceURL, byteRange string,
) (platform.MarkdownMedia, error) {
	if sourceURL != e2eMarkdownVideoSource {
		return platform.MarkdownMedia{}, &platform.Error{
			Code: platform.ErrCodeNotFound, Provider: platform.KindGitHub, PlatformHost: "github.com",
		}
	}
	file, err := os.Open(c.path)
	if err != nil {
		return platform.MarkdownMedia{}, fmt.Errorf("open demo video: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return platform.MarkdownMedia{}, fmt.Errorf("stat demo video: %w", err)
	}
	size := info.Size()
	start, end, partial := parseSingleByteRange(byteRange, size)
	if partial && start >= size {
		_ = file.Close()
		return platform.MarkdownMedia{}, &platform.Error{
			Code: platform.ErrCodeRangeNotSatisfiable, Provider: platform.KindGitHub, PlatformHost: "github.com",
		}
	}
	media := platform.MarkdownMedia{
		Body: struct {
			io.Reader
			io.Closer
		}{io.NewSectionReader(file, start, end-start+1), file},
		ContentType:   "video/mp4",
		ContentLength: end - start + 1,
		Partial:       partial,
	}
	if partial {
		media.ContentRange = fmt.Sprintf("bytes %d-%d/%d", start, end, size)
	}
	return media, nil
}

// parseSingleByteRange accepts "bytes=a-b" and "bytes=a-". Any other value
// means the whole file, as a server that ignores Range would answer.
func parseSingleByteRange(byteRange string, size int64) (start, end int64, partial bool) {
	spec, ok := strings.CutPrefix(byteRange, "bytes=")
	if !ok {
		return 0, size - 1, false
	}
	first, last, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, size - 1, false
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return 0, size - 1, false
	}
	end = size - 1
	if last != "" {
		requested, err := strconv.ParseInt(last, 10, 64)
		if err != nil || requested < start {
			return 0, size - 1, false
		}
		end = min(requested, size-1)
	}
	return start, end, true
}

// seedMarkdownVideoFixture adds a synthetic issue and a review thread on
// acme/widgets#1 whose bodies hold the demo attachment URL alone in a
// paragraph, in both SQLite and the provider fixture so a sync keeps them.
func seedMarkdownVideoFixture(
	ctx context.Context,
	database *db.DB,
	fc *testutil.FixtureClient,
	headSHA string,
) error {
	repo, err := database.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	if err != nil {
		return fmt.Errorf("get markdown video repo: %w", err)
	}
	if repo == nil {
		return errors.New("get markdown video repo: not found")
	}
	now := time.Now().UTC().Add(-30 * time.Minute)
	issueBody := "The widget panel flickers when the sidebar collapses. Screen recording:\n\n" +
		e2eMarkdownVideoSource + "\n\n" +
		"It starts about two seconds in."
	const issueTitle = "Widget panel flickers when the sidebar collapses"
	if _, err := database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repo.ID,
		PlatformID:     3014,
		Number:         e2eMarkdownVideoIssueNumber,
		URL:            "https://github.com/acme/widgets/issues/14",
		Title:          issueTitle,
		Author:         "eve",
		State:          "open",
		Body:           issueBody,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}); err != nil {
		return fmt.Errorf("seed markdown video issue: %w", err)
	}
	fc.OpenIssues["acme/widgets"] = append(fc.OpenIssues["acme/widgets"], &gh.Issue{
		ID:        new(int64(3014)),
		Number:    new(e2eMarkdownVideoIssueNumber),
		Title:     new(issueTitle),
		Body:      new(issueBody),
		HTMLURL:   new("https://github.com/acme/widgets/issues/14"),
		State:     new("open"),
		User:      &gh.User{Login: new("eve")},
		CreatedAt: &gh.Timestamp{Time: now},
		UpdatedAt: &gh.Timestamp{Time: now},
	})

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 1)
	if err != nil {
		return fmt.Errorf("get markdown video pull request: %w", err)
	}
	if mr == nil {
		return errors.New("get markdown video pull request: not found")
	}
	threadBody := "This is what the cache miss looks like on a slow device:\n\n" + e2eMarkdownVideoSource
	if err := database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID:     mr.ID,
		PlatformExternalID: e2eMarkdownVideoThreadID,
		EventType:          "review_comment",
		Author:             "reviewer",
		Body:               threadBody,
		CreatedAt:          now,
		DedupeKey:          "review-comment-" + e2eMarkdownVideoThreadID,
	}}); err != nil {
		return fmt.Errorf("seed markdown video review event: %w", err)
	}
	line := 1
	if err := database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID:  e2eMarkdownVideoThreadID,
		ProviderCommentID: e2eMarkdownVideoThreadID,
		Body:              threadBody,
		AuthorLogin:       "reviewer",
		Range: db.ReviewLineRange{
			Path:        "internal/cache.go",
			Side:        "right",
			Line:        line,
			NewLine:     &line,
			LineType:    "context",
			DiffHeadSHA: mr.PlatformHeadSHA,
			CommitSHA:   mr.PlatformHeadSHA,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}}); err != nil {
		return fmt.Errorf("seed markdown video review thread: %w", err)
	}
	key := "acme/widgets#1"
	fc.ReviewThreads[key] = append(fc.ReviewThreads[key], platformgithub.PullRequestReviewThread{
		NodeID: e2eMarkdownVideoThreadID,
		Path:   "internal/cache.go",
		Side:   "RIGHT",
		Line:   1,
		Comments: []platformgithub.PullRequestReviewThreadComment{{
			NodeID:           e2eMarkdownVideoThreadID,
			DatabaseID:       6902,
			ReviewDatabaseID: 5013,
			SubjectType:      "LINE",
			Body:             threadBody,
			AuthorLogin:      "reviewer",
			Path:             "internal/cache.go",
			Line:             1,
			URL:              "https://github.com/acme/widgets/pull/1#discussion_r6902",
			CommitID:         headSHA,
			OriginalCommitID: headSHA,
			CreatedAt:        now,
			UpdatedAt:        now,
		}},
	})
	return nil
}

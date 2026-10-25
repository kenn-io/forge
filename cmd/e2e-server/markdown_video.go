package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"
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

// e2eMarkdownVideoGallerySources are separate attachments that all serve the
// demo clip, so a pull request description holds several players with
// distinct sources.
var e2eMarkdownVideoGallerySources = []string{
	"https://github.com/user-attachments/assets/e2e-gallery-video-1",
	"https://github.com/user-attachments/assets/e2e-gallery-video-2",
	"https://github.com/user-attachments/assets/e2e-gallery-video-3",
	"https://github.com/user-attachments/assets/e2e-gallery-video-4",
	"https://github.com/user-attachments/assets/e2e-gallery-video-5",
}

// e2eMarkdownVideoClient serves one local clip as the demo GitHub attachment.
// Embedding keeps every optional provider interface of the workflow client.
type e2eMarkdownVideoClient struct {
	*e2eWorkflowClient

	path string
	// interrupt ends each response at the first multiple of
	// e2eMarkdownVideoInterruptEvery past its start, the way the daemon ends a
	// stream the browser stopped reading, so a test sees the browser fetch the
	// rest.
	interrupt bool
}

func (c *e2eMarkdownVideoClient) OpenMarkdownMedia(
	_ context.Context, _, _, sourceURL, byteRange string,
) (platform.MarkdownMedia, error) {
	if sourceURL != e2eMarkdownVideoSource && !slices.Contains(e2eMarkdownVideoGallerySources, sourceURL) {
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
	var body io.Reader = io.NewSectionReader(file, start, end-start+1)
	if cut := (start/e2eMarkdownVideoInterruptEvery + 1) * e2eMarkdownVideoInterruptEvery; c.interrupt && end >= cut {
		body = &interruptedReader{r: io.LimitReader(body, cut-start)}
	}
	media := platform.MarkdownMedia{
		Body: struct {
			io.Reader
			io.Closer
		}{body, file},
		ContentType:   markdownVideoContentType(c.path),
		ContentLength: end - start + 1,
		Partial:       partial,
	}
	if partial {
		media.ContentRange = fmt.Sprintf("bytes %d-%d/%d", start, end, size)
	}
	return media, nil
}

const (
	// e2eMarkdownVideoInterruptEvery spaces the offsets where interrupted
	// responses end. It is a multiple of the 32 KiB blocks browsers hand to
	// the decoder, so the browser keeps every byte it got and asks again from
	// the cut. Cutting at every multiple also cuts the player's own request
	// after the preview copy has cached the start of the clip.
	e2eMarkdownVideoInterruptEvery = 64 << 10
	// e2eMarkdownVideoInterruptPause holds an interrupted response open long
	// enough for a test to pause the player before the response ends.
	e2eMarkdownVideoInterruptPause = 2 * time.Second
)

// errMarkdownVideoInterrupted fails the copy of an interrupted response, which
// aborts it the same way the stall limit does.
var errMarkdownVideoInterrupted = errors.New("demo video response interrupted")

// interruptedReader passes r through, waits
// e2eMarkdownVideoInterruptPause, then fails.
type interruptedReader struct {
	r io.Reader
}

func (r *interruptedReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if !errors.Is(err, io.EOF) {
		return n, err
	}
	<-time.After(e2eMarkdownVideoInterruptPause)
	return n, errMarkdownVideoInterrupted
}

// markdownVideoContentType lets browser tests use a WebM clip, which every
// Playwright browser decodes, while demos keep MP4.
func markdownVideoContentType(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".webm") {
		return "video/webm"
	}
	return "video/mp4"
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
// paragraph, and appends the gallery attachments to the description of
// acme/widgets#1, in both SQLite and the provider fixture so a sync keeps them.
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
	issue := &gh.Issue{
		ID:        new(int64(3014)),
		Number:    new(e2eMarkdownVideoIssueNumber),
		Title:     new(issueTitle),
		Body:      new(issueBody),
		HTMLURL:   new("https://github.com/acme/widgets/issues/14"),
		State:     new("open"),
		User:      &gh.User{Login: new("eve")},
		CreatedAt: &gh.Timestamp{Time: now},
		UpdatedAt: &gh.Timestamp{Time: now},
	}
	// Lists read open issues; a detail refresh reads one issue by number.
	fc.OpenIssues["acme/widgets"] = append(fc.OpenIssues["acme/widgets"], issue)
	fc.Issues["acme/widgets"] = append(fc.Issues["acme/widgets"], issue)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 1)
	if err != nil {
		return fmt.Errorf("get markdown video pull request: %w", err)
	}
	if mr == nil {
		return errors.New("get markdown video pull request: not found")
	}
	description := mr.Body + "\n\nRecordings from each step of the release check:\n\n" +
		strings.Join(e2eMarkdownVideoGallerySources, "\n\n")
	if err := database.UpdateMRTitleBody(ctx, mr.ID, mr.Title, description, mr.UpdatedAt); err != nil {
		return fmt.Errorf("seed markdown video pull request description: %w", err)
	}
	for _, pulls := range [][]*gh.PullRequest{fc.OpenPRs["acme/widgets"], fc.PRs["acme/widgets"]} {
		for _, pull := range pulls {
			if pull.GetNumber() == 1 {
				pull.Body = new(description)
			}
		}
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

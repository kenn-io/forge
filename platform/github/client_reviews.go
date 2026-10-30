package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/platform"
)

func (c *Client) CreateReview(
	ctx context.Context, owner, repo string, number int,
	event string, body string,
) (*gh.PullRequestReview, error) {
	return c.CreateReviewWithComments(ctx, owner, repo, number, event, body, "", nil)
}

func (c *Client) CreateReviewWithComments(
	ctx context.Context,
	owner, repo string,
	number int,
	event string,
	body string,
	commitID string,
	comments []*gh.DraftReviewComment,
) (*gh.PullRequestReview, error) {
	request := &gh.PullRequestReviewRequest{
		Event:    new(event),
		Body:     new(body),
		Comments: comments,
	}
	if commitID != "" {
		request.CommitID = &commitID
	}
	review, resp, err := c.writeGH().PullRequests.CreateReview(
		ctx, owner, repo, number, request,
	)
	c.trackWriteRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"creating review on %s/%s#%d: %w", owner, repo, number, err,
		)
	}
	return review, nil
}

func (c *Client) ApplyReviewSuggestions(
	ctx context.Context,
	owner, repo string,
	number int,
	input platform.ApplyReviewSuggestionsInput,
) (*platform.AppliedReviewSuggestions, error) {
	headOwner, headRepo, headFullName, err := githubSuggestionHeadRepo(owner, repo, input)
	if err != nil {
		return nil, err
	}
	headBranch := strings.TrimSpace(input.HeadBranch)
	expectedHeadSHA := strings.TrimSpace(input.ExpectedHeadSHA)
	if headBranch == "" {
		return nil, githubSuggestionPlatformError(platform.ErrCodeInvalidArgument, "head branch is required", nil)
	}
	if expectedHeadSHA == "" {
		return nil, githubSuggestionPlatformError(platform.ErrCodeInvalidArgument, "expected head SHA is required", nil)
	}

	byPath := make(map[string][]platform.ReviewSuggestion)
	for _, suggestion := range input.Suggestions {
		path := suggestion.Range.Path
		trimmedPath := strings.TrimSpace(path)
		if trimmedPath == "" {
			return nil, githubSuggestionPlatformError(platform.ErrCodeInvalidArgument, "suggestion path is required", nil)
		}
		if path != trimmedPath {
			return nil, githubSuggestionPlatformError(
				platform.ErrCodeInvalidArgument,
				"suggestion path has leading or trailing whitespace",
				nil,
			)
		}
		byPath[path] = append(byPath[path], suggestion)
	}
	if len(byPath) == 0 {
		return nil, githubSuggestionPlatformError(platform.ErrCodeInvalidArgument, "at least one suggestion is required", nil)
	}
	if err := c.ensureReviewSuggestionPullMutable(ctx, owner, repo, number, headFullName, headBranch, expectedHeadSHA); err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	additions := make([]map[string]string, 0, len(paths))
	for _, path := range paths {
		content, err := c.githubFileContentAtRef(ctx, headOwner, headRepo, path, expectedHeadSHA)
		if err != nil {
			return nil, err
		}
		nextContent, err := applyReviewSuggestionEdits(content, byPath[path])
		if err != nil {
			return nil, githubSuggestionPlatformError(platform.ErrCodeInvalidArgument, err.Error(), err)
		}
		additions = append(additions, map[string]string{
			"path":     path,
			"contents": base64.StdEncoding.EncodeToString([]byte(nextContent)),
		})
	}

	return c.createCommitForReviewSuggestions(
		ctx,
		owner,
		repo,
		number,
		headFullName,
		headBranch,
		expectedHeadSHA,
		strings.TrimSpace(input.Message),
		additions,
	)
}

func (c *Client) ensureReviewSuggestionPullMutable(
	ctx context.Context,
	owner string,
	repo string,
	number int,
	headFullName string,
	headBranch string,
	expectedHeadSHA string,
) error {
	pr, err := c.GetPullRequest(ctx, owner, repo, number)
	if err != nil {
		return err
	}
	if !strings.EqualFold(pr.GetState(), "open") {
		return c.githubSuggestionConflict("not_open", "pull request is not open")
	}
	liveHeadFullName := githubSuggestionLiveHeadRepoFullName(pr)
	if liveHeadFullName == "" || !strings.EqualFold(liveHeadFullName, headFullName) {
		return c.githubSuggestionConflict("head_repo_unknown", "pull request head repository is unknown")
	}
	if pr.Head == nil {
		return c.githubSuggestionStaleState("pull request head changed since it was reviewed")
	}
	liveHeadBranch := strings.TrimSpace(pr.Head.GetRef())
	if liveHeadBranch == "" || liveHeadBranch != headBranch {
		return c.githubSuggestionStaleState("pull request head branch changed since it was reviewed")
	}
	liveHeadSHA := strings.TrimSpace(pr.Head.GetSHA())
	if liveHeadSHA == "" || !strings.EqualFold(liveHeadSHA, expectedHeadSHA) {
		return c.githubSuggestionStaleState("pull request head changed since it was reviewed")
	}
	headOwner, headRepo, ok := strings.Cut(liveHeadFullName, "/")
	if !ok || headOwner == "" || headRepo == "" {
		return c.githubSuggestionConflict("head_repo_unknown", "pull request head repository is unknown")
	}
	if err := c.ensureReviewSuggestionHeadRepoReachable(ctx, headOwner, headRepo); err != nil {
		return err
	}
	return nil
}

func (c *Client) ensureReviewSuggestionHeadRepoReachable(
	ctx context.Context,
	owner string,
	repo string,
) error {
	_, resp, err := c.writeGH().Repositories.Get(ctx, owner, repo)
	c.trackWriteRate(resp)
	if err != nil {
		if githubSuggestionHeadRepoUnavailable(err) {
			return c.githubSuggestionConflict("head_repo_unknown", "pull request head repository is unknown")
		}
		return err
	}
	return nil
}

func (c *Client) githubSuggestionConflict(reason string, message string) error {
	return &platform.Error{
		Code:         platform.ErrCodeConflict,
		Provider:     platform.KindGitHub,
		PlatformHost: c.platformHost,
		Details:      map[string]string{"reason": reason},
		Err:          fmt.Errorf("%s", message),
	}
}

func (c *Client) githubSuggestionStaleState(message string) error {
	return &platform.Error{
		Code:         platform.ErrCodeStaleState,
		Provider:     platform.KindGitHub,
		PlatformHost: c.platformHost,
		Err:          fmt.Errorf("%s", message),
	}
}

func githubSuggestionLiveHeadRepoFullName(pr *gh.PullRequest) string {
	if pr == nil || pr.Head == nil || pr.Head.Repo == nil {
		return ""
	}
	if fullName := strings.TrimSpace(pr.Head.Repo.GetFullName()); fullName != "" {
		return fullName
	}
	if fullName := ParseHeadRepoFullName(pr.Head.Repo.GetCloneURL()); fullName != "" {
		return fullName
	}
	owner := strings.TrimSpace(pr.Head.Repo.GetOwner().GetLogin())
	name := strings.TrimSpace(pr.Head.Repo.GetName())
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

func githubSuggestionHeadRepoUnavailable(err error) bool {
	ghErr, ok := errors.AsType[*gh.ErrorResponse](err)
	if !ok || ghErr.Response == nil {
		return false
	}
	switch ghErr.Response.StatusCode {
	case http.StatusNotFound, http.StatusGone:
		return true
	default:
		return false
	}
}

func (c *Client) githubFileContentAtRef(
	ctx context.Context,
	owner string,
	repo string,
	path string,
	ref string,
) (string, error) {
	file, directory, resp, err := c.writeGH().Repositories.GetContents(
		ctx,
		owner,
		repo,
		path,
		&gh.RepositoryContentGetOptions{Ref: ref},
	)
	c.trackWriteRate(resp)
	if err != nil {
		return "", fmt.Errorf("getting %s/%s:%s at %s: %w", owner, repo, path, ref, err)
	}
	if directory != nil || file == nil {
		return "", githubSuggestionPlatformError(platform.ErrCodeInvalidArgument, "suggestion target is not a file", nil)
	}
	content, err := file.GetContent()
	if err != nil {
		return "", fmt.Errorf("decode %s/%s:%s at %s: %w", owner, repo, path, ref, err)
	}
	return content, nil
}

const createCommitOnBranchMutation = `mutation($input: CreateCommitOnBranchInput!) {
  createCommitOnBranch(input: $input) {
    commit {
      oid
      url
    }
  }
}`

func (c *Client) createCommitForReviewSuggestions(
	ctx context.Context,
	owner string,
	repo string,
	number int,
	headFullName string,
	headBranch string,
	expectedHeadSHA string,
	message string,
	additions []map[string]string,
) (*platform.AppliedReviewSuggestions, error) {
	if err := c.ensureReviewSuggestionPullMutable(ctx, owner, repo, number, headFullName, headBranch, expectedHeadSHA); err != nil {
		return nil, err
	}

	type createCommitResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			CreateCommitOnBranch *struct {
				Commit *struct {
					OID string `json:"oid"`
					URL string `json:"url"`
				} `json:"commit"`
			} `json:"createCommitOnBranch"`
		} `json:"data"`
	}
	if message == "" {
		message = "Apply review suggestion"
		if len(additions) > 1 {
			message = "Apply review suggestions"
		}
	}
	payload, err := json.Marshal(graphQLRequest{
		Query: createCommitOnBranchMutation,
		Variables: map[string]any{
			"input": map[string]any{
				"branch": map[string]string{
					"repositoryNameWithOwner": headFullName,
					"branchName":              headBranch,
				},
				"expectedHeadOid": expectedHeadSHA,
				"message": map[string]string{
					"headline": message,
				},
				"fileChanges": map[string]any{
					"additions": additions,
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal review suggestion commit mutation: %w", err)
	}
	req, err := http.NewRequestWithContext(
		c.graphQLContext(ctx),
		http.MethodPost,
		c.graphQLEndpoint,
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("create review suggestion commit request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.writeHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("applying review suggestions on %s/%s#%d: %w", owner, repo, number, err)
	}
	c.trackWriteGraphQLRateHeaders(resp)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("applying review suggestions on %s/%s#%d: graphql status %s", owner, repo, number, resp.Status)
	}
	var decoded createCommitResponse
	if err := json.UnmarshalRead(resp.Body, &decoded); err != nil {
		return nil, fmt.Errorf("decode review suggestion commit response for %s/%s#%d: %w", owner, repo, number, err)
	}
	if len(decoded.Errors) > 0 {
		return nil, githubCreateCommitGraphQLError(owner, repo, number, decoded.Errors)
	}
	if decoded.Data.CreateCommitOnBranch == nil || decoded.Data.CreateCommitOnBranch.Commit == nil {
		return nil, fmt.Errorf("applying review suggestions on %s/%s#%d: missing commit in graphql response", owner, repo, number)
	}
	commit := decoded.Data.CreateCommitOnBranch.Commit
	return &platform.AppliedReviewSuggestions{
		CommitSHA: commit.OID,
		CommitURL: commit.URL,
	}, nil
}

func githubSuggestionHeadRepo(
	owner string,
	repo string,
	input platform.ApplyReviewSuggestionsInput,
) (string, string, string, error) {
	fullName := ParseHeadRepoFullName(input.HeadRepoCloneURL)
	if fullName == "" {
		return "", "", "", githubSuggestionPlatformError(
			platform.ErrCodeInvalidArgument,
			"head repository is required",
			nil,
		)
	}
	headOwner, headRepo, ok := strings.Cut(fullName, "/")
	if !ok || headOwner == "" || headRepo == "" || strings.Contains(headRepo, "/") {
		return "", "", "", githubSuggestionPlatformError(
			platform.ErrCodeInvalidArgument,
			"head repository is required",
			nil,
		)
	}
	return headOwner, headRepo, fullName, nil
}

func githubCreateCommitGraphQLError(
	owner string,
	repo string,
	number int,
	graphQLErrors []graphQLError,
) error {
	message := joinGraphQLErrorMessages(graphQLErrors)
	wrapped := fmt.Errorf(
		"applying review suggestions on %s/%s#%d: graphql errors: %s",
		owner, repo, number, message,
	)
	if githubCreateCommitStaleError(graphQLErrors) {
		return &platform.Error{
			Code:     platform.ErrCodeStaleState,
			Provider: platform.KindGitHub,
			Err:      wrapped,
		}
	}
	for _, graphQLError := range graphQLErrors {
		if strings.EqualFold(graphQLError.Type, "NOT_FOUND") ||
			strings.Contains(graphQLError.Message, "Could not resolve") {
			// Post-preflight resolution failures are head repo/branch
			// races, not user-addressable 404s: fail closed with the
			// same stable reason as the preflight so clients run
			// conflict recovery.
			return &platform.Error{
				Code:     platform.ErrCodeConflict,
				Provider: platform.KindGitHub,
				Details:  map[string]string{"reason": "head_repo_unknown"},
				Err:      wrapped,
			}
		}
	}
	return wrapped
}

func githubCreateCommitStaleError(graphQLErrors []graphQLError) bool {
	for _, graphQLError := range graphQLErrors {
		message := strings.ToLower(graphQLError.Message)
		if strings.Contains(message, "expectedheadoid") ||
			strings.Contains(message, "expected head") ||
			strings.Contains(message, "stale") ||
			(strings.Contains(message, "head") &&
				strings.Contains(message, "expected") &&
				strings.Contains(message, "match")) {
			return true
		}
	}
	return false
}

func githubSuggestionPlatformError(
	code platform.PlatformErrorCode,
	message string,
	err error,
) error {
	if err == nil {
		err = errors.New(message)
	}
	return &platform.Error{
		Code:     code,
		Provider: platform.KindGitHub,
		Err:      err,
	}
}

type reviewSuggestionEdit struct {
	start       int
	end         int
	replacement []string
}

func applyReviewSuggestionEdits(
	content string,
	suggestions []platform.ReviewSuggestion,
) (string, error) {
	lines, newline, trailingNewline := splitGitHubSuggestionContent(content)
	if lines == nil {
		lines = []string{}
	}
	edits := make([]reviewSuggestionEdit, 0, len(suggestions))
	for _, suggestion := range suggestions {
		if strings.ToLower(strings.TrimSpace(suggestion.Range.Side)) != "right" {
			return "", fmt.Errorf("suggestion %q is not on the right side", suggestion.Range.Path)
		}
		start := suggestion.Range.Line
		if suggestion.Range.StartLine != nil {
			start = *suggestion.Range.StartLine
		}
		end := suggestion.Range.Line
		if start <= 0 || end <= 0 || start > end {
			return "", fmt.Errorf("suggestion %q has invalid line range", suggestion.Range.Path)
		}
		if end > len(lines) {
			return "", fmt.Errorf("suggestion %q range exceeds file length", suggestion.Range.Path)
		}
		edits = append(edits, reviewSuggestionEdit{
			start:       start,
			end:         end,
			replacement: githubSuggestionReplacementLines(suggestion.Replacement),
		})
	}
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].start == edits[j].start {
			return edits[i].end < edits[j].end
		}
		return edits[i].start < edits[j].start
	})
	for i := 1; i < len(edits); i += 1 {
		if edits[i].start <= edits[i-1].end {
			return "", errors.New("suggestions contain overlapping line ranges")
		}
	}
	for i := len(edits) - 1; i >= 0; i -= 1 {
		edit := edits[i]
		prefix := append([]string{}, lines[:edit.start-1]...)
		prefix = append(prefix, edit.replacement...)
		lines = append(prefix, lines[edit.end:]...)
	}
	return joinGitHubSuggestionContent(lines, newline, trailingNewline), nil
}

func splitGitHubSuggestionContent(content string) ([]string, string, bool) {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
		content = strings.ReplaceAll(content, "\r\n", "\n")
	}
	trailingNewline := strings.HasSuffix(content, "\n")
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return []string{}, newline, trailingNewline
	}
	return strings.Split(content, "\n"), newline, trailingNewline
}

func joinGitHubSuggestionContent(lines []string, newline string, trailingNewline bool) string {
	content := strings.Join(lines, newline)
	if trailingNewline && len(lines) > 0 {
		content += newline
	}
	return content
}

func githubSuggestionReplacementLines(replacement string) []string {
	replacement = strings.ReplaceAll(replacement, "\r\n", "\n")
	if replacement == "" {
		return nil
	}
	return strings.Split(replacement, "\n")
}

func (c *Client) DismissReview(
	ctx context.Context, owner, repo string, number int, reviewID int64, message string,
) (*gh.PullRequestReview, error) {
	review, resp, err := c.writeGH().PullRequests.DismissReview(
		ctx, owner, repo, number, reviewID,
		gh.PullRequestDismissReviewRequest{Message: message},
	)
	c.trackWriteRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"dismissing review %d on %s/%s#%d: %w", reviewID, owner, repo, number, err,
		)
	}
	return review, nil
}

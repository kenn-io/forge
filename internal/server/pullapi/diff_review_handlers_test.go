package pullapi

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v90/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

type countingReviewSyncClient struct {
	ghclient.Client
	pullCalls atomic.Int32
}

func (c *countingReviewSyncClient) GetPullRequest(
	context.Context, string, string, int,
) (*gh.PullRequest, error) {
	c.pullCalls.Add(1)
	return nil, errors.New("removed pull must not be fetched")
}

func TestReviewBackgroundSyncsRecheckRemovedUpstream(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = "repo-acme-widget"
	repoID, err := database.UpsertRepo(ctx, identity)
	require.NoError(err)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 7, PlatformExternalID: "pull-7", Number: 7,
		Title: "Removed pull", State: db.MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, 'merge_request', 7, 'pull-7', ?, ?, 'removed_upstream')`,
		repoID, now, now,
	)
	require.NoError(err)
	repo, err := database.GetRepoByID(ctx, repoID)
	require.NoError(err)
	require.NotNil(repo)

	client := &countingReviewSyncClient{Client: testutil.NewFixtureClient()}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": client}, database, nil,
		[]ghclient.RepoRef{{
			Platform: "github", PlatformHost: "github.com",
			Owner: "acme", Name: "widget", PlatformExternalID: "repo-acme-widget",
		}},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	var queued func(context.Context) error
	handler := New(Deps{
		DB: database, Syncer: syncer,
		EnqueueDetailSyncOrRerun: func(
			_ string, _ []any, fn func(context.Context) error,
		) bool {
			queued = fn
			return true
		},
	})
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(handler.Shutdown(shutdownCtx))
	})

	handler.syncAfterReviewSuggestionApply(*repo, 7)
	require.NotNil(queued)
	require.NoError(queued(ctx))
	handler.syncAfterReviewDraftPublish(*repo, 7)
	handler.bgWG.Wait()
	require.Zero(client.pullCalls.Load())
}

func TestDBReviewLineRangeRejectsMalformedMultilineRanges(t *testing.T) {
	validLine := 10
	cases := []struct {
		name  string
		patch func(*diffReviewLineRange)
	}{
		{
			name: "non-positive start line",
			patch: func(input *diffReviewLineRange) {
				startLine := 0
				input.StartSide = "right"
				input.StartLine = &startLine
			},
		},
		{
			name: "start line without start side",
			patch: func(input *diffReviewLineRange) {
				input.StartLine = &validLine
			},
		},
		{
			name: "start side without start line",
			patch: func(input *diffReviewLineRange) {
				input.StartSide = "right"
			},
		},
		{
			name: "start line after end line",
			patch: func(input *diffReviewLineRange) {
				startLine := 11
				input.StartSide = "right"
				input.StartLine = &startLine
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			input := diffReviewLineRange{
				Path:        "src/main.go",
				Side:        "right",
				Line:        10,
				NewLine:     &validLine,
				LineType:    "add",
				DiffHeadSHA: "head-sha",
			}
			tc.patch(&input)

			_, err := dbReviewLineRange(input)
			require.Error(err)
		})
	}
}

func TestMarkdownSuggestionReplacementsSkipsNestedSuggestionFenceInCodeBlock(t *testing.T) {
	body := strings.Join([]string{
		"Reviewer explained this with a markdown example.",
		"",
		"````markdown",
		"```suggestion",
		"return client.publishThreads();",
		"```",
		"````",
		"",
		"```suggestion",
		"return actualSuggestion();",
		"```",
	}, "\n")

	replacements := markdownSuggestionReplacements(body)
	require.Equal(t, []string{"return actualSuggestion();"}, replacements)

	_, err := verifyReviewSuggestionReplacement(body, "return client.publishThreads();")
	assert.Error(t, err)
}

func TestMarkdownSuggestionReplacementsSkipsNestedSuggestionFenceInIndentedCodeBlock(t *testing.T) {
	body := strings.Join([]string{
		"Reviewer explained this with an indented markdown example.",
		"",
		"   ````markdown",
		"```suggestion",
		"return client.publishThreads();",
		"```",
		"   ````",
		"",
		"  ```suggestion",
		"return actualSuggestion();",
		"  ```",
	}, "\n")

	replacements := markdownSuggestionReplacements(body)
	require.Equal(t, []string{"return actualSuggestion();"}, replacements)

	_, err := verifyReviewSuggestionReplacement(body, "return client.publishThreads();")
	assert.Error(t, err)
}

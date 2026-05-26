package github

import (
	"context"
	"fmt"

	"github.com/shurcooL/githubv4"
	"go.kenn.io/middleman/internal/platform"
)

// ResolveThread resolves or unresolves a GitHub review thread.
func (p gitHubClientProvider) ResolveThread(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	threadID string,
	resolved bool,
) error {
	if p.gqlClient == nil {
		return fmt.Errorf("GraphQL client not configured for host %s", p.host)
	}

	if err := validateGitHubThreadID(threadID); err != nil {
		return err
	}

	if resolved {
		return resolveReviewThread(ctx, p.gqlClient, threadID)
	}
	return unresolveReviewThread(ctx, p.gqlClient, threadID)
}

func validateGitHubThreadID(threadID string) error {
	// GitHub review thread IDs are GraphQL node IDs (base64-encoded strings)
	if threadID == "" {
		return fmt.Errorf("thread ID cannot be empty")
	}
	// Node IDs are typically at least 20+ characters
	if len(threadID) < 10 {
		return fmt.Errorf("invalid thread ID format: too short")
	}
	return nil
}

type resolveReviewThreadMutation struct {
	ResolveReviewThread struct {
		Thread struct {
			ID string
		}
	} `graphql:"resolveReviewThread(input: $input)"`
}

type unresolveReviewThreadMutation struct {
	UnresolveReviewThread struct {
		Thread struct {
			ID string
		}
	} `graphql:"unresolveReviewThread(input: $input)"`
}

type resolveReviewThreadInput struct {
	ThreadID githubv4.ID `json:"threadId"`
}

func resolveReviewThread(ctx context.Context, client *githubv4.Client, threadID string) error {
	var mutation resolveReviewThreadMutation
	input := resolveReviewThreadInput{
		ThreadID: githubv4.ID(threadID),
	}
	err := client.Mutate(ctx, &mutation, input, nil)
	if err != nil {
		return fmt.Errorf("resolveReviewThread mutation failed: %w", err)
	}
	return nil
}

func unresolveReviewThread(ctx context.Context, client *githubv4.Client, threadID string) error {
	var mutation unresolveReviewThreadMutation
	input := resolveReviewThreadInput{
		ThreadID: githubv4.ID(threadID),
	}
	err := client.Mutate(ctx, &mutation, input, nil)
	if err != nil {
		return fmt.Errorf("unresolveReviewThread mutation failed: %w", err)
	}
	return nil
}

var _ platform.ThreadResolver = gitHubClientProvider{}

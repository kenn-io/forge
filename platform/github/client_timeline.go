package github

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"time"

	gh "github.com/google/go-github/v93/github"
)

func (c *Client) ListPullRequestTimelineEvents(
	ctx context.Context, owner, repo string, number int,
) ([]PullRequestTimelineEvent, error) {
	type graphQLResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				PullRequest *struct {
					TimelineItems struct {
						Nodes []struct {
							TypeName string `json:"__typename"`
							ID       string `json:"id"`
							Actor    *struct {
								Login string `json:"login"`
							} `json:"actor"`
							Assignee *struct {
								TypeName string `json:"__typename"`
								Login    string `json:"login"`
							} `json:"assignee"`
							BeforeCommit *struct {
								OID string `json:"oid"`
							} `json:"beforeCommit"`
							AfterCommit *struct {
								OID string `json:"oid"`
							} `json:"afterCommit"`
							CreatedAt time.Time `json:"createdAt"`
							Ref       *struct {
								Name string `json:"name"`
							} `json:"ref"`
							DeletedCommentAuthor *struct {
								Login string `json:"login"`
							} `json:"deletedCommentAuthor"`
							PreviousTitle   string `json:"previousTitle"`
							CurrentTitle    string `json:"currentTitle"`
							PreviousRefName string `json:"previousRefName"`
							CurrentRefName  string `json:"currentRefName"`
							Source          *struct {
								TypeName   string `json:"__typename"`
								Number     int    `json:"number"`
								Title      string `json:"title"`
								URL        string `json:"url"`
								Repository *struct {
									Owner *struct {
										Login string `json:"login"`
									} `json:"owner"`
									Name string `json:"name"`
								} `json:"repository"`
							} `json:"source"`
							IsCrossRepository bool `json:"isCrossRepository"`
							WillCloseTarget   bool `json:"willCloseTarget"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool    `json:"hasNextPage"`
							EndCursor   *string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"timelineItems"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}

	var events []PullRequestTimelineEvent
	var cursor *string
	for {
		payload, err := json.Marshal(graphQLRequest{
			Query: pullRequestTimelineEventsQuery,
			Variables: map[string]any{
				"owner":  owner,
				"repo":   repo,
				"number": number,
				"cursor": cursor,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal pull request timeline query: %w", err)
		}

		req, err := http.NewRequestWithContext(
			c.graphQLContext(ctx),
			http.MethodPost,
			c.graphQLEndpoint,
			bytes.NewReader(payload),
		)
		if err != nil {
			return nil, fmt.Errorf("create pull request timeline request: %w", err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf(
				"list pull request timeline events for %s/%s#%d: %w",
				owner, repo, number, err,
			)
		}
		c.trackGraphQLRateHeaders(resp)
		if resp.StatusCode != http.StatusOK {
			responseErr := gh.CheckResponse(resp)
			_ = resp.Body.Close()
			if responseErr != nil {
				return nil, fmt.Errorf(
					"list pull request timeline events for %s/%s#%d: %w",
					owner, repo, number, responseErr,
				)
			}
			return nil, fmt.Errorf(
				"list pull request timeline events for %s/%s#%d: graphql status %s",
				owner, repo, number, resp.Status,
			)
		}

		var decoded graphQLResponse
		if err := json.UnmarshalRead(resp.Body, &decoded); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf(
				"decode pull request timeline events for %s/%s#%d: %w",
				owner, repo, number, err,
			)
		}
		_ = resp.Body.Close()

		if len(decoded.Errors) > 0 {
			return nil, fmt.Errorf(
				"list pull request timeline events for %s/%s#%d: graphql errors: %s",
				owner, repo, number, joinGraphQLErrorMessages(decoded.Errors),
			)
		}

		if decoded.Data.Repository == nil {
			return nil, fmt.Errorf(
				"list pull request timeline events for %s/%s#%d: missing repository in graphql response",
				owner, repo, number,
			)
		}
		if decoded.Data.Repository.PullRequest == nil {
			return nil, fmt.Errorf(
				"list pull request timeline events for %s/%s#%d: missing pull request in graphql response",
				owner, repo, number,
			)
		}

		for _, node := range decoded.Data.Repository.PullRequest.TimelineItems.Nodes {
			event := PullRequestTimelineEvent{
				NodeID:            node.ID,
				CreatedAt:         node.CreatedAt,
				PreviousTitle:     node.PreviousTitle,
				CurrentTitle:      node.CurrentTitle,
				PreviousRefName:   node.PreviousRefName,
				CurrentRefName:    node.CurrentRefName,
				IsCrossRepository: node.IsCrossRepository,
				WillCloseTarget:   node.WillCloseTarget,
			}
			switch node.TypeName {
			case "HeadRefForcePushedEvent":
				event.EventType = "force_push"
			case "CommentDeletedEvent":
				event.EventType = "comment_deleted"
			case "CrossReferencedEvent":
				event.EventType = "cross_referenced"
			case "RenamedTitleEvent":
				event.EventType = "renamed_title"
			case "BaseRefChangedEvent":
				event.EventType = "base_ref_changed"
			case "AssignedEvent":
				event.EventType = "assigned"
			case "UnassignedEvent":
				event.EventType = "unassigned"
			case "MergedEvent":
				event.EventType = "merged"
			case "ClosedEvent":
				event.EventType = "closed"
			case "ReopenedEvent":
				event.EventType = "reopened"
			default:
				continue
			}
			if node.Actor != nil {
				event.Actor = node.Actor.Login
			}
			if node.Assignee != nil {
				event.Assignee = node.Assignee.Login
			}
			if node.BeforeCommit != nil {
				event.BeforeSHA = node.BeforeCommit.OID
			}
			if node.AfterCommit != nil {
				event.AfterSHA = node.AfterCommit.OID
			}
			if node.Ref != nil {
				event.Ref = node.Ref.Name
			}
			if node.DeletedCommentAuthor != nil {
				event.DeletedCommentAuthor = node.DeletedCommentAuthor.Login
			}
			if node.Source != nil {
				event.SourceType = node.Source.TypeName
				event.SourceNumber = node.Source.Number
				event.SourceTitle = node.Source.Title
				event.SourceURL = node.Source.URL
				if node.Source.Repository != nil {
					event.SourceRepo = node.Source.Repository.Name
					if node.Source.Repository.Owner != nil {
						event.SourceOwner = node.Source.Repository.Owner.Login
					}
				}
			}
			events = append(events, event)
		}

		pageInfo := decoded.Data.Repository.PullRequest.TimelineItems.PageInfo
		if !pageInfo.HasNextPage {
			break
		}
		cursor = pageInfo.EndCursor
	}

	return events, nil
}

func (c *Client) ListIssueTimelineEvents(
	ctx context.Context, owner, repo string, number int,
) ([]PullRequestTimelineEvent, error) {
	type graphQLResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				Issue *struct {
					TimelineItems struct {
						Nodes []struct {
							TypeName string `json:"__typename"`
							ID       string `json:"id"`
							Actor    *struct {
								Login string `json:"login"`
							} `json:"actor"`
							Assignee *struct {
								TypeName string `json:"__typename"`
								Login    string `json:"login"`
							} `json:"assignee"`
							CreatedAt time.Time `json:"createdAt"`
							Source    *struct {
								TypeName   string `json:"__typename"`
								Number     int    `json:"number"`
								Title      string `json:"title"`
								URL        string `json:"url"`
								Repository *struct {
									Owner *struct {
										Login string `json:"login"`
									} `json:"owner"`
									Name string `json:"name"`
								} `json:"repository"`
							} `json:"source"`
							IsCrossRepository bool `json:"isCrossRepository"`
							WillCloseTarget   bool `json:"willCloseTarget"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool    `json:"hasNextPage"`
							EndCursor   *string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"timelineItems"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}

	var events []PullRequestTimelineEvent
	var cursor *string
	for {
		payload, err := json.Marshal(graphQLRequest{
			Query: issueTimelineEventsQuery,
			Variables: map[string]any{
				"owner":  owner,
				"repo":   repo,
				"number": number,
				"cursor": cursor,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal issue timeline query: %w", err)
		}

		req, err := http.NewRequestWithContext(
			c.graphQLContext(ctx),
			http.MethodPost,
			c.graphQLEndpoint,
			bytes.NewReader(payload),
		)
		if err != nil {
			return nil, fmt.Errorf("create issue timeline request: %w", err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf(
				"list issue timeline events for %s/%s#%d: %w",
				owner, repo, number, err,
			)
		}
		c.trackGraphQLRateHeaders(resp)
		if resp.StatusCode != http.StatusOK {
			responseErr := gh.CheckResponse(resp)
			_ = resp.Body.Close()
			if responseErr != nil {
				return nil, fmt.Errorf(
					"list issue timeline events for %s/%s#%d: %w",
					owner, repo, number, responseErr,
				)
			}
			return nil, fmt.Errorf(
				"list issue timeline events for %s/%s#%d: graphql status %s",
				owner, repo, number, resp.Status,
			)
		}

		var decoded graphQLResponse
		if err := json.UnmarshalRead(resp.Body, &decoded); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf(
				"decode issue timeline events for %s/%s#%d: %w",
				owner, repo, number, err,
			)
		}
		_ = resp.Body.Close()

		if len(decoded.Errors) > 0 {
			return nil, fmt.Errorf(
				"list issue timeline events for %s/%s#%d: graphql errors: %s",
				owner, repo, number, joinGraphQLErrorMessages(decoded.Errors),
			)
		}
		if decoded.Data.Repository == nil {
			return nil, fmt.Errorf(
				"list issue timeline events for %s/%s#%d: missing repository in graphql response",
				owner, repo, number,
			)
		}
		if decoded.Data.Repository.Issue == nil {
			return nil, fmt.Errorf(
				"list issue timeline events for %s/%s#%d: missing issue in graphql response",
				owner, repo, number,
			)
		}

		for _, node := range decoded.Data.Repository.Issue.TimelineItems.Nodes {
			event := PullRequestTimelineEvent{
				NodeID:    node.ID,
				CreatedAt: node.CreatedAt,
			}
			switch node.TypeName {
			case "AssignedEvent":
				event.EventType = "assigned"
			case "UnassignedEvent":
				event.EventType = "unassigned"
			case "CrossReferencedEvent":
				event.EventType = "cross_referenced"
			case "ClosedEvent":
				event.EventType = "closed"
			case "ReopenedEvent":
				event.EventType = "reopened"
			default:
				continue
			}
			if node.Actor != nil {
				event.Actor = node.Actor.Login
			}
			if node.Assignee != nil {
				event.Assignee = node.Assignee.Login
			}
			if node.Source != nil {
				event.SourceType = node.Source.TypeName
				event.SourceNumber = node.Source.Number
				event.SourceTitle = node.Source.Title
				event.SourceURL = node.Source.URL
				if node.Source.Repository != nil {
					event.SourceRepo = node.Source.Repository.Name
					if node.Source.Repository.Owner != nil {
						event.SourceOwner = node.Source.Repository.Owner.Login
					}
				}
			}
			event.IsCrossRepository = node.IsCrossRepository
			event.WillCloseTarget = node.WillCloseTarget
			events = append(events, event)
		}

		pageInfo := decoded.Data.Repository.Issue.TimelineItems.PageInfo
		if !pageInfo.HasNextPage {
			break
		}
		cursor = pageInfo.EndCursor
	}

	return events, nil
}

func (c *Client) ListForcePushEvents(
	ctx context.Context, owner, repo string, number int,
) ([]ForcePushEvent, error) {
	timelineEvents, err := c.ListPullRequestTimelineEvents(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}

	events := make([]ForcePushEvent, 0, len(timelineEvents))
	for _, timelineEvent := range timelineEvents {
		if timelineEvent.EventType != "force_push" {
			continue
		}
		events = append(events, ForcePushEvent{
			Actor:     timelineEvent.Actor,
			BeforeSHA: timelineEvent.BeforeSHA,
			AfterSHA:  timelineEvent.AfterSHA,
			Ref:       timelineEvent.Ref,
			CreatedAt: timelineEvent.CreatedAt,
		})
	}
	return events, nil
}

package github

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const pullRequestTimelineEventsQuery = `
query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      timelineItems(itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT, COMMENT_DELETED_EVENT, CROSS_REFERENCED_EVENT, RENAMED_TITLE_EVENT, BASE_REF_CHANGED_EVENT, ASSIGNED_EVENT, UNASSIGNED_EVENT, MERGED_EVENT, CLOSED_EVENT, REOPENED_EVENT], first: 100, after: $cursor) {
        nodes {
          __typename
          ... on Node {
            id
          }
          ... on HeadRefForcePushedEvent {
            actor { login }
            beforeCommit { oid }
            afterCommit { oid }
            createdAt
            ref { name }
          }
          ... on CommentDeletedEvent {
            actor { login }
            createdAt
            deletedCommentAuthor { login }
          }
          ... on CrossReferencedEvent {
            actor { login }
            createdAt
            isCrossRepository
            willCloseTarget
            source {
              __typename
              ... on Issue {
                number
                title
                url
                repository {
                  owner { login }
                  name
                }
              }
              ... on PullRequest {
                number
                title
                url
                repository {
                  owner { login }
                  name
                }
              }
            }
          }
          ... on RenamedTitleEvent {
            actor { login }
            createdAt
            previousTitle
            currentTitle
          }
          ... on BaseRefChangedEvent {
            actor { login }
            createdAt
            previousRefName
            currentRefName
          }
          ... on AssignedEvent {
            actor { login }
            assignee {
              __typename
              ... on Bot { login }
              ... on Mannequin { login }
              ... on Organization { login }
              ... on User { login }
            }
            createdAt
          }
          ... on UnassignedEvent {
            actor { login }
            assignee {
              __typename
              ... on Bot { login }
              ... on Mannequin { login }
              ... on Organization { login }
              ... on User { login }
            }
            createdAt
          }
          ... on MergedEvent {
            actor { login }
            createdAt
          }
          ... on ClosedEvent {
            actor { login }
            createdAt
          }
          ... on ReopenedEvent {
            actor { login }
            createdAt
          }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

const issueTimelineEventsQuery = `
query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    issue(number: $number) {
      timelineItems(itemTypes: [ASSIGNED_EVENT, UNASSIGNED_EVENT, CROSS_REFERENCED_EVENT, CLOSED_EVENT, REOPENED_EVENT], first: 100, after: $cursor) {
        nodes {
          __typename
          ... on Node {
            id
          }
          ... on CrossReferencedEvent {
            actor { login }
            createdAt
            isCrossRepository
            willCloseTarget
            source {
              __typename
              ... on Issue {
                number
                title
                url
                repository {
                  owner { login }
                  name
                }
              }
              ... on PullRequest {
                number
                title
                url
                repository {
                  owner { login }
                  name
                }
              }
            }
          }
          ... on AssignedEvent {
            actor { login }
            assignee {
              __typename
              ... on Bot { login }
              ... on Mannequin { login }
              ... on Organization { login }
              ... on User { login }
            }
            createdAt
          }
          ... on UnassignedEvent {
            actor { login }
            assignee {
              __typename
              ... on Bot { login }
              ... on Mannequin { login }
              ... on Organization { login }
              ... on User { login }
            }
            createdAt
          }
          ... on ClosedEvent {
            actor { login }
            createdAt
          }
          ... on ReopenedEvent {
            actor { login }
            createdAt
          }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

const readyForReviewIDQuery = `
query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      id
    }
  }
}`

const readyForReviewMutation = `
mutation($pullRequestId: ID!) {
  markPullRequestReadyForReview(input: {pullRequestId: $pullRequestId}) {
    pullRequest {
      databaseId
      number
      title
      state
      isDraft
      locked
      body
      url
      authorAssociation
      author {
        login
      }
      createdAt
      updatedAt
      mergedAt
      closedAt
      additions
      deletions
      mergeable
      mergeStateStatus
      reviewDecision
      headRefName
      baseRefName
      headRefOid
      baseRefOid
      headRepository {
        url
      }
      labels(first: 100) {
        nodes {
          name
          color
          description
          isDefault
        }
      }
    }
  }
}`

const convertToDraftMutation = `
mutation($pullRequestId: ID!) {
  convertPullRequestToDraft(input: {pullRequestId: $pullRequestId}) {
    pullRequest {
      id
      updatedAt
    }
  }
}`

const pullRequestReviewThreadCommentsQuery = `
query($threadID: ID!, $cursor: String) {
  node(id: $threadID) {
    ... on PullRequestReviewThread {
      comments(first: 100, after: $cursor) {
        nodes {
          id
          databaseId
          fullDatabaseId
          body
          path
          line
          originalLine
          subjectType
          diffHunk
          url
          author { login }
          authorAssociation
          commit { oid }
          originalCommit { oid }
          pullRequestReview { databaseId }
          isMinimized
          minimizedReason
          createdAt
          updatedAt
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphQLError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type graphQLReviewThreadComment struct {
	AuthorAssociation *string      `json:"authorAssociation"`
	NodeID            string       `json:"id"`
	DatabaseID        GraphQLInt64 `json:"databaseId"`
	FullDatabaseID    GraphQLInt64 `json:"fullDatabaseId"`
	Body              string       `json:"body"`
	Path              string       `json:"path"`
	Line              int          `json:"line"`
	OriginalLine      int          `json:"originalLine"`
	SubjectType       string       `json:"subjectType"`
	DiffHunk          string       `json:"diffHunk"`
	URL               string       `json:"url"`
	Author            *struct {
		Login string `json:"login"`
	} `json:"author"`
	Commit *struct {
		OID string `json:"oid"`
	} `json:"commit"`
	OriginalCommit *struct {
		OID string `json:"oid"`
	} `json:"originalCommit"`
	PullRequestReview *struct {
		DatabaseID int64 `json:"databaseId"`
	} `json:"pullRequestReview"`
	IsMinimized     bool      `json:"isMinimized"`
	MinimizedReason string    `json:"minimizedReason"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type GraphQLInt64 int64

func (value *GraphQLInt64) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "" || text == "null" {
		*value = 0
		return nil
	}
	if strings.HasPrefix(text, `"`) {
		unquoted, err := strconv.Unquote(text)
		if err != nil {
			return fmt.Errorf("decode GraphQL int64: %w", err)
		}
		text = unquoted
		if text == "" {
			*value = 0
			return nil
		}
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("decode GraphQL int64 %q: %w", text, err)
	}
	*value = GraphQLInt64(parsed)
	return nil
}

type graphQLReviewThreadCommentConnection struct {
	Nodes    []graphQLReviewThreadComment `json:"nodes"`
	PageInfo struct {
		HasNextPage bool    `json:"hasNextPage"`
		EndCursor   *string `json:"endCursor"`
	} `json:"pageInfo"`
}

type readyForReviewError struct {
	err        error
	statusCode int
	staleState bool
}

func (e *readyForReviewError) Error() string { return e.err.Error() }

func (e *readyForReviewError) Unwrap() error { return e.err }

func (e *readyForReviewError) StatusCode() int { return e.statusCode }

func (e *readyForReviewError) IsStaleState() bool { return e.staleState }

func newReadyForReviewError(err error, statusCode int, staleState bool) error {
	return &readyForReviewError{
		err:        err,
		statusCode: statusCode,
		staleState: staleState,
	}
}

func readyForReviewGraphQLErrorMeta(graphQLErrors []graphQLError) (int, bool) {
	for _, graphQLError := range graphQLErrors {
		if strings.EqualFold(graphQLError.Type, "NOT_FOUND") {
			return http.StatusNotFound, true
		}
		if strings.Contains(graphQLError.Message, "Could not resolve to a PullRequest") ||
			strings.Contains(graphQLError.Message, "Could not resolve to a node with the global id") {
			return http.StatusNotFound, true
		}
	}
	return 0, false
}

func joinGraphQLErrorMessages(graphQLErrors []graphQLError) string {
	messages := make([]string, 0, len(graphQLErrors))
	for _, graphQLError := range graphQLErrors {
		if graphQLError.Message != "" {
			messages = append(messages, graphQLError.Message)
		}
	}
	if len(messages) == 0 {
		return "unknown GraphQL error"
	}
	return strings.Join(messages, "; ")
}

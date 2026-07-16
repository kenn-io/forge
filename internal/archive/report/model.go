package report

import (
	"fmt"
	"time"
)

const (
	MaxDetailedRecords   = 10_000
	MaxDetailedTextBytes = 32 * 1024 * 1024
)

type ActivityKind string

const (
	ActivityIssue               ActivityKind = "issue"
	ActivityMergeRequest        ActivityKind = "merge_request"
	ActivityOrdinaryComment     ActivityKind = "ordinary_comment"
	ActivityReview              ActivityKind = "review"
	ActivityInlineReviewComment ActivityKind = "inline_review_comment"
)

type RepositoryRef struct {
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	RepoPath     string `json:"repo_path"`
}

type Counts struct {
	IssuesOpened         int `json:"issues_opened"`
	MergeRequestsOpened  int `json:"merge_requests_opened"`
	OrdinaryComments     int `json:"ordinary_comments"`
	ReviewsSubmitted     int `json:"reviews_submitted"`
	InlineReviewComments int `json:"inline_review_comments"`
}

func (c Counts) TotalActivity() int {
	return c.IssuesOpened + c.MergeRequestsOpened + c.OrdinaryComments +
		c.ReviewsSubmitted + c.InlineReviewComments
}

type Coverage struct {
	Status                 string     `json:"status"`
	ActivePhases           []string   `json:"active_phases"`
	CollectionMode         string     `json:"collection_mode"`
	OperatorState          string     `json:"operator_state"`
	Comments               string     `json:"comments"`
	Reviews                string     `json:"reviews"`
	InlineComments         string     `json:"inline_comments"`
	InitialCompletedAt     *time.Time `json:"initial_completed_at,omitempty"`
	MaintenanceSucceededAt *time.Time `json:"maintenance_succeeded_at,omitempty"`
	BudgetWaitUntil        *time.Time `json:"budget_wait_until,omitempty"`
	ArchivedItems          int        `json:"archived_items"`
	UnsupportedItems       int        `json:"unsupported_items"`
	InaccessibleItems      int        `json:"inaccessible_items"`
}

type Repository struct {
	Repository RepositoryRef `json:"repository"`
	Coverage   Coverage      `json:"coverage"`
	Counts     Counts        `json:"counts"`
}

type Contributor struct {
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	Login        string `json:"login"`
	Counts       Counts `json:"counts"`
}

type Activity struct {
	Repository         RepositoryRef `json:"repository"`
	Kind               ActivityKind  `json:"kind"`
	ItemNumber         int           `json:"item_number"`
	ProviderExternalID string        `json:"provider_external_id"`
	Title              string        `json:"title"`
	Author             string        `json:"author"`
	OccurredAt         time.Time     `json:"occurred_at"`
	Body               string        `json:"body"`
	URL                string        `json:"url"`
}

type Model struct {
	Start        time.Time     `json:"start"`
	End          time.Time     `json:"end"`
	Repositories []Repository  `json:"repositories"`
	Totals       Counts        `json:"totals"`
	Contributors []Contributor `json:"contributors"`
	Activity     []Activity    `json:"activity,omitempty"`
}

type LimitError struct {
	ObservedRecords   int
	MaxRecords        int
	ObservedTextBytes int64
	MaxTextBytes      int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf(
		"archive report exceeds detailed limits: %d records (max %d), %d text bytes (max %d)",
		e.ObservedRecords, e.MaxRecords, e.ObservedTextBytes, e.MaxTextBytes,
	)
}

func ValidateDetailedSize(records int, textBytes int64) error {
	if records <= MaxDetailedRecords && textBytes <= MaxDetailedTextBytes {
		return nil
	}
	return &LimitError{
		ObservedRecords: records, MaxRecords: MaxDetailedRecords,
		ObservedTextBytes: textBytes, MaxTextBytes: MaxDetailedTextBytes,
	}
}

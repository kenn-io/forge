package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	gh "github.com/google/go-github/v93/github"
)

// trackRate records the request and updates rate limit state
// from the response. Safe to call with nil response or nil
// tracker.
func (c *Client) trackRate(resp *gh.Response) {
	if resp == nil || c.rateTracker == nil {
		return
	}
	c.rateTracker.RecordRequest()
	c.rateTracker.UpdateFromRate(RateFromGitHub(resp.Rate))
}

// trackWriteRate records a write-path response against the mutation
// credential's tracker, so PAT exhaustion disables writes without
// pausing app-token reads. Hosts without a write tracker share one
// credential across reads and writes; their write responses feed the
// shared tracker, same as before the split existed.
func (c *Client) trackWriteRate(resp *gh.Response) {
	if c.writeRateTracker == nil {
		c.trackRate(resp)
		return
	}
	if resp == nil {
		return
	}
	c.writeRateTracker.RecordRequest()
	c.writeRateTracker.UpdateFromRate(RateFromGitHub(resp.Rate))
}

// trackNotificationRate records notification API responses only when the
// read client and notification client use the same credential. In GitHub App
// split-auth mode, reads use an installation token while notifications use
// the user's PAT, so PAT headers cannot update the installation read tracker.
func (c *Client) trackNotificationRate(resp *gh.Response) {
	if c.notificationRateTracker != nil {
		if resp == nil {
			return
		}
		c.notificationRateTracker.RecordRequest()
		c.notificationRateTracker.UpdateFromRate(RateFromGitHub(resp.Rate))
		return
	}
	if c.splitAuthActive() {
		return
	}
	c.trackRate(resp)
}

func (c *Client) GetRateLimitSnapshot(ctx context.Context) (*RateLimitSnapshot, error) {
	limits, resp, err := c.gh.RateLimit.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("get GitHub rate limit snapshot: %w", err)
	}
	if limits == nil {
		return &RateLimitSnapshot{}, nil
	}
	snapshot := &RateLimitSnapshot{}
	if limits.Core != nil {
		rate := RateFromGitHub(*limits.Core)
		snapshot.Core = &rate
	}
	// GitHub can return an optimistic core body while the same response's
	// headers report exhausted quota. Preserve the enforced window.
	if resp != nil && resp.Header.Get("X-RateLimit-Resource") == "core" &&
		resp.Rate.Limit > 0 && !resp.Rate.Reset.IsZero() {
		rate := RateFromGitHub(resp.Rate)
		snapshot.Core = &rate
	}
	if limits.GraphQL != nil {
		rate := RateFromGitHub(*limits.GraphQL)
		snapshot.GraphQL = &rate
	}
	return snapshot, nil
}

func (c *Client) trackGraphQLRateHeaders(resp *http.Response) {
	if resp == nil || c.graphQLRateTracker == nil {
		return
	}
	c.graphQLRateTracker.RecordRequest()
	remaining, err := parseRateHeaderInt(resp.Header, "X-RateLimit-Remaining")
	if err != nil {
		return
	}
	limit, err := parseRateHeaderInt(resp.Header, "X-RateLimit-Limit")
	if err != nil {
		return
	}
	resetUnix, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return
	}
	c.graphQLRateTracker.UpdateFromRate(RateFromHeaders(
		limit, remaining, time.Unix(resetUnix, 0).UTC(),
	))
}

// trackWriteGraphQLRateHeaders records a GraphQL mutation response
// against the write credential's GraphQL tracker. Hosts without one
// share a single credential, so the shared GraphQL tracker absorbs
// the response instead.
func (c *Client) trackWriteGraphQLRateHeaders(resp *http.Response) {
	tracker := c.writeGraphQLRateTracker
	if tracker == nil {
		c.trackGraphQLRateHeaders(resp)
		return
	}
	if resp == nil {
		return
	}
	tracker.RecordRequest()
	remaining, err := parseRateHeaderInt(resp.Header, "X-RateLimit-Remaining")
	if err != nil {
		return
	}
	limit, err := parseRateHeaderInt(resp.Header, "X-RateLimit-Limit")
	if err != nil {
		return
	}
	resetUnix, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return
	}
	tracker.UpdateFromRate(RateFromHeaders(
		limit, remaining, time.Unix(resetUnix, 0).UTC(),
	))
}

func parseRateHeaderInt(header http.Header, name string) (int, error) {
	value, err := strconv.ParseInt(header.Get(name), 10, strconv.IntSize)
	if err != nil {
		return 0, err
	}
	return int(value), nil
}

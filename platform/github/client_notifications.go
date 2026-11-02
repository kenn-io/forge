package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/platform"
)

func (c *Client) ListNotifications(ctx context.Context, opts platform.NotificationListOptions) ([]platform.NotificationThread, bool, error) {
	page := max(opts.Page, 1)
	ghOpts := &gh.NotificationListOptions{
		All:           opts.All,
		Participating: opts.Participating,
		Page:          page, PerPage: 100,
	}
	if opts.Since != nil {
		ghOpts.Since = opts.Since.UTC()
	}
	var notifications []*gh.Notification
	var resp *gh.Response
	var err error
	if opts.RepoOwner != "" && opts.RepoName != "" {
		notifications, resp, err = c.notificationGH().Activity.ListRepositoryNotifications(ctx, opts.RepoOwner, opts.RepoName, ghOpts)
	} else {
		notifications, resp, err = c.notificationGH().Activity.ListNotifications(ctx, ghOpts)
	}
	c.trackNotificationRate(resp)
	if err != nil {
		return nil, false, err
	}
	threads := make([]platform.NotificationThread, 0, len(notifications))
	for _, notification := range notifications {
		threads = append(threads, c.normalizeNotification(notification))
	}
	return threads, resp != nil && resp.NextPage != 0, nil
}

// NotificationPage is one page of the authenticated user's notifications
// across every repository. NotModified reports a 304 for a conditional
// request; LastModified is the validator GitHub returned.
type NotificationPage struct {
	Threads      []platform.NotificationThread
	HasNext      bool
	LastModified string
	NotModified  bool
}

// NotificationPageAPI lists the authenticated user's notifications across
// every repository. GitHub scopes Last-Modified to the user's whole
// notification set rather than to the URL, and a 304 costs no rate limit, so
// one conditional request proves nothing changed anywhere.
type NotificationPageAPI interface {
	ListNotificationPage(
		ctx context.Context, opts platform.NotificationListOptions, ifModifiedSince string,
	) (NotificationPage, error)
}

var _ NotificationPageAPI = (*Client)(nil)

func (c *Client) ListNotificationPage(
	ctx context.Context, opts platform.NotificationListOptions, ifModifiedSince string,
) (NotificationPage, error) {
	query := url.Values{}
	query.Set("all", strconv.FormatBool(opts.All))
	query.Set("participating", strconv.FormatBool(opts.Participating))
	if opts.Since != nil {
		query.Set("since", opts.Since.UTC().Format(time.RFC3339))
	}
	query.Set("page", strconv.Itoa(max(opts.Page, 1)))
	query.Set("per_page", "100")
	req, err := c.notificationGH().NewRequest(
		ctx, http.MethodGet, "notifications?"+query.Encode(), nil,
	)
	if err != nil {
		return NotificationPage{}, err
	}
	if ifModifiedSince != "" {
		req.Header.Set("If-Modified-Since", ifModifiedSince)
	}
	var notifications []*gh.Notification
	resp, err := c.notificationGH().Do(req, &notifications)
	c.trackNotificationRate(resp)
	if err != nil {
		if IsNotModified(err) {
			return NotificationPage{NotModified: true, LastModified: ifModifiedSince}, nil
		}
		return NotificationPage{}, err
	}
	page := NotificationPage{
		Threads: make([]platform.NotificationThread, 0, len(notifications)),
		HasNext: resp.NextPage != 0,
	}
	if resp.Response != nil {
		page.LastModified = resp.Header.Get("Last-Modified")
	}
	for _, notification := range notifications {
		page.Threads = append(page.Threads, c.normalizeNotification(notification))
	}
	return page, nil
}

func (c *Client) GetNotificationThread(ctx context.Context, threadID string) (platform.NotificationThread, error) {
	notification, resp, err := c.notificationGH().Activity.GetThread(ctx, threadID)
	c.trackNotificationRate(resp)
	if err != nil {
		return platform.NotificationThread{}, err
	}
	return c.normalizeNotification(notification), nil
}

func (c *Client) MarkNotificationThreadRead(ctx context.Context, threadID string) error {
	resp, err := c.notificationGH().Activity.MarkThreadRead(ctx, threadID)
	c.trackNotificationRate(resp)
	return err
}

func (c *Client) normalizeNotification(n *gh.Notification) platform.NotificationThread {
	if n == nil {
		return platform.NotificationThread{}
	}
	thread := platform.NotificationThread{
		ID:     n.GetID(),
		Reason: n.GetReason(),
		Unread: n.GetUnread(),
	}
	if updated := n.GetUpdatedAt(); !updated.IsZero() {
		thread.UpdatedAt = updated.UTC()
	}
	if lastRead := n.GetLastReadAt(); !lastRead.IsZero() {
		lastReadAt := lastRead.UTC()
		thread.LastReadAt = &lastReadAt
	}
	if repo := n.GetRepository(); repo != nil {
		thread.RepoName = strings.ToLower(repo.GetName())
		if owner := repo.GetOwner(); owner != nil {
			thread.RepoOwner = strings.ToLower(owner.GetLogin())
		}
	}
	if subject := n.GetSubject(); subject != nil {
		thread.SubjectType = subject.GetType()
		thread.SubjectTitle = subject.GetTitle()
		thread.SubjectURL = subject.GetURL()
		thread.SubjectLatestCommentURL = subject.GetLatestCommentURL()
		thread.ItemType, thread.ItemNumber, thread.WebURL = c.notificationItem(subject.GetType(), subject.GetURL(), thread.RepoOwner, thread.RepoName)
	}
	return thread
}

func (c *Client) notificationItem(subjectType, apiURL, owner, repo string) (string, *int, string) {
	itemType := "other"
	subjectLower := strings.ToLower(subjectType)
	switch subjectLower {
	case "pullrequest":
		itemType = "pr"
	case "issue":
		itemType = "issue"
	case "release":
		itemType = "release"
	case "commit":
		itemType = "commit"
	}

	if owner == "" || repo == "" || apiURL == "" {
		return itemType, nil, ""
	}
	segments := strings.Split(strings.TrimRight(apiURL, "/"), "/")
	lastSegment := func(prefix string) (string, bool) {
		for i := range len(segments) - 1 {
			if segments[i] == prefix {
				return segments[i+1], true
			}
		}
		return "", false
	}
	host := c.platformHost
	if host == "" {
		host = "github.com"
	}
	switch itemType {
	case "pr":
		if value, ok := lastSegment("pulls"); ok {
			if number, err := strconv.Atoi(value); err == nil {
				return itemType, &number, fmt.Sprintf("https://%s/%s/%s/pull/%d", host, owner, repo, number)
			}
		}
		if value, ok := lastSegment("issues"); ok {
			if number, err := strconv.Atoi(value); err == nil {
				return itemType, &number, fmt.Sprintf("https://%s/%s/%s/pull/%d", host, owner, repo, number)
			}
		}
	case "issue":
		if value, ok := lastSegment("issues"); ok {
			if number, err := strconv.Atoi(value); err == nil {
				return itemType, &number, fmt.Sprintf("https://%s/%s/%s/issues/%d", host, owner, repo, number)
			}
		}
	case "commit":
		if sha, ok := lastSegment("commits"); ok && sha != "" {
			return itemType, nil, fmt.Sprintf("https://%s/%s/%s/commit/%s", host, owner, repo, sha)
		}
	case "release":
		return itemType, nil, ""
	}
	return itemType, nil, ""
}

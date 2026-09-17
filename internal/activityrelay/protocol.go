// Package activityrelay implements a minimal GitHub refresh-hint feed.
package activityrelay

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const (
	PullRequest       = "pull_request"
	PullRequestChecks = "pull_request_checks"
	Issue             = "issue"
	RepositoryRefs    = "repository_refs"
	Repository        = "repository"
)

type Hint struct {
	Provider     string `json:"provider"`
	Host         string `json:"host"`
	RepositoryID string `json:"repository_id"`
	Target       string `json:"target"`
	Number       int    `json:"number,omitempty"`
}

func (h Hint) Validate() error {
	if h.Provider != "github" || h.Host != "github.com" || len(h.RepositoryID) == 0 || len(h.RepositoryID) > 256 {
		return errors.New("invalid relay repository identity")
	}
	switch h.Target {
	case PullRequest, PullRequestChecks, Issue:
		if h.Number > 0 {
			return nil
		}
	case RepositoryRefs, Repository:
		if h.Number == 0 {
			return nil
		}
	}
	return errors.New("invalid relay refresh target")
}

type Event struct {
	Hint
	Cursor string `json:"cursor"`
}

type Page struct {
	Events         []Event `json:"events"`
	NextCursor     string  `json:"next_cursor,omitempty"`
	HasMore        bool    `json:"has_more"`
	ResyncRequired bool    `json:"resync_required,omitempty"`
	Code           string  `json:"code,omitempty"`
	ResetCursor    string  `json:"reset_cursor,omitempty"`
}

// Fetch treats cursors as opaque and checks the page before a consumer saves it.
func Fetch(ctx context.Context, client *http.Client, baseURL, after string) (Page, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return Page{}, fmt.Errorf("parse relay URL: %w", err)
	}
	endpoint.Path = "/activity"
	endpoint.RawQuery = url.Values{"after": {after}, "limit": {"100"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Page{}, fmt.Errorf("create relay request: %w", err)
	}
	response, err := client.Do(req)
	if err != nil {
		return Page{}, fmt.Errorf("read relay: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusGone {
		return Page{}, fmt.Errorf("relay returned HTTP %d", response.StatusCode)
	}
	const maxPageBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPageBytes+1))
	if err != nil {
		return Page{}, fmt.Errorf("read relay response: %w", err)
	}
	var page Page
	if len(body) > maxPageBytes || json.Unmarshal(body, &page) != nil {
		return Page{}, errors.New("invalid relay response body")
	}
	if response.StatusCode == http.StatusGone {
		if page.Code != "cursor_expired" || page.ResetCursor == "" || len(page.Events) != 0 || page.HasMore {
			return Page{}, errors.New("invalid relay reset response")
		}
		return page, nil
	}
	if page.Code != "" || page.ResetCursor != "" || page.NextCursor == "" || len(page.Events) > 100 {
		return Page{}, errors.New("invalid relay page")
	}
	if page.ResyncRequired {
		if after != "" || len(page.Events) != 0 || page.HasMore {
			return Page{}, errors.New("invalid relay initial checkpoint")
		}
		return page, nil
	}
	if after == "" || (len(page.Events) == 0 && (page.HasMore || page.NextCursor != after)) {
		return Page{}, errors.New("relay page did not preserve its checkpoint")
	}
	previous := after
	for _, event := range page.Events {
		if err := event.Validate(); err != nil {
			return Page{}, err
		}
		if event.Cursor == "" || event.Cursor == previous {
			return Page{}, errors.New("relay event did not advance its cursor")
		}
		previous = event.Cursor
	}
	if previous != page.NextCursor {
		return Page{}, errors.New("relay checkpoint does not match its last event")
	}
	return page, nil
}

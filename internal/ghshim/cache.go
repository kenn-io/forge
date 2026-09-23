package ghshim

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	gh "github.com/google/go-github/v91/github"
	"golang.org/x/sync/singleflight"
)

// PullClient is the existing provider boundary; hydration uses Forge's routed
// read credentials and quota accounting rather than the invoking tool's token.
type PullClient interface {
	GetPullRequest(context.Context, string, string, int) (*gh.PullRequest, error)
	ListPullRequestsPage(context.Context, string, string, string, int) ([]*gh.PullRequest, bool, error)
}

type entry struct {
	pulls []*gh.PullRequest
	until time.Time
}
type Cache struct {
	mu      sync.Mutex
	entries map[string]entry
	flights singleflight.Group
}

func (c *Cache) Query(ctx context.Context, client PullClient, identity string, q Query) ([]byte, error) {
	// Cache complete lists, not filtered branch results: tools querying different
	// branches share one hydration. Never treat Forge's partial archive as complete.
	state := q.State
	if state == "merged" {
		state = "closed"
	}
	key := fmt.Sprintf("%s/%s/%s/%s/%d", identity, q.Owner, q.Repo, state, q.Number)
	result, err, _ := c.flights.Do(key, func() (any, error) {
		c.mu.Lock()
		cached, ok := c.entries[key]
		c.mu.Unlock()
		if ok && time.Now().Before(cached.until) {
			return cached.pulls, nil
		}
		var pulls []*gh.PullRequest
		if q.Command == "view" {
			pr, err := client.GetPullRequest(ctx, q.Owner, q.Repo, q.Number)
			if err != nil {
				return nil, err
			}
			if pr == nil {
				return nil, fmt.Errorf("missing pull request")
			}
			pulls = []*gh.PullRequest{pr}
		} else {
			seen := make(map[int]bool)
			for page := 1; ; page++ {
				if page > 100 {
					return nil, fmt.Errorf("pull request list exceeds cache limit")
				}
				batch, more, err := client.ListPullRequestsPage(ctx, q.Owner, q.Repo, state, page)
				if err != nil {
					return nil, err
				}
				for _, pr := range batch {
					if !seen[pr.GetNumber()] {
						pulls = append(pulls, pr)
						seen[pr.GetNumber()] = true
					}
				}
				if !more {
					break
				}
			}
			sort.SliceStable(pulls, func(i, j int) bool { return pulls[i].GetCreatedAt().After(pulls[j].GetCreatedAt().Time) })
		}
		c.mu.Lock()
		if c.entries == nil {
			c.entries = make(map[string]entry)
		}
		for k, v := range c.entries {
			if time.Now().After(v.until) {
				delete(c.entries, k)
			}
		}
		c.entries[key] = entry{pulls: pulls, until: time.Now().Add(time.Minute)}
		c.mu.Unlock()
		return pulls, nil
	})
	if err != nil {
		return nil, err
	}
	pulls := result.([]*gh.PullRequest)
	selected := make([]*gh.PullRequest, 0)
	for _, pr := range pulls {
		if q.Command == "list" && ((q.Head != "" && pr.GetHead().GetRef() != q.Head) || (q.Base != "" && pr.GetBase().GetRef() != q.Base) || (q.State == "merged" && pr.MergedAt == nil)) {
			continue
		}
		selected = append(selected, pr)
		if len(selected) == q.Limit {
			break
		}
	}
	return Encode(q, selected)
}

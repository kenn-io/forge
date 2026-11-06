package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCandidateHintsRecoverExcludedItems(t *testing.T) {
	for _, tt := range []struct {
		name, kind, state string
		draft, missing    bool
		limit             int
		codes             []string
		retry             map[string]any
	}{
		{name: "draft", kind: "pr", state: "open", draft: true, codes: []string{"excluded_drafts"}, retry: map[string]any{"include_drafts": true}},
		{name: "closed PR", kind: "pr", state: "closed", codes: []string{"excluded_closed"}, retry: map[string]any{"include_closed": true}},
		{name: "merged PR", kind: "pr", state: "merged", codes: []string{"excluded_closed"}, retry: map[string]any{"include_closed": true}},
		{name: "closed issue", kind: "issue", state: "closed", codes: []string{"excluded_closed"}, retry: map[string]any{"include_closed": true}},
		{name: "closed draft", kind: "pr", state: "closed", draft: true, codes: []string{"excluded_drafts", "excluded_closed"}, retry: map[string]any{"include_drafts": true, "include_closed": true}},
		{name: "exclusions before limit", kind: "pr", state: "closed", draft: true, limit: 500, codes: []string{"excluded_drafts", "excluded_closed", "limit_lowered"}, retry: map[string]any{"include_drafts": true, "include_closed": true}},
		{name: "missing cache", kind: "pr", state: "closed", draft: true, missing: true},
		{name: "open PR", kind: "pr", state: "open"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			backend := &fakeBackend{
				listActivityFn: func(context.Context, ActivityQuery) (ActivityPage, error) {
					row := ActivityItem{Repository: testRepository(), ItemType: tt.kind, ItemNumber: 7}
					return ActivityPage{Items: []ActivityItem{row, row}}, nil
				},
				listPullsFn: func(context.Context, ItemListQuery) ([]Pull, error) {
					if tt.missing {
						return nil, nil
					}
					return []Pull{{Repository: testRepository(), Number: 7, State: tt.state, IsDraft: tt.draft}}, nil
				},
				listIssuesFn: func(context.Context, ItemListQuery) ([]Issue, error) {
					return []Issue{{Repository: testRepository(), Number: 7, State: tt.state}}, nil
				},
			}
			client := connectMCPTestSession(t, newMCPTestServer(t, backend))
			out := callHintTool[findCandidatesOutput](t, client, "kenn_forge_find_review_candidates", map[string]any{"limit": tt.limit})
			var codes []string
			for _, hint := range out.Hints {
				codes = append(codes, hint.Code)
				if hint.Code == "limit_lowered" {
					assert.Empty(hint.RetryWith)
					continue
				}
				assert.Equal(tt.retry, hint.RetryWith)
				assert.Contains(hint.Message, "1 ")
			}
			assert.Equal(tt.codes, codes)
			if len(tt.codes) > 0 {
				assert.Empty(out.Candidates)
				retried := callHintTool[findCandidatesOutput](t, client, "kenn_forge_find_review_candidates", out.Hints[0].RetryWith)
				require.Len(t, retried.Candidates, 1)
				assert.Equal(7, retried.Candidates[0].Item.Number)
				assert.Empty(retried.Hints)
			} else if !tt.missing {
				assert.Len(out.Candidates, 1)
			}
		})
	}
}

func TestSearchHintsExplainEmptyResults(t *testing.T) {
	for _, tt := range []struct {
		name, query, state, kind  string
		hidden, probeFails, found bool
		limit                     int
		codes                     []string
	}{
		{name: "multi term miss", query: "retry budget", codes: []string{"every_word_required"}},
		{name: "merged match", query: "retry", kind: "pr", hidden: true, codes: []string{"excluded_closed"}},
		{name: "closed issue match", query: "retry", kind: "issue", hidden: true, codes: []string{"excluded_closed"}},
		{name: "state explains multi term miss", query: "retry budget", kind: "pr", hidden: true, codes: []string{"excluded_closed"}},
		{name: "state guidance first", query: "retry budget", kind: "pr", hidden: true, limit: 500, codes: []string{"excluded_closed", "limit_lowered"}},
		{name: "quoted phrase", query: `"retry budget"`},
		{name: "excluded words", query: "retry NOT budget !flaky"},
		{name: "explicit state", query: "retry budget", state: "open", codes: []string{"every_word_required"}},
		{name: "probe failure", query: "retry budget", probeFails: true, codes: []string{"every_word_required"}},
		{name: "nonempty", query: "retry budget", found: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			var queries []ItemListQuery
			var retrying bool
			checkQuery := func(q ItemListQuery) error {
				queries = append(queries, q)
				assert.Equal(tt.query, q.Text)
				assert.Equal(testRepository(), q.Repository)
				if q.State == "all" {
					if retrying {
						assert.Equal(26, q.Limit)
					} else {
						assert.Equal(2, q.Limit)
					}
					if tt.probeFails {
						return errors.New("diagnostic read failed")
					}
				} else {
					assert.Equal("open", q.State)
					if tt.limit > 100 {
						assert.Equal(101, q.Limit)
					} else {
						assert.Equal(26, q.Limit)
					}
				}
				return nil
			}
			backend := &fakeBackend{
				listPullsFn: func(_ context.Context, q ItemListQuery) ([]Pull, error) {
					if err := checkQuery(q); err != nil {
						return nil, err
					}
					if tt.found || tt.hidden && q.State == "all" && tt.kind == "pr" {
						return []Pull{{Repository: testRepository(), Number: 7, State: "merged"}}, nil
					}
					return nil, nil
				},
				listIssuesFn: func(_ context.Context, q ItemListQuery) ([]Issue, error) {
					if err := checkQuery(q); err != nil {
						return nil, err
					}
					if tt.hidden && q.State == "all" && tt.kind == "issue" {
						return []Issue{{Repository: testRepository(), Number: 7, State: "closed"}}, nil
					}
					return nil, nil
				},
			}
			client := connectMCPTestSession(t, newMCPTestServer(t, backend))
			args := map[string]any{"query": tt.query, "repo": repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"}}
			if tt.limit != 0 {
				args["limit"] = tt.limit
			}
			if tt.state != "" {
				args["state"] = tt.state
			}
			if tt.kind != "" {
				args["item_types"] = []string{tt.kind}
			}
			out := callHintTool[searchItemsOutput](t, client, "kenn_forge_search_items", args)
			var codes []string
			for _, hint := range out.Hints {
				codes = append(codes, hint.Code)
				if hint.Code == "excluded_closed" {
					assert.Equal(map[string]any{"state": "all"}, hint.RetryWith)
					retry := map[string]any{"query": tt.query, "repo": repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"}, "state": "all", "item_types": []string{tt.kind}}
					retrying = true
					retried := callHintTool[searchItemsOutput](t, client, "kenn_forge_search_items", retry)
					assert.Len(retried.Results, 1)
					assert.Empty(retried.Hints)
				} else {
					assert.Empty(hint.RetryWith)
				}
			}
			assert.Equal(tt.codes, codes)
			if tt.state != "" || tt.found {
				for _, q := range queries {
					assert.Equal("open", q.State)
				}
			}
		})
	}
}

func TestLimitHintsReportEveryLoweredLimit(t *testing.T) {
	for _, tt := range []struct {
		tool    string
		maximum int
		args    map[string]any
	}{
		{"kenn_forge_list_activity", 200, map[string]any{}},
		{"kenn_forge_search_items", 100, map[string]any{"query": "retry"}},
		{"kenn_forge_find_review_candidates", 100, map[string]any{}},
		{"kenn_forge_list_pull_contexts", 100, map[string]any{"repo": repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"}}},
	} {
		t.Run(tt.tool, func(t *testing.T) {
			assert := assert.New(t)
			client := connectMCPTestSession(t, newMCPTestServer(t, &fakeBackend{}))
			for _, limit := range []int{0, tt.maximum, 500} {
				tt.args["limit"] = limit
				out := callHintTool[struct {
					Hints []resultHint `json:"hints"`
				}](t, client, tt.tool, tt.args)
				if limit <= tt.maximum {
					assert.Empty(out.Hints)
				} else {
					require.Len(t, out.Hints, 1)
					assert.Equal("limit_lowered", out.Hints[0].Code)
					assert.Contains(out.Hints[0].Message, "500")
					assert.Contains(out.Hints[0].Message, map[int]string{100: "100", 200: "200"}[tt.maximum])
					assert.Empty(out.Hints[0].RetryWith)
				}
			}
		})
	}
}

func TestEventLimitHintsReportOnlyRequestedEvents(t *testing.T) {
	for _, kind := range []string{"pr", "issue", "bulk"} {
		t.Run(kind, func(t *testing.T) {
			for _, tt := range []struct {
				name    string
				include *bool
				limit   int
			}{
				{name: "default events", limit: 500},
				{name: "requested events", include: new(true), limit: 500},
				{name: "omitted events", include: new(false), limit: 500},
				{name: "at maximum", include: new(true), limit: 100},
				{name: "default limit", include: new(true)},
			} {
				t.Run(tt.name, func(t *testing.T) {
					assert := assert.New(t)
					pull := Pull{Repository: testRepository(), Number: 7, State: "open"}
					events := []DetailEvent{{EventType: "comment"}}
					backend := &fakeBackend{
						listPullsFn: func(context.Context, ItemListQuery) ([]Pull, error) { return []Pull{pull}, nil },
						getPullFn: func(context.Context, ItemIdentity) (PullDetail, error) {
							return PullDetail{Pull: &pull, Events: events}, nil
						},
						getIssueFn: func(context.Context, ItemIdentity) (IssueDetail, error) {
							return IssueDetail{Issue: &Issue{Repository: testRepository(), Number: 7, State: "open"}, Events: events}, nil
						},
					}
					client := connectMCPTestSession(t, newMCPTestServer(t, backend))
					args := map[string]any{"event_limit": tt.limit}
					if tt.include != nil {
						args["include_events"] = *tt.include
					}
					tool := "kenn_forge_get_item_context"
					repo := repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"}
					include := tt.include == nil || *tt.include
					if kind == "bulk" {
						tool = "kenn_forge_list_pull_contexts"
						args["repo"] = repo
						include = tt.include != nil && *tt.include
					} else {
						args["item"] = itemRefInput{Type: kind, Number: 7, Repository: &repo}
					}
					out := callHintTool[struct {
						Hints  []resultHint           `json:"hints"`
						Events []contextEvent         `json:"events"`
						Items  []getItemContextOutput `json:"items"`
					}](t, client, tool, args)
					if include && tt.limit > 100 {
						assert.Equal([]resultHint{{Code: "limit_lowered", Message: "Requested event_limit 500 was lowered to 100"}}, out.Hints)
					} else {
						assert.Empty(out.Hints)
					}
					gotEvents := out.Events
					if kind == "bulk" {
						require.Len(t, out.Items, 1)
						assert.Empty(out.Items[0].Hints)
						gotEvents = out.Items[0].Events
					}
					if include {
						assert.Len(gotEvents, 1)
					} else {
						assert.Empty(gotEvents)
					}
				})
			}
		})
	}
}

// Compare both client-visible copies while exercising Forge's hint decisions.
func callHintTool[T any](t *testing.T, client *mcp.ClientSession, name string, args any) T {
	t.Helper()
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.False(t, result.IsError, "%v", result.Content)
	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.JSONEq(t, string(data), result.Content[0].(*mcp.TextContent).Text)
	var out T
	require.NoError(t, json.Unmarshal(data, &out))
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &fields))
	if hints, ok := fields["hints"]; ok {
		assert.NotEqual(t, "[]", string(hints))
		assert.NotEqual(t, "null", string(hints))
	}
	return out
}

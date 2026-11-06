package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCandidateHintsRecoverExcludedItems(t *testing.T) {
	for _, tt := range []struct {
		name, kind, state                string
		draft, missing, capped, separate bool
		limit                            int
		codes                            []string
		retry                            map[string]any
	}{
		{name: "draft", kind: "pr", state: "open", draft: true, codes: []string{"excluded_drafts"}, retry: map[string]any{"include_drafts": true}},
		{name: "closed PR", kind: "pr", state: "closed", codes: []string{"excluded_closed"}, retry: map[string]any{"include_closed": true}},
		{name: "merged PR", kind: "pr", state: "merged", codes: []string{"excluded_closed"}, retry: map[string]any{"include_closed": true}},
		{name: "closed issue", kind: "issue", state: "closed", codes: []string{"excluded_closed"}, retry: map[string]any{"include_closed": true}},
		{name: "capped closed draft before limit", kind: "pr", state: "closed", draft: true, capped: true, limit: 500, codes: []string{"excluded_drafts", "excluded_closed", "limit_lowered"}, retry: map[string]any{"include_drafts": true, "include_closed": true}},
		{name: "separate draft and closed PRs", kind: "pr", state: "open", draft: true, separate: true, codes: []string{"excluded_drafts", "excluded_closed"}, retry: map[string]any{"include_drafts": true}},
		{name: "missing cache", kind: "pr", state: "closed", draft: true, missing: true},
		{name: "open PR", kind: "pr", state: "open"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			backend := &fakeBackend{
				listActivityFn: func(context.Context, ActivityQuery) (ActivityPage, error) {
					row := ActivityItem{Repository: testRepository(), ItemType: tt.kind, ItemNumber: 7}
					rows := []ActivityItem{row, row}
					if tt.separate {
						rows = append(rows, ActivityItem{Repository: testRepository(), ItemType: "pr", ItemNumber: 8})
					}
					return ActivityPage{Items: rows, Capped: tt.capped}, nil
				},
				listPullsFn: func(context.Context, ItemListQuery) ([]Pull, error) {
					if tt.missing {
						return nil, nil
					}
					rows := []Pull{{Repository: testRepository(), Number: 7, State: tt.state, IsDraft: tt.draft}}
					if tt.separate {
						rows = append(rows, Pull{Repository: testRepository(), Number: 8, State: "closed"})
					}
					return rows, nil
				},
				listIssuesFn: func(context.Context, ItemListQuery) ([]Issue, error) {
					return []Issue{{Repository: testRepository(), Number: 7, State: tt.state}}, nil
				},
			}
			client := connectMCPTestSession(t, newMCPTestServer(t, backend))
			out := callHintTool[findCandidatesOutput](t, client, "kenn_forge_find_review_candidates", map[string]any{"limit": tt.limit})
			var codes []string
			if len(tt.codes) > 0 {
				assert.Empty(out.Candidates)
			}
			for _, hint := range out.Hints {
				codes = append(codes, hint.Code)
				if hint.Code == "limit_lowered" {
					continue
				}
				retry, number := tt.retry, 7
				if tt.separate && hint.Code == "excluded_closed" {
					retry, number = map[string]any{"include_closed": true}, 8
				}
				assert.Equal(retry, hint.RetryWith)
				message := map[string]string{"excluded_drafts": "1 draft PRs were left out", "excluded_closed": "1 closed or merged items were left out"}[hint.Code]
				if tt.capped {
					message = "At least " + message
				}
				if tt.draft && tt.state == "closed" {
					message += map[string]string{"excluded_drafts": ". This count includes closed drafts.", "excluded_closed": ". This count includes draft PRs."}[hint.Code]
				}
				assert.Equal(message, hint.Message)
				retried := callHintTool[findCandidatesOutput](t, client, "kenn_forge_find_review_candidates", hint.RetryWith)
				require.Len(t, retried.Candidates, 1)
				assert.Equal(number, retried.Candidates[0].Item.Number)
				assert.Empty(retried.Hints)
			}
			assert.Equal(tt.codes, codes)
			if len(tt.codes) == 0 && !tt.missing {
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
		{name: "closed issue match", query: "retry", kind: "issue", hidden: true, codes: []string{"excluded_closed"}},
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
			checkQuery := func(q ItemListQuery) error {
				queries = append(queries, q)
				assert.Equal(tt.query, q.Text)
				assert.Equal(testRepository(), q.Repository)
				if q.State == "all" {
					if tt.probeFails {
						return errors.New("diagnostic read failed")
					}
				} else {
					assert.Equal("open", q.State)
				}
				return nil
			}
			backend := &fakeBackend{
				listPullsFn: func(_ context.Context, q ItemListQuery) ([]Pull, error) {
					if err := checkQuery(q); err != nil {
						return nil, err
					}
					if tt.found || tt.hidden && q.State == "all" && tt.kind == "pr" {
						state := "merged"
						if q.State == "open" {
							state = "open"
						}
						return []Pull{{Repository: testRepository(), Number: 7, State: state}}, nil
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
			for _, q := range queries {
				if q.State == "all" {
					assert.Equal(2, q.Limit)
				}
			}
			var codes []string
			for _, hint := range out.Hints {
				codes = append(codes, hint.Code)
				if hint.Code == "excluded_closed" {
					assert.Equal(map[string]any{"state": "all"}, hint.RetryWith)
					retry := maps.Clone(args)
					maps.Copy(retry, hint.RetryWith)
					retried := callHintTool[searchItemsOutput](t, client, "kenn_forge_search_items", retry)
					assert.Len(retried.Results, 1)
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
					assert.Equal([]resultHint{{Code: "limit_lowered", Message: fmt.Sprintf("Requested limit 500 was lowered to %d", tt.maximum)}}, out.Hints)
				}
			}
		})
	}
}

func TestEventLimitHintsReportOnlyRequestedEvents(t *testing.T) {
	for _, kind := range []string{"pr", "issue", "bulk"} {
		t.Run(kind, func(t *testing.T) {
			for _, include := range []bool{true, false} {
				t.Run(fmt.Sprintf("include_events=%t", include), func(t *testing.T) {
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
					args := map[string]any{"event_limit": 500, "include_events": include}
					tool := "kenn_forge_get_item_context"
					repo := repoFilterInput{Provider: "github", PlatformRepoID: 1001, Owner: "acme", Name: "widget"}
					if kind == "bulk" {
						tool = "kenn_forge_list_pull_contexts"
						args["repo"] = repo
					} else {
						args["item"] = itemRefInput{Type: kind, Number: 7, Repository: &repo}
					}
					out := callHintTool[struct {
						Hints  []resultHint           `json:"hints"`
						Events []contextEvent         `json:"events"`
						Items  []getItemContextOutput `json:"items"`
					}](t, client, tool, args)

					gotEvents := out.Events
					if kind == "bulk" {
						require.Len(t, out.Items, 1)
						assert.Empty(out.Items[0].Hints)
						gotEvents = out.Items[0].Events
					}
					if include {
						assert.Equal([]resultHint{{Code: "limit_lowered", Message: "Requested event_limit 500 was lowered to 100"}}, out.Hints)
						assert.Len(gotEvents, 1)
					} else {
						assert.Empty(out.Hints)
						assert.Empty(gotEvents)
					}
				})
			}
		})
	}
}

// Check that empty hint fields are omitted from tool output.
func callHintTool[T any](t *testing.T, client *mcp.ClientSession, name string, args any) T {
	t.Helper()
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.False(t, result.IsError, "%v", result.Content)
	data, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
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

package github

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
)

func TestDequeuePullRequest(t *testing.T) {
	const queued = `{"data":{"repository":{"pullRequest":{"id":"PR_node","mergeQueueEntry":{"id":"MQE_node"}}}}}`
	for _, tt := range []struct {
		name      string
		entry     string
		response  string
		wantCalls int
		wantError string
		// refused means GitHub left the pull request queued; otherwise the
		// outcome is unknown.
		refused bool
	}{
		{name: "accepted", entry: queued, response: `{"data":{"dequeuePullRequest":{"mergeQueueEntry":{"id":"MQE_node"}}}}`, wantCalls: 2},
		{
			name:      "not queued",
			entry:     `{"data":{"repository":{"pullRequest":{"id":"PR_node","mergeQueueEntry":null}}}}`,
			wantCalls: 1,
		},
		{
			name: "forbidden", entry: queued, wantCalls: 2,
			response:  `{"data":{"dequeuePullRequest":null},"errors":[{"type":"FORBIDDEN","message":"Cannot dequeue"}]}`,
			wantError: "Cannot dequeue", refused: true,
		},
		{
			name: "execution error", entry: queued, wantCalls: 2,
			response:  `{"data":{"dequeuePullRequest":null},"errors":[{"message":"Something went wrong while executing your query."}]}`,
			wantError: "Something went wrong",
		},
		{name: "unconfirmed", entry: queued, wantCalls: 2, response: `{"data":{"dequeuePullRequest":null}}`, wantError: "does not confirm"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			c := newSplitCredentialClient(t, func(token string, w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "merge-user", token)
				calls++
				var req graphQLRequest
				if err := json.UnmarshalRead(r.Body, &req); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if calls == 1 {
					assert.Contains(t, req.Query, "mergeQueueEntry")
					assert.Equal(t, map[string]any{"owner": "acme", "repo": "widget", "number": float64(7)}, req.Variables)
					_, _ = io.WriteString(w, tt.entry)
					return
				}
				assert.Contains(t, req.Query, "dequeuePullRequest(input: {id: $id})")
				assert.Equal(t, map[string]any{"id": "PR_node"}, req.Variables)
				_, _ = io.WriteString(w, tt.response)
			})
			err := c.DequeuePullRequest(t.Context(), "acme", "widget", 7)
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
				assert.Equal(t, tt.refused, errors.Is(err, &platform.Error{Code: platform.ErrCodeConflict}))
			}
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

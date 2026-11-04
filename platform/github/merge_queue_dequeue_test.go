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
	const node = `{"data":{"repository":{"pullRequest":{"id":"PR_node"}}}}`
	for _, tt := range []struct {
		name      string
		node      string
		response  string
		wantCalls int
		wantError string
		// refused means GitHub left the pull request queued; otherwise the
		// outcome is unknown.
		refused bool
	}{
		{name: "accepted", node: node, response: `{"data":{"dequeuePullRequest":{"mergeQueueEntry":{"id":"MQE_node"}}}}`, wantCalls: 2},
		{
			// The answer GitHub gave for a pull request outside the queue, observed live.
			name: "not queued", node: node, wantCalls: 2,
			response:  `{"data":{"dequeuePullRequest":null},"errors":[{"type":"UNPROCESSABLE","message":"Failed to remove PR #7"}]}`,
			wantError: "Failed to remove PR #7", refused: true,
		},
		{
			name: "forbidden", node: node, wantCalls: 2,
			response:  `{"data":{"dequeuePullRequest":null},"errors":[{"type":"FORBIDDEN","message":"Cannot dequeue"}]}`,
			wantError: "Cannot dequeue", refused: true,
		},
		{
			name: "execution error", node: node, wantCalls: 2,
			response:  `{"data":{"dequeuePullRequest":null},"errors":[{"message":"Something went wrong while executing your query."}]}`,
			wantError: "Something went wrong",
		},
		{name: "unconfirmed", node: node, wantCalls: 2, response: `{"data":{"dequeuePullRequest":null}}`, wantError: "does not confirm"},
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
					assert.Contains(t, req.Query, "pullRequest(number: $number)")
					assert.Equal(t, map[string]any{"owner": "acme", "repo": "widget", "number": float64(7)}, req.Variables)
					_, _ = io.WriteString(w, tt.node)
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

package generated_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/worklistapi"
)

// The server writes null for an item that is no longer a visible synced row.
// The generated client must keep that distinct from a populated row.
func TestGeneratedWorklistItemKeepsNullRowsDistinct(t *testing.T) {
	gone := worklistapi.WorklistItemResponse{
		ItemType: "pr", Number: 7, Freshness: "gone", WrittenAt: "2026-01-02T03:04:05Z",
	}
	live := worklistapi.WorklistItemStateResponse{State: "open", HeadSHA: "abc123", CIStatus: "success"}
	visiblePull := worklistapi.WorklistItemResponse{
		ItemType: "pr", Number: 42, Freshness: "fresh", WrittenAt: "2026-01-02T03:04:05Z",
		Live: &live,
		Pull: &pullapi.MergeRequestResponse{
			Number: 42, Title: "Add widget", State: "open", RepoOwner: "acme", RepoName: "widget",
		},
	}
	visibleIssue := worklistapi.WorklistItemResponse{
		ItemType: "issue", Number: 5, Freshness: "fresh", WrittenAt: "2026-01-02T03:04:05Z",
		Live:  &live,
		Issue: &issueapi.IssueResponse{Number: 5, Title: "Widget crashes", State: "open"},
	}
	payload, err := json.Marshal([]worklistapi.WorklistItemResponse{gone, visiblePull, visibleIssue})
	require.NoError(t, err)
	require.Contains(t, string(payload), `"pull":null`, "the server writes null rows")

	var decoded []generated.WorklistItemResponse
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Len(t, decoded, 3)

	assert.Nil(t, decoded[0].Live)
	assert.Nil(t, decoded[0].Pull)
	assert.Nil(t, decoded[0].Issue)

	require.NotNil(t, decoded[1].Live)
	assert.Equal(t, "abc123", decoded[1].Live.HeadSha)
	require.NotNil(t, decoded[1].Pull)
	assert.Equal(t, "Add widget", decoded[1].Pull.Title)
	assert.Nil(t, decoded[1].Issue)

	require.NotNil(t, decoded[2].Issue)
	assert.Equal(t, "Widget crashes", decoded[2].Issue.Title)
	assert.Nil(t, decoded[2].Pull)
}

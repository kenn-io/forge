package mcpserver

import (
	"context"
	_ "embed"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const guidanceResourceURI = "kenn-forge://mcp/guidance"

//go:embed guidance.md
var guidanceMarkdown string

const reviewCandidatesPrompt = `Use kenn-forge's cached data to triage review work.

Call kenn_forge_list_repos first to learn valid repo filters and sync freshness.
Use kenn_forge_find_review_candidates for recent PR and issue activity.
Inspect details only for plausible items with kenn_forge_get_item_context.
Use kenn_forge_get_item_diff to check the size and shape of a change before claiming it; request the full diff file only when the summary is not enough.
Consult kenn_forge_get_stack_context before claiming a stacked PR so review order respects the stack.
Prefer cached evidence over assumptions, and report stale cache signals or uncertainty.
Never perform provider writes.
Set workflow state only when the reason is clear.
Include expected_status when marking an item, so you do not overwrite humans or other agents; use force: true only for a deliberate unconditional local override.
Treat awaiting_merge as a PR-oriented state and avoid setting it on issues unless the user explicitly asks for that state.`

func (s *Server) registerGuidance() {
	s.mcp.AddResource(&mcp.Resource{
		URI:         guidanceResourceURI,
		Name:        "kenn-forge-mcp-guidance",
		Title:       "Kenn Forge MCP Guidance",
		Description: "Recommended model workflow for using Kenn Forge MCP tools.",
		MIMEType:    "text/markdown",
	}, readGuidanceResource)
	s.mcp.AddPrompt(&mcp.Prompt{
		Name:        "kenn-forge-review-candidates",
		Title:       "Review Candidates",
		Description: "Periodic review triage workflow for cached kenn-forge PR and issue activity.",
	}, reviewCandidatesPromptHandler)
}

func readGuidanceResource(
	_ context.Context,
	req *mcp.ReadResourceRequest,
) (*mcp.ReadResourceResult, error) {
	if req.Params.URI != guidanceResourceURI {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      guidanceResourceURI,
			MIMEType: "text/markdown",
			Text:     guidanceMarkdown,
		}},
	}, nil
}

func reviewCandidatesPromptHandler(
	context.Context,
	*mcp.GetPromptRequest,
) (*mcp.GetPromptResult, error) {
	return &mcp.GetPromptResult{
		Description: "Periodic review triage workflow for cached kenn-forge PR and issue activity.",
		Messages: []*mcp.PromptMessage{{
			Role:    mcp.Role("user"),
			Content: &mcp.TextContent{Text: reviewCandidatesPrompt},
		}},
	}, nil
}

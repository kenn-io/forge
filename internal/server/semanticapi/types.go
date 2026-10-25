package semanticapi

import (
	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
)

type SemanticSearchResponse struct {
	Ready bool                         `json:"ready" doc:"False while the index is still embedding; results then cover only the documents embedded so far."`
	Items []SemanticSearchItemResponse `json:"items" nullable:"false"`
}

// SemanticSearchItemResponse is one pull request or issue, ranked by the
// closest of its description and comments. Pull or Issue carries the same
// row the pull request or issue list returns.
type SemanticSearchItemResponse struct {
	Repo     httpapi.RepoRefResponse       `json:"repo"`
	ItemType string                        `json:"item_type" enum:"pr,issue"`
	Number   int                           `json:"number"`
	Title    string                        `json:"title"`
	URL      string                        `json:"url"`
	Score    float32                       `json:"score" doc:"Cosine similarity between the query and the matched text; higher is closer. Compare scores only within one response."`
	Match    SemanticSearchMatchResponse   `json:"match"`
	Pull     *pullapi.MergeRequestResponse `json:"pull" doc:"Pull request list row; null for an issue."`
	Issue    *issueapi.IssueResponse       `json:"issue" doc:"Issue list row; null for a pull request."`
}

// TransformSchema marks the row reference that does not apply to an item's
// type as nullable. Huma rejects a nullable tag on a referenced object, so
// the null branch is declared here.
func (*SemanticSearchItemResponse) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	for _, name := range []string{"pull", "issue"} {
		property := schema.Properties[name]
		if property == nil || property.Ref == "" {
			continue
		}
		schema.Properties[name] = &huma.Schema{
			Description: property.Description,
			AnyOf:       []*huma.Schema{{Ref: property.Ref}, {Type: "null"}},
		}
	}
	return schema
}

// SemanticSearchMatchResponse names the text that ranked an item.
type SemanticSearchMatchResponse struct {
	Kind      string `json:"kind" enum:"description,comment" doc:"description is the item's title and body; comment is one of its comments or reviews."`
	CommentID int64  `json:"comment_id,omitempty" doc:"Forge's ID for the matched comment; omitted for a description match."`
	Excerpt   string `json:"excerpt" doc:"Start of the matched passage, whitespace collapsed."`
}

type SemanticSearchStatusResponse struct {
	Enabled       bool    `json:"enabled" doc:"Whether an [embeddings] endpoint is configured on the provider owner."`
	Model         string  `json:"model"`
	Ready         bool    `json:"ready" doc:"Every document is embedded or skipped for the configured model."`
	Embedded      int64   `json:"embedded" doc:"Documents searchable under the configured model."`
	Skipped       int64   `json:"skipped" doc:"Documents with nothing to embed, or that the endpoint refused."`
	Backlog       int64   `json:"backlog" doc:"Documents still waiting to be embedded."`
	LastSuccessAt *string `json:"last_success_at" format:"date-time"`
	LastError     string  `json:"last_error"`
	LastErrorAt   *string `json:"last_error_at" format:"date-time"`
}

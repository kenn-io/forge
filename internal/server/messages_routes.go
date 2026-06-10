package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/middleman/internal/messages"
)

type messagesSavedSearchesBody struct {
	Searches []messages.SavedSearch `json:"searches"`
	ETag     string                 `json:"etag"`
}

type messagesSavedSearchesOutput struct {
	ETag string `header:"ETag"`
	Body messagesSavedSearchesBody
}

type replaceMessagesSavedSearchesInput struct {
	IfMatch       string `header:"If-Match"`
	MiddlemanCSRF string `header:"X-Middleman-Csrf" required:"true"`
	Body          struct {
		Searches []json.RawMessage `json:"searches" required:"true" nullable:"false"`
	}
}

func (s *Server) registerMessagesAPI(api huma.API) {
	huma.Get(api, "/messages/saved-searches", s.msgvault.savedSearches,
		documentOperation("list-messages-saved-searches", "List messages saved searches", "Messages"))
	huma.Put(api, "/messages/saved-searches", s.msgvault.replaceSavedSearches,
		documentOperation("replace-messages-saved-searches", "Replace messages saved searches", "Messages"))
}

func (h *msgvaultHandler) savedSearches(context.Context, *struct{}) (*messagesSavedSearchesOutput, error) {
	list, err := messages.LoadSavedSearches()
	if err != nil {
		return nil, problemInternal("saved searches file is unreadable")
	}
	list = messages.CanonicalizeSavedSearches(list)
	if list == nil {
		list = []messages.SavedSearch{}
	}
	etag := messages.SavedSearchesETag(list)
	return &messagesSavedSearchesOutput{
		ETag: etag,
		Body: messagesSavedSearchesBody{Searches: list, ETag: etag},
	}, nil
}

func (h *msgvaultHandler) replaceSavedSearches(
	_ context.Context,
	in *replaceMessagesSavedSearchesInput,
) (*messagesSavedSearchesOutput, error) {
	parsed := make([]messages.SavedSearch, 0, len(in.Body.Searches))
	for _, rawEntry := range in.Body.Searches {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawEntry, &fields); err != nil {
			continue
		}
		rawQuery, ok := fields["query"]
		if !ok {
			continue
		}
		var query string
		if err := json.Unmarshal(rawQuery, &query); err != nil {
			continue
		}
		name := ""
		if rawName, ok := fields["name"]; ok {
			_ = json.Unmarshal(rawName, &name)
		}
		parsed = append(parsed, messages.SavedSearch{Name: name, Query: query})
	}

	h.savedSearchesMu.Lock()
	defer h.savedSearchesMu.Unlock()

	if in.IfMatch != "" {
		current, err := messages.LoadSavedSearches()
		if err != nil {
			return nil, problemInternal("saved searches file is unreadable")
		}
		current = messages.CanonicalizeSavedSearches(current)
		if messages.SavedSearchesETag(current) != in.IfMatch {
			return nil, newProblem(
				http.StatusPreconditionFailed,
				CodeConflict,
				"saved searches have changed since last load; refetch and retry",
				map[string]any{"reason": "stale_etag"},
			)
		}
	}

	canonical := messages.CanonicalizeSavedSearches(parsed)
	if canonical == nil {
		canonical = []messages.SavedSearch{}
	}
	if err := messages.SaveSavedSearches(canonical); err != nil {
		return nil, problemInternal("saved searches could not be written")
	}
	etag := messages.SavedSearchesETag(canonical)
	return &messagesSavedSearchesOutput{
		ETag: etag,
		Body: messagesSavedSearchesBody{Searches: canonical, ETag: etag},
	}, nil
}

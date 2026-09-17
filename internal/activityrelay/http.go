package activityrelay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

type Source struct {
	Secret        []byte
	RepositoryIDs []int64
}

func Handlers(store *Store, sources map[string]Source) (http.Handler, http.Handler) {
	public, private := http.NewServeMux(), http.NewServeMux()
	config := huma.DefaultConfig("Forge activity relay", "1")
	config.OpenAPIPath, config.DocsPath, config.SchemasPath = "", "", ""
	config.CreateHooks = nil
	ingressAPI := humago.New(public, config)
	feedConfig := config
	feedConfig.OpenAPI = &huma.OpenAPI{Info: &huma.Info{Title: "Forge activity feed", Version: "1"}}
	feedAPI := humago.New(private, feedConfig)

	type deliveryInput struct {
		Source    string `path:"source"`
		Event     string `header:"X-GitHub-Event"`
		Signature string `header:"X-Hub-Signature-256"`
		RawBody   []byte
	}
	huma.Register(ingressAPI, huma.Operation{
		OperationID: "receive-github-activity", Method: http.MethodPost,
		Path: "/webhooks/github/{source}", MaxBodyBytes: 25 << 20,
	}, func(ctx context.Context, input *deliveryInput) (*struct{}, error) {
		source, found := sources[input.Source]
		if !found {
			return nil, huma.Error404NotFound("unknown webhook source")
		}
		signature, err := hex.DecodeString(strings.TrimPrefix(input.Signature, "sha256="))
		mac := hmac.New(sha256.New, source.Secret)
		_, _ = mac.Write(input.RawBody)
		if len(source.Secret) == 0 || !strings.HasPrefix(input.Signature, "sha256=") || err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
			return nil, huma.Error401Unauthorized("invalid webhook signature")
		}
		hints, err := reduce(input.Event, input.RawBody, source.RepositoryIDs)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid webhook payload")
		}
		if err := store.Append(ctx, hints); err != nil {
			return nil, huma.Error503ServiceUnavailable("could not persist activity")
		}
		return &struct{}{}, nil
	})
	type feedInput struct {
		After string `query:"after"`
		Limit int    `query:"limit" default:"100" minimum:"1" maximum:"1000"`
	}
	type feedOutput struct {
		Status int
		Body   Page
	}
	huma.Register(feedAPI, huma.Operation{OperationID: "read-activity", Method: http.MethodGet, Path: "/activity"},
		func(ctx context.Context, input *feedInput) (*feedOutput, error) {
			page, err := store.Read(ctx, input.After, input.Limit)
			if errors.Is(err, ErrInvalidCursor) {
				return nil, huma.Error400BadRequest("invalid activity cursor")
			}
			if err != nil {
				return nil, huma.Error503ServiceUnavailable("could not read activity")
			}
			status := http.StatusOK
			if page.Code == "cursor_expired" {
				status = http.StatusGone
			}
			return &feedOutput{Status: status, Body: page}, nil
		})
	huma.Register(feedAPI, huma.Operation{OperationID: "relay-health", Method: http.MethodGet, Path: "/healthz"},
		func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			if err := store.db.PingContext(ctx); err != nil {
				return nil, huma.Error503ServiceUnavailable("activity store unavailable")
			}
			return &struct{}{}, nil
		})
	return public, private
}

type pullReference struct {
	Number int `json:"number"`
}

// Decode only routing fields. Neither this value nor decoder errors are stored
// or logged; a fresh Hint is the only value passed to persistence.
func reduce(event string, body []byte, allowed []int64) ([]Hint, error) {
	switch event {
	case "pull_request", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread",
		"issues", "issue_comment", "push", "create", "delete", "repository":
	default:
		return nil, nil
	}
	var payload struct {
		Repository struct {
			ID     int64  `json:"id"`
			NodeID string `json:"node_id"`
		} `json:"repository"`
		PullRequest *pullReference `json:"pull_request"`
		Issue       *struct {
			Number      int       `json:"number"`
			PullRequest *struct{} `json:"pull_request"`
		} `json:"issue"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Repository.ID <= 0 || !slices.Contains(allowed, payload.Repository.ID) {
		return nil, errors.New("invalid webhook repository")
	}
	hint := Hint{Provider: "github", Host: "github.com", RepositoryID: payload.Repository.NodeID}
	switch event {
	case "pull_request", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread":
		if payload.PullRequest == nil {
			return nil, errors.New("missing pull request")
		}
		hint.Target, hint.Number = PullRequest, payload.PullRequest.Number
	case "issues", "issue_comment":
		if payload.Issue == nil {
			return nil, errors.New("missing issue")
		}
		hint.Target, hint.Number = Issue, payload.Issue.Number
		if payload.Issue.PullRequest != nil {
			hint.Target = PullRequest
		}
	case "push", "create", "delete":
		hint.Target = RepositoryRefs
	case "repository":
		hint.Target = Repository
	}
	if err := hint.Validate(); err != nil {
		return nil, err
	}
	return []Hint{hint}, nil
}

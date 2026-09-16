package activityrelay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// keepaliveInterval keeps idle streams alive through proxies that close
// silent connections.
const keepaliveInterval = 20 * time.Second

type Source struct {
	Secret        []byte
	RepositoryIDs []int64
}

func Handlers(broadcaster *Broadcaster, sources map[string]Source) (http.Handler, http.Handler) {
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
		broadcaster.Publish(hints)
		return &struct{}{}, nil
	})
	huma.Register(feedAPI, huma.Operation{
		OperationID: "subscribe-activity", Method: http.MethodGet, Path: "/activity",
		Responses: map[string]*huma.Response{"200": {
			Description: "Live refresh hints",
			Content:     map[string]*huma.MediaType{"text/event-stream": {}},
		}},
	}, func(context.Context, *struct{}) (*huma.StreamResponse, error) {
		return &huma.StreamResponse{Body: func(ctx huma.Context) {
			ctx.SetHeader("Content-Type", "text/event-stream")
			ctx.SetHeader("Cache-Control", "no-cache")
			_, w := humago.Unwrap(ctx)
			serveStream(ctx.Context(), w, broadcaster)
		}}, nil
	})
	huma.Register(feedAPI, huma.Operation{OperationID: "relay-health", Method: http.MethodGet, Path: "/healthz"},
		func(context.Context, *struct{}) (*struct{}, error) {
			return &struct{}{}, nil
		})
	return public, private
}

func serveStream(ctx context.Context, w http.ResponseWriter, broadcaster *Broadcaster) {
	controller := http.NewResponseController(w)
	// The stream outlives the server's per-response write timeout.
	_ = controller.SetWriteDeadline(time.Time{})
	write := func(frame string) bool {
		if _, err := io.WriteString(w, frame); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	hints, cancel := broadcaster.Subscribe()
	defer cancel()
	if !write(": connected\n\n") {
		return
	}
	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case hint := <-hints:
			data, err := json.Marshal(hint)
			if err != nil || !write("event: "+hintEvent+"\ndata: "+string(data)+"\n\n") {
				return
			}
		case <-keepalive.C:
			if !write(": keepalive\n\n") {
				return
			}
		}
	}
}

type pullReference struct {
	Number int `json:"number"`
}

// Decode only routing fields. Neither this value nor decoder errors are
// retained or logged; a fresh Hint is the only value that leaves this function.
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

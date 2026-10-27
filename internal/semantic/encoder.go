package semantic

import (
	"context"
	"fmt"
	"time"

	"go.kenn.io/kit/embedclient"
	"go.kenn.io/kit/embedconfig"
	"go.kenn.io/kit/embedmodel"
	"go.kenn.io/kit/vector"
)

// recipe identifies how Forge turns sources into encoder inputs: the
// documentContent text and the chunk window below. Bump it when either
// changes so the sidecar is rebuilt and every document embedded again.
const recipe = "forge.semantic/v1"

// splitOptions bounds one encoder input. Runes only approximate tokens;
// 2000 runes stays under common embedding model context limits.
var splitOptions = vector.SplitOptions{MaxRunes: 2000, Overlap: 200}

// Encoder embeds documents and queries through one configured endpoint.
type Encoder struct {
	client     *embedclient.Client
	generation vector.Generation
	key        string
	batch      embedconfig.Batch
}

// NewEncoder builds an encoder from the [embeddings] configuration. It
// resolves the API key once; changing the key source requires a restart.
func NewEncoder(cfg embedconfig.Embedder) (*Encoder, error) {
	parts, err := cfg.Parts()
	if err != nil {
		return nil, fmt.Errorf("semantic: configure embeddings: %w", err)
	}
	secret, err := cfg.ResolveAPIKey()
	if err != nil {
		return nil, fmt.Errorf("semantic: %w", err)
	}
	client, err := embedclient.New(embedclient.Options{
		Model: parts.Model, Roles: parts.Roles, Deployment: parts.Deployment,
		Batch: parts.Batch, Transport: parts.Transport, APIKey: secret.Value,
		Retry: embedclient.Retry{MaxAttempts: 3, MaxBackoff: 10 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: configure embedding client: %w", err)
	}
	descriptor := embedmodel.Descriptor{
		Model: parts.Model, Roles: parts.Roles, Deployment: parts.Deployment,
		Input: embedconfig.InputLimits{Recipe: recipe},
	}
	generation, err := descriptor.Generation()
	if err != nil {
		return nil, fmt.Errorf("semantic: embedding identity: %w", err)
	}
	// The vector space alone does not change when the recipe does, but the
	// stored vectors would no longer match what Forge would embed today.
	generation.Params["recipe"] = recipe
	return &Encoder{
		client: client, generation: generation, key: generation.Fingerprint(),
		batch: parts.Batch,
	}, nil
}

// documents encodes document text with the configured document affixes.
func (e *Encoder) documents(ctx context.Context, texts []string) ([][]float32, error) {
	return e.client.EmbedTexts(ctx, embedconfig.RoleDocument, texts)
}

// query encodes one search query with the configured query affixes.
func (e *Encoder) query(ctx context.Context, text string) (vector.Vector, error) {
	return vector.EncodeOne(ctx, func(ctx context.Context, texts []string) ([][]float32, error) {
		return e.client.EmbedTexts(ctx, embedconfig.RoleQuery, texts)
	}, text)
}

func (e *Encoder) fillBatchOptions() []vector.BatchOption {
	options := []vector.BatchOption{vector.WithBatchSize(e.batch.Items)}
	if e.batch.MaxTokens > 0 && e.batch.InputTokenUpperBound > 0 {
		options = append(options, vector.WithBatchTokenBudget(e.batch.MaxTokens, e.batch.InputTokenUpperBound))
	}
	return options
}

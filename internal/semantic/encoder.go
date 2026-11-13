package semantic

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"go.kenn.io/forge/internal/tracing"
	"go.kenn.io/kit/embedclient"
	"go.kenn.io/kit/embedconfig"
	"go.kenn.io/kit/embedlocal"
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
	client                       *embedclient.Client
	generation                   vector.Generation
	key                          string
	batch                        embedconfig.Batch
	resolve                      func(context.Context) (string, bool, error)
	options                      embedclient.Options
	defaultBatch, defaultTimeout bool
	localSlot                    chan struct{}
}

// NewEncoder builds an encoder from the [embeddings] configuration. It
// resolves the API key once; changing the key source requires a restart.
func NewEncoder(cfg embedconfig.Embedder, resolve func(context.Context) (string, bool, error)) (*Encoder, error) {
	parts, err := cfg.Parts()
	if err != nil {
		return nil, fmt.Errorf("semantic: configure embeddings: %w", err)
	}
	secret, err := cfg.ResolveAPIKey()
	if err != nil {
		return nil, fmt.Errorf("semantic: %w", err)
	}
	options := embedclient.Options{
		Model: parts.Model, Roles: parts.Roles, Deployment: parts.Deployment,
		Batch: parts.Batch, Transport: parts.Transport, APIKey: secret.Value,
		Retry: embedclient.Retry{MaxAttempts: 3, MaxBackoff: 10 * time.Second},
	}
	client, err := embedclient.New(options)
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
		batch: parts.Batch, resolve: resolve, options: options,
		defaultBatch: cfg.BatchSize == 0, defaultTimeout: cfg.TimeoutSeconds == 0,
		localSlot: make(chan struct{}, 1),
	}, nil
}

// documents encodes document text with the configured document affixes.
func (e *Encoder) documents(ctx context.Context, texts []string) ([][]float32, error) {
	if e.resolve == nil {
		return e.client.EmbedTexts(ctx, embedconfig.RoleDocument, texts)
	}
	client, err := e.clientForRequest(ctx)
	if err != nil {
		return nil, err
	}
	return client.EmbedTexts(ctx, embedconfig.RoleDocument, texts)
}

// query encodes one search query with the configured query affixes.
func (e *Encoder) query(ctx context.Context, text string) (vector.Vector, error) {
	return vector.EncodeOne(ctx, func(ctx context.Context, texts []string) ([][]float32, error) {
		client, err := e.clientForRequest(ctx)
		if err != nil {
			return nil, err
		}
		return client.EmbedTexts(ctx, embedconfig.RoleQuery, texts)
	}, text)
}

func (e *Encoder) optionsForRequest(ctx context.Context) (embedclient.Options, bool, error) {
	options := e.options
	if e.resolve == nil {
		return options, false, nil
	}
	endpoint, builtin, err := e.resolve(ctx)
	if err != nil {
		return options, false, err
	}
	options.Deployment.BaseURL = endpoint
	if builtin {
		if e.defaultBatch {
			options.Batch.Items = embedlocal.BatchItems
		}
		if e.defaultTimeout {
			options.Transport.Timeout = embedlocal.RequestTimeout
		}
	}
	return options, builtin, nil
}

func (e *Encoder) clientForRequest(ctx context.Context) (*embedclient.Client, error) {
	if e.resolve == nil {
		return e.client, nil
	}
	options, builtin, err := e.optionsForRequest(ctx)
	if err != nil {
		return nil, err
	}
	if builtin {
		client := &http.Client{Timeout: options.Transport.Timeout}
		if options.HTTP != nil {
			*client = *options.HTTP
		}
		base := client.Transport
		if base == nil {
			base = tracing.HTTPTransport(nil)
		}
		client.Transport = localTransport{base: base, slot: e.localSlot}
		options.HTTP = client
	}
	return embedclient.New(options)
}

type localTransport struct {
	base http.RoundTripper
	slot chan struct{}
}

func (t localTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case t.slot <- struct{}{}:
	case <-req.Context().Done():
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, req.Context().Err()
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		<-t.slot
		return nil, err
	}
	resp.Body = &localResponseBody{ReadCloser: resp.Body, release: sync.OnceFunc(func() { <-t.slot })}
	return resp, nil
}

type localResponseBody struct {
	io.ReadCloser
	release func()
}

func (b *localResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.release()
	}
	return n, err
}

func (b *localResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}

func (e *Encoder) fillBatchOptions() []vector.BatchOption {
	options := []vector.BatchOption{vector.WithBatchSize(e.batch.Items)}
	if e.batch.MaxTokens > 0 && e.batch.InputTokenUpperBound > 0 {
		options = append(options, vector.WithBatchTokenBudget(e.batch.MaxTokens, e.batch.InputTokenUpperBound))
	}
	return options
}

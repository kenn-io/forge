package semantic

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"go.kenn.io/kit/embedclient"
	"go.kenn.io/kit/vector"
	"go.kenn.io/kit/vector/sqlitevec"
)

// Status reports the index's coverage and the outcome of its latest pass.
// Ready means every document is embedded or skipped for the configured
// model; until then searches answer from the documents embedded so far.
type Status struct {
	Model         string
	Embedded      int64
	Skipped       int64
	Backlog       int64
	Ready         bool
	LastSuccessAt *time.Time
	LastError     string
	LastErrorAt   *time.Time
}

// Indexer keeps the sidecar mirror current, embeds pending documents, and
// answers searches from the configured model's generation.
type Indexer struct {
	index    *Index
	encoder  *Encoder
	source   SourceReader
	interval time.Duration
	wake     chan struct{}

	mu     sync.Mutex
	status Status
}

// NewIndexer binds an open index to an encoder and the Forge database.
// Run refreshes it every interval and whenever Wake is called.
func NewIndexer(index *Index, encoder *Encoder, source SourceReader, interval time.Duration) *Indexer {
	return &Indexer{
		index: index, encoder: encoder, source: source, interval: interval,
		wake:   make(chan struct{}, 1),
		status: Status{Model: encoder.Model()},
	}
}

// Wake asks Run to start a pass now. It never blocks.
func (x *Indexer) Wake() {
	select {
	case x.wake <- struct{}{}:
	default:
	}
}

// Status returns a snapshot of the index's coverage and health.
func (x *Indexer) Status() Status {
	x.mu.Lock()
	defer x.mu.Unlock()
	status := x.status
	status.LastSuccessAt = cloneTime(status.LastSuccessAt)
	status.LastErrorAt = cloneTime(status.LastErrorAt)
	return status
}

// Run refreshes the index until ctx ends. A failed pass is retried on the
// next tick or wake; the endpoint client already retries transient errors
// within a pass.
func (x *Indexer) Run(ctx context.Context) {
	ticker := time.NewTicker(x.interval)
	defer ticker.Stop()
	for {
		if err := x.Refresh(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("semantic index refresh failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-x.wake:
		}
	}
}

// Refresh runs one pass: mirror the Forge database, publish the configured
// generation, and embed what is pending.
//
// The configured generation is published before the fill, on a cold start
// and after a model or recipe change alike, so search serves partial results
// during a backfill. A previous generation cannot keep answering: queries are
// embedded with the configured model, whose vectors do not compare with
// another model's.
func (x *Indexer) Refresh(ctx context.Context) error {
	err := x.refresh(ctx)
	now := time.Now().UTC()
	x.mu.Lock()
	defer x.mu.Unlock()
	if err != nil {
		x.status.LastError = err.Error()
		x.status.LastErrorAt = &now
		return err
	}
	x.status.LastError = ""
	x.status.LastErrorAt = nil
	x.status.LastSuccessAt = &now
	return nil
}

func (x *Indexer) refresh(ctx context.Context) error {
	if _, err := x.index.SyncMirror(ctx, x.source); err != nil {
		return err
	}
	key, err := x.ensurePublished(ctx)
	if err != nil {
		return err
	}
	if err := x.publishCoverage(ctx, key); err != nil {
		return err
	}
	_, fillErr := vector.Fill(ctx, x.index.store, key, x.encoder.documents,
		vector.WithFillSplit[string](splitOptions),
		vector.WithFillBatch[string](x.encoder.fillBatchOptions()...),
		vector.WithFillBatchErrorIsolation[string](inputRejected),
		vector.WithFillEncodeError[string](func(doc string, err error) bool {
			if inputRejected(err) {
				slog.Debug("semantic index skipped a document the endpoint rejected", "doc", doc, "err", err)
				return true
			}
			return false
		}),
		vector.WithFillProgress[string](x.recordProgress),
	)
	if err := x.publishCoverage(ctx, key); err != nil {
		return errors.Join(fillErr, err)
	}
	if fillErr != nil {
		return fmt.Errorf("semantic: fill: %w", fillErr)
	}
	return nil
}

// inputRejected reports an endpoint refusal of the input itself, such as
// text over the model's limit. Only those documents are skipped; every other
// failure aborts the pass so a misconfigured or unreachable endpoint never
// stamps the corpus as skipped.
func inputRejected(err error) bool {
	apiErr, ok := errors.AsType[*embedclient.APIError](err)
	return ok && apiErr.InputRejected()
}

// ensurePublished registers the configured generation, makes it the one
// searches use, and returns its key. A generation that was retired cannot
// be reused, so returning to an earlier configuration registers a fresh key
// for the same vector space.
func (x *Indexer) ensurePublished(ctx context.Context) (string, error) {
	generations, err := x.index.store.Generations(ctx)
	if err != nil {
		return "", err
	}
	states := make(map[string]sqlitevec.State, len(generations))
	for _, generation := range generations {
		states[generation.Key] = generation.State
	}
	key := x.encoder.key
	for n := 2; states[key] == sqlitevec.StateRetired; n++ {
		key = x.encoder.key + "." + strconv.Itoa(n)
	}
	if states[key] == sqlitevec.StateActive {
		return key, nil
	}
	if err := x.index.store.EnsureGeneration(ctx, key, x.encoder.generation, sqlitevec.StateBuilding); err != nil {
		return "", fmt.Errorf("semantic: ensure generation: %w", err)
	}
	return key, x.cutOver(ctx, key)
}

// cutOver makes key the generation searches use and reclaims every other
// generation's storage. A complete generation is published with Activate,
// which checks coverage; otherwise the partial generation is published.
func (x *Indexer) cutOver(ctx context.Context, key string) error {
	coverage, err := x.index.store.Coverage(ctx, key, "")
	if err != nil {
		return err
	}
	if coverage.Backlog == 0 {
		err = x.index.store.Activate(ctx, key)
	} else {
		err = x.index.store.SetGenerationState(ctx, key, sqlitevec.StateActive)
	}
	if err != nil {
		return fmt.Errorf("semantic: publish generation: %w", err)
	}
	generations, err := x.index.store.Generations(ctx)
	if err != nil {
		return err
	}
	for _, generation := range generations {
		if generation.Key == key {
			continue
		}
		if generation.State != sqlitevec.StateRetired {
			if err := x.index.store.SetGenerationState(ctx, generation.Key, sqlitevec.StateRetired); err != nil {
				return err
			}
		}
		if err := x.index.store.Reclaim(ctx, generation.Key); err != nil {
			return err
		}
	}
	return nil
}

func (x *Indexer) publishCoverage(ctx context.Context, key string) error {
	coverage, err := x.index.store.Coverage(ctx, key, "")
	if err != nil {
		return err
	}
	active, hasActive, err := x.index.store.ActiveGeneration(ctx)
	if err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.status.Embedded = coverage.Embedded
	x.status.Skipped = coverage.Skipped
	x.status.Backlog = coverage.Backlog
	x.status.Ready = hasActive && active.Key == key && coverage.Backlog == 0
	return nil
}

func (x *Indexer) recordProgress(progress vector.FillProgress[string]) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if progress.Vectors > 0 {
		x.status.Embedded++
	} else {
		x.status.Skipped++
	}
	if x.status.Backlog > 0 {
		x.status.Backlog--
	}
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

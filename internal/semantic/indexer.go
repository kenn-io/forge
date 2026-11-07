package semantic

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.kenn.io/kit/embedclient"
	"go.kenn.io/kit/vector"
)

// minRefreshGap keeps frequent syncs from rereading every description and
// comment more than once a minute.
const minRefreshGap = time.Minute

// Wake asks Run to start a pass now. It never blocks.
func (x *Indexer) Wake() {
	select {
	case x.wake <- struct{}{}:
	default:
	}
}

// Run refreshes the index until ctx ends. A failed pass is retried on the
// next tick or wake; the endpoint client already retries transient errors
// within a pass.
func (x *Indexer) Run(ctx context.Context) {
	schedulePasses(ctx, x.interval, x.wake, func() {
		if err := x.Refresh(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("semantic index refresh failed", "err", err)
		}
	})
}

// schedulePasses runs pass now, then after every tick or wake until ctx
// ends. Passes start at least minRefreshGap apart: a trigger that arrives
// sooner waits out the gap, and the next pass answers every wake that
// arrived before it started.
func schedulePasses(ctx context.Context, interval time.Duration, wake chan struct{}, pass func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-wake:
		default:
		}
		started := time.Now()
		pass()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(started.Add(minRefreshGap))):
		}
	}
}

// Refresh runs one pass: mirror the Forge database, then embed what is
// pending.
func (x *Indexer) Refresh(ctx context.Context) error {
	if err := x.syncMirror(ctx); err != nil {
		return err
	}
	_, err := vector.Fill(ctx, x.store, x.encoder.key, x.encoder.documents,
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
	)
	if err != nil {
		return fmt.Errorf("semantic: fill: %w", err)
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

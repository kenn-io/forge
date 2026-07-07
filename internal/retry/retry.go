package retry

import (
	"context"
	"log/slog"
	"time"

	"github.com/cenkalti/backoff/v7"
)

const DefaultMaxTries = 3

type Config[T any] struct {
	Label       string
	BackOff     backoff.BackOff
	MaxTries    uint
	IsTransient func(error) bool
	Op          func() (T, error)
}

func DefaultBackOff() *backoff.ExponentialBackOff {
	expo := backoff.NewExponentialBackOff()
	expo.InitialInterval = 500 * time.Millisecond
	expo.MaxInterval = 4 * time.Second
	expo.RandomizationFactor = 0.3
	return expo
}

func Do[T any](ctx context.Context, cfg Config[T]) (T, error) {
	if cfg.BackOff == nil {
		cfg.BackOff = DefaultBackOff()
	}
	if cfg.MaxTries == 0 {
		cfg.MaxTries = DefaultMaxTries
	}
	return DoWithBackOff(ctx, cfg)
}

func DoWithBackOff[T any](ctx context.Context, cfg Config[T]) (T, error) {
	wrapped := func() (T, error) {
		v, err := cfg.Op()
		if err == nil {
			return v, nil
		}
		if cfg.IsTransient != nil && cfg.IsTransient(err) {
			return v, err
		}
		return v, backoff.Permanent(err)
	}

	notify := func(err error, next time.Duration) {
		slog.Debug("retrying transient failure",
			"op", cfg.Label, "next", next, "err", err)
	}

	return backoff.Retry(ctx, wrapped,
		backoff.WithBackOff(cfg.BackOff),
		backoff.WithMaxTries(cfg.MaxTries),
		backoff.WithNotify(notify),
	)
}

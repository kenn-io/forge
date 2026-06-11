package tokenauth

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type redactingHandler struct {
	base slog.Handler
}

func NewRedactingHandler(base slog.Handler) slog.Handler {
	return redactingHandler{base: base}
}

func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	safe := slog.NewRecord(
		record.Time,
		record.Level,
		RedactKnownSecrets(record.Message),
		record.PC,
	)
	record.Attrs(func(attr slog.Attr) bool {
		safe.AddAttrs(redactLogAttr(attr))
		return true
	})
	return h.base.Handle(ctx, safe)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		safe = append(safe, redactLogAttr(attr))
	}
	return redactingHandler{base: h.base.WithAttrs(safe)}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{base: h.base.WithGroup(RedactKnownSecrets(name))}
}

func redactLogAttr(attr slog.Attr) slog.Attr {
	attr.Key = RedactKnownSecrets(attr.Key)
	attr.Value = redactLogValue(attr.Key, attr.Value)
	return attr
}

func redactLogValue(key string, value slog.Value) slog.Value {
	value = value.Resolve()
	if isSensitiveLogKey(key) {
		return slog.StringValue(redacted)
	}
	switch value.Kind() {
	case slog.KindString:
		return slog.StringValue(RedactKnownSecrets(value.String()))
	case slog.KindAny:
		switch typed := value.Any().(type) {
		case error:
			return slog.AnyValue(RedactError(typed))
		case string:
			return slog.StringValue(RedactKnownSecrets(typed))
		case fmt.Stringer:
			return slog.StringValue(RedactKnownSecrets(typed.String()))
		default:
			return value
		}
	case slog.KindGroup:
		attrs := value.Group()
		safe := make([]slog.Attr, 0, len(attrs))
		for _, attr := range attrs {
			safe = append(safe, redactLogAttr(attr))
		}
		return slog.GroupValue(safe...)
	default:
		return value
	}
}

func isSensitiveLogKey(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "token") ||
		strings.Contains(key, "secret") ||
		strings.Contains(key, "password") ||
		strings.Contains(key, "authorization") ||
		strings.Contains(key, "private-token")
}

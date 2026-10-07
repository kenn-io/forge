package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (d *DB) AppMetadataValue(ctx context.Context, key string) (string, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false, errors.New("app metadata key is required")
	}

	var value string
	err := d.roQueryRowContext(ctx,
		`SELECT value FROM forge_app_metadata WHERE key = ?`,
		key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get app metadata: %w", err)
	}
	return value, true, nil
}

// ClaimTelemetryScreenDay atomically claims a screen for the installation ID
// stored under installIDKey. It returns the claim and false when this
// installation already claimed the screen on day.
func (d *DB) ClaimTelemetryScreenDay(
	ctx context.Context,
	installIDKey, screen, day string,
) (string, bool, error) {
	var claim string
	err := d.rwQueryRowContext(ctx, `INSERT INTO forge_app_metadata (key, value, updated_at)
			SELECT ?, value || char(10) || ?, CURRENT_TIMESTAMP
			FROM forge_app_metadata WHERE key = ?
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
			WHERE forge_app_metadata.value != excluded.value RETURNING value`,
		telemetryScreenKey(screen), day, installIDKey).Scan(&claim)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("claim telemetry screen %q: %w", screen, err)
	}
	return claim, true, nil
}

// ReleaseTelemetryScreenDay preserves any newer installation or day claim.
func (d *DB) ReleaseTelemetryScreenDay(ctx context.Context, screen, claim string) error {
	_, err := d.rwExecContext(ctx,
		`DELETE FROM forge_app_metadata WHERE key = ? AND value = ?`,
		telemetryScreenKey(screen), claim,
	)
	if err != nil {
		return fmt.Errorf("release telemetry screen %q: %w", screen, err)
	}
	return nil
}

func telemetryScreenKey(screen string) string {
	return "telemetry.screen." + screen
}

func (d *DB) GetOrCreateAppMetadataValue(
	ctx context.Context,
	key string,
	create func() (string, error),
) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("app metadata key is required")
	}
	if create == nil {
		return "", errors.New("app metadata create func is required")
	}

	var value string
	if err := d.Tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`SELECT value FROM forge_app_metadata WHERE key = ?`,
			key,
		).Scan(&value)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get app metadata: %w", err)
		}

		created, err := create()
		if err != nil {
			return err
		}
		created = strings.TrimSpace(created)
		if created == "" {
			return errors.New("created app metadata value is required")
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO forge_app_metadata (key, value, updated_at)
			 VALUES (?, ?, CURRENT_TIMESTAMP)`,
			key, created,
		)
		if err != nil {
			return fmt.Errorf("insert app metadata: %w", err)
		}
		value = created
		return nil
	}); err != nil {
		return "", err
	}
	return value, nil
}

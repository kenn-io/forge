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
	err := d.ro.QueryRowContext(ctx,
		`SELECT value FROM middleman_app_metadata WHERE key = ?`,
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
			`SELECT value FROM middleman_app_metadata WHERE key = ?`,
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
			`INSERT INTO middleman_app_metadata (key, value, updated_at)
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

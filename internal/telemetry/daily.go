package telemetry

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/kit/atomicfile"
	"go.kenn.io/kit/telemetry/posthog"
)

// Seed kit's versioned state once so an upgrade preserves accepted screen counts.
func initializeDailyClaims(database *db.DB, installID, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("telemetry daily claims path is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), posthog.ShutdownTimeout)
	defer cancel()
	lock := flock.New(path + ".lock")
	if _, err := lock.TryLockContext(ctx, 10*time.Millisecond); err != nil {
		return fmt.Errorf("lock telemetry daily claims: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state := struct {
		Version int                 `json:"version"`
		Days    map[string][]string `json:"days"`
	}{Version: 1, Days: make(map[string][]string)}
	for _, screen := range screenNames {
		claim, found, err := database.AppMetadataValue(ctx, "telemetry.screen."+screen)
		if err != nil {
			return err
		}
		identity, day, ok := strings.Cut(claim, "\n")
		if !found || !ok || identity != installID {
			continue
		}
		if _, err := time.Parse(time.DateOnly, day); err != nil {
			return fmt.Errorf("read previous telemetry screen day: %w", err)
		}
		key, err := json.Marshal([]string{installID, "screen_viewed", screen})
		if err != nil {
			return err
		}
		state.Days[string(key)] = []string{day}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, data, atomicfile.WithPrivate())
}

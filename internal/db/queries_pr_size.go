package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// PRDiffSizePoint is the total provider-reported PR diff at an observed
// head/base revision, never per-commit churn. Observations start at sync time;
// skipped heads and older history are not reconstructed.
type PRDiffSizePoint struct {
	HeadSHA    string    `json:"head_sha"`
	BaseSHA    string    `json:"base_sha"`
	Additions  int       `json:"additions"`
	Deletions  int       `json:"deletions"`
	ObservedAt time.Time `json:"observed_at"`
}

func recordPRDiffSize(ctx context.Context, executor mergeRequestSnapshotExecutor, id int64, mr *MergeRequest) error {
	if mr.PlatformHeadSHA == "" || !mr.AdditionsKnown || !mr.DeletionsKnown || mr.Additions < 0 || mr.Deletions < 0 {
		return nil
	}
	var raw string
	if err := executor.QueryRowContext(ctx,
		`SELECT diff_size_history_json FROM forge_merge_requests WHERE id = ?`, id,
	).Scan(&raw); err != nil {
		return fmt.Errorf("read PR diff size history: %w", err)
	}
	var points []PRDiffSizePoint
	if err := json.Unmarshal([]byte(raw), &points); err != nil {
		return fmt.Errorf("decode PR diff size history: %w", err)
	}
	point := PRDiffSizePoint{
		HeadSHA: mr.PlatformHeadSHA, BaseSHA: mr.PlatformBaseSHA,
		Additions: mr.Additions, Deletions: mr.Deletions, ObservedAt: time.Now().UTC(),
	}
	if len(points) > 0 {
		last := points[len(points)-1]
		if last.HeadSHA == point.HeadSHA && last.BaseSHA == point.BaseSHA {
			if last.Additions == point.Additions && last.Deletions == point.Deletions {
				return nil
			}
			// A later enrichment can correct totals for the same revision.
			points = points[:len(points)-1]
		}
	}
	points = append(points, point)
	const retainedRevisions = 32
	if len(points) > retainedRevisions {
		points = points[len(points)-retainedRevisions:]
	}
	encoded, err := json.Marshal(points)
	if err != nil {
		return fmt.Errorf("encode PR diff size history: %w", err)
	}
	if _, err := executor.ExecContext(ctx,
		`UPDATE forge_merge_requests SET diff_size_history_json = ? WHERE id = ?`, string(encoded), id,
	); err != nil {
		return fmt.Errorf("write PR diff size history: %w", err)
	}
	return nil
}

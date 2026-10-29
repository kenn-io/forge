package httpapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/db"
)

func TestBranchUpdateEligible(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*db.MergeRequest)
		eligible bool
	}{
		{"clean behind", func(*db.MergeRequest) {}, true},
		{"strict behind", func(mr *db.MergeRequest) { mr.MergeableState = "behind"; mr.MergeBaseSHA = "" }, true},
		{"up to date", func(mr *db.MergeRequest) { mr.MergeBaseSHA = "base" }, false},
		{"stale head", func(mr *db.MergeRequest) { mr.DiffHeadSHA = "old-head" }, false},
		{"stale base", func(mr *db.MergeRequest) { mr.DiffBaseSHA = "old-base" }, false},
		{"missing ancestry", func(mr *db.MergeRequest) { mr.MergeBaseSHA = "" }, false},
		{"unknown", func(mr *db.MergeRequest) { mr.MergeableState = "unknown" }, false},
		{"conflicted", func(mr *db.MergeRequest) { mr.MergeableState = "dirty" }, false},
		{"closed", func(mr *db.MergeRequest) { mr.State = "closed" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mr := db.MergeRequest{State: "open", MergeableState: "clean", PlatformHeadSHA: "head", PlatformBaseSHA: "base", DiffHeadSHA: "head", DiffBaseSHA: "base", MergeBaseSHA: "ancestor"}
			tc.change(&mr)
			assert.Equal(t, tc.eligible, BranchUpdateEligible(mr))
		})
	}
}

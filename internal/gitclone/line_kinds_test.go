package gitclone

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCountLineKinds(t *testing.T) {
	t.Parallel()
	files := []DiffFile{
		{Path: "cmd/server/main.go", Additions: 10, Deletions: 2},
		{Path: "deploy/config.yaml", Additions: 3, Deletions: 1},
		{Path: "docs/Guide.MD", Additions: 20, Deletions: 5},
		{Path: "NOTES.txt", Additions: 1},
		{Path: "testdata/events.jsonl", Additions: 400, Deletions: 100},
		{Path: "web/icon.svg", Additions: 30},
		// The generated mark wins over a data extension.
		{Path: "api/schema.json", IsGenerated: true, Additions: 900, Deletions: 800},
		{Path: "package-lock.json", IsGenerated: true, Additions: 50, Deletions: 40},
		{Path: "assets/logo.png", IsBinary: true},
	}

	assert.Equal(t, LineKinds{
		Code:      LineCounts{Additions: 13, Deletions: 3},
		Docs:      LineCounts{Additions: 21, Deletions: 5},
		Data:      LineCounts{Additions: 430, Deletions: 100},
		Generated: LineCounts{Additions: 950, Deletions: 840},
	}, CountLineKinds(files))
}

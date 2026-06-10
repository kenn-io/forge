package docs

import (
	"bytes"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePorcelainRecords(t *testing.T) {
	build := func(parts ...string) []byte {
		var b bytes.Buffer
		for _, p := range parts {
			b.WriteString(p)
			b.WriteByte(0)
		}
		return b.Bytes()
	}

	cases := []struct {
		name string
		in   []byte
		want []PorcelainRecord
	}{
		{name: "modified in worktree", in: build(" M README.md"), want: []PorcelainRecord{{X: ' ', Y: 'M', Path: "README.md"}}},
		{name: "added in index", in: build("A  add.md"), want: []PorcelainRecord{{X: 'A', Y: ' ', Path: "add.md"}}},
		{name: "deleted in worktree", in: build(" D gone.md"), want: []PorcelainRecord{{X: ' ', Y: 'D', Path: "gone.md"}}},
		{name: "untracked", in: build("?? new.md"), want: []PorcelainRecord{{X: '?', Y: '?', Path: "new.md"}}},
		{name: "ignored", in: build("!! cache.tmp"), want: []PorcelainRecord{{X: '!', Y: '!', Path: "cache.tmp"}}},
		{name: "rename keeps old path", in: build("R  newname.md", "oldname.md"), want: []PorcelainRecord{{X: 'R', Y: ' ', Path: "newname.md", OldPath: "oldname.md"}}},
		{name: "copy keeps source path", in: build("C  dup.md", "orig.md"), want: []PorcelainRecord{{X: 'C', Y: ' ', Path: "dup.md", OldPath: "orig.md"}}},
		{name: "unmerged UU", in: build("UU conflict.md"), want: []PorcelainRecord{{X: 'U', Y: 'U', Path: "conflict.md"}}},
		{name: "partially staged MM", in: build("MM partial.md"), want: []PorcelainRecord{{X: 'M', Y: 'M', Path: "partial.md"}}},
		{name: "worktree rename uses Y column", in: build(" R newname.md", "oldname.md"), want: []PorcelainRecord{{X: ' ', Y: 'R', Path: "newname.md", OldPath: "oldname.md"}}},
		{name: "worktree copy uses Y column", in: build(" C dup.md", "orig.md"), want: []PorcelainRecord{{X: ' ', Y: 'C', Path: "dup.md", OldPath: "orig.md"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePorcelainRecords(tc.in)
			require.NoError(t, err)
			Assert.Equal(t, tc.want, got)
		})
	}
}

func TestParsePorcelainRecordsRejectsMalformedRename(t *testing.T) {
	_, err := parsePorcelainRecords([]byte("R  newname.md\x00"))
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "malformed rename")
}

func TestComputePublishSet(t *testing.T) {
	cases := []struct {
		name    string
		records []PorcelainRecord
		want    PublishSet
	}{
		{
			name: "skips non-markdown",
			records: []PorcelainRecord{
				{X: ' ', Y: 'M', Path: "README.md"},
				{X: ' ', Y: 'M', Path: "package.json"},
			},
			want: PublishSet{
				Changes:                 []PublishChange{{Path: "README.md", Status: GitChangeModified}},
				IgnoredNonMarkdownCount: 1,
			},
		},
		{
			name:    "skips gitignored entries",
			records: []PorcelainRecord{{X: '!', Y: '!', Path: "skip.md"}},
			want:    PublishSet{IgnoredMarkdownCount: 1},
		},
		{
			name:    "includes untracked markdown",
			records: []PorcelainRecord{{X: '?', Y: '?', Path: "new.md"}},
			want:    PublishSet{Changes: []PublishChange{{Path: "new.md", Status: GitChangeUntracked}}},
		},
		{
			name:    "rename includes old and new paths",
			records: []PorcelainRecord{{X: 'R', Y: ' ', Path: "new.md", OldPath: "old.md"}},
			want: PublishSet{Changes: []PublishChange{{
				Path: "new.md", OldPath: "old.md", Status: GitChangeRenamed,
			}}},
		},
		{
			name:    "partial stage MM flagged",
			records: []PorcelainRecord{{X: 'M', Y: 'M', Path: "partial.md"}},
			want: PublishSet{
				Changes:           []PublishChange{{Path: "partial.md", Status: GitChangeModified}},
				PartiallyStagedMD: []string{"partial.md"},
			},
		},
		{
			name:    "unmerged markdown flagged",
			records: []PorcelainRecord{{X: 'U', Y: 'U', Path: "conflict.md"}},
			want: PublishSet{
				Changes:    []PublishChange{{Path: "conflict.md", Status: GitChangeModified}},
				UnmergedMD: []string{"conflict.md"},
			},
		},
		{
			name:    "staged non-markdown trips the unrelated-staged flag",
			records: []PorcelainRecord{{X: 'M', Y: ' ', Path: "code.go"}},
			want: PublishSet{
				UnrelatedStagedPresent:  true,
				IgnoredNonMarkdownCount: 1,
			},
		},
		{
			name:    "deleted markdown is a publish entry",
			records: []PorcelainRecord{{X: ' ', Y: 'D', Path: "gone.md"}},
			want:    PublishSet{Changes: []PublishChange{{Path: "gone.md", Status: GitChangeDeleted}}},
		},
		{
			name:    "worktree rename carries old path and worktree flag",
			records: []PorcelainRecord{{X: ' ', Y: 'R', Path: "new.md", OldPath: "old.md"}},
			want: PublishSet{Changes: []PublishChange{{
				Path: "new.md", OldPath: "old.md", Status: GitChangeRenamed, WorktreeRename: true,
			}}},
		},
		{
			name:    "index rename does not set WorktreeRename",
			records: []PorcelainRecord{{X: 'R', Y: ' ', Path: "new.md", OldPath: "old.md"}},
			want: PublishSet{Changes: []PublishChange{{
				Path: "new.md", OldPath: "old.md", Status: GitChangeRenamed,
			}}},
		},
		{
			name:    "rename from non-markdown to markdown trips index guard",
			records: []PorcelainRecord{{X: 'R', Y: ' ', Path: "note.md", OldPath: "code.go"}},
			want: PublishSet{
				UnrelatedStagedPresent:  true,
				IgnoredNonMarkdownCount: 1,
			},
		},
		{
			name:    "rename from markdown to non-markdown trips index guard",
			records: []PorcelainRecord{{X: 'R', Y: ' ', Path: "renamed.txt", OldPath: "note.md"}},
			want: PublishSet{
				UnrelatedStagedPresent:  true,
				IgnoredNonMarkdownCount: 1,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Assert.Equal(t, tc.want, computePublishSet(tc.records))
		})
	}
}

func TestSuggestedCommitMessage(t *testing.T) {
	cases := []struct {
		name    string
		changes []PublishChange
		want    string
	}{
		{name: "empty", changes: nil, want: ""},
		{
			name:    "one file uses the filename in the subject",
			changes: []PublishChange{{Path: "README.md", Status: GitChangeModified}},
			want:    "docs: update README.md\n\n- README.md\n",
		},
		{
			name: "three files use the count",
			changes: []PublishChange{
				{Path: "README.md", Status: GitChangeModified},
				{Path: "Daily/2026-05-18.md", Status: GitChangeAdded},
				{Path: "Projects/middleman.md", Status: GitChangeModified},
			},
			want: "docs: update 3 files\n\n- README.md\n- Daily/2026-05-18.md\n- Projects/middleman.md\n",
		},
		{
			name:    "rename body line shows new path only",
			changes: []PublishChange{{Path: "Notes/new.md", OldPath: "Notes/old.md", Status: GitChangeRenamed}},
			want:    "docs: update Notes/new.md\n\n- Notes/new.md\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Assert.Equal(t, tc.want, suggestedCommitMessage(tc.changes))
		})
	}
}

package docs

import (
	"bytes"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePorcelainV1(t *testing.T) {
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
		want []GitStatusEntry
	}{
		{name: "empty stream", in: []byte{}, want: []GitStatusEntry{}},
		{name: "untracked", in: build("?? new.md"), want: []GitStatusEntry{{Path: "new.md", Status: GitStatusUntracked}}},
		{name: "ignored", in: build("!! cache.tmp"), want: []GitStatusEntry{{Path: "cache.tmp", Status: GitStatusIgnored}}},
		{name: "modified in worktree", in: build(" M README.md"), want: []GitStatusEntry{{Path: "README.md", Status: GitStatusModified}}},
		{name: "added in index", in: build("A  add.md"), want: []GitStatusEntry{{Path: "add.md", Status: GitStatusAdded}}},
		{name: "deleted", in: build(" D gone.md"), want: []GitStatusEntry{{Path: "gone.md", Status: GitStatusDeleted}}},
		{
			name: "rename consumes source token",
			in:   build("R  newname.md", "oldname.md", " M other.md"),
			want: []GitStatusEntry{
				{Path: "newname.md", Status: GitStatusRenamed},
				{Path: "other.md", Status: GitStatusModified},
			},
		},
		{
			name: "worktree rename consumes source token",
			in:   build(" R new.md", "old.md", " M other.md"),
			want: []GitStatusEntry{
				{Path: "new.md", Status: GitStatusRenamed},
				{Path: "other.md", Status: GitStatusModified},
			},
		},
		{name: "conflict folds to modified", in: build("UU conflict.md"), want: []GitStatusEntry{{Path: "conflict.md", Status: GitStatusModified}}},
		{name: "both deleted is conflict not deleted", in: build("DD both-deleted.md"), want: []GitStatusEntry{{Path: "both-deleted.md", Status: GitStatusModified}}},
		{name: "both added is conflict not added", in: build("AA both-added.md"), want: []GitStatusEntry{{Path: "both-added.md", Status: GitStatusModified}}},
		{name: "added by us / unmerged is conflict", in: build("AU added-by-us.md"), want: []GitStatusEntry{{Path: "added-by-us.md", Status: GitStatusModified}}},
		{name: "deleted by them / unmerged is conflict", in: build("DU deleted-by-them.md"), want: []GitStatusEntry{{Path: "deleted-by-them.md", Status: GitStatusModified}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePorcelainV1(tc.in)
			require.NoError(t, err)
			Assert.Equal(t, tc.want, got)
		})
	}
}

package landedwork_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestRebaseFileChanges(t *testing.T) {
	for _, tc := range []struct {
		name, path, old, source, replay string
		mode                            bool
		accept                          bool
	}{
		{"binary exact", "data.bin", "old\x00", "new\x00", "new\x00", false, true},
		{"binary differs", "data.bin", "old\x00", "new\x00", "other\x00", false, false},
		{"raw name and bytes", "odd\n\xff.txt", "old\xff\n", "new\xfe\n", "new\xfe\n", false, true},
		{"missing newline", "text", "old\n", "new", "new", false, true},
		{"old missing newline", "text", "old", "new\n", "new\n", false, true},
		{"newline differs", "text", "old\n", "new", "new\n", false, false},
		{"mode differs", "script", "old\n", "new\n", "new\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			f := buildFixture(t, false)
			f.repo.Checkout("-b", "file-base", f.base)
			base := f.repo.CommitFile(tc.path, tc.old, "file base")
			f.repo.Checkout("-b", "file-source")
			f.source = []string{f.repo.CommitFile(tc.path, tc.source, "source")}
			f.repo.Checkout("-b", "file-replay", base)
			f.base = f.repo.CommitFile("unrelated", "context\n", "advance")
			if tc.mode {
				require.NoError(os.Chmod(filepath.Join(f.repo.Root, tc.path), 0755))
			}
			f.head = f.repo.CommitFile(tc.path, tc.replay, "replay")
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "rebase")
			e.Capabilities.Rebase = true
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(err)
			assert.Equal(tc.accept, r.Coverage.Complete)
			if tc.accept {
				assert.Len(r.Landings, 1)
			} else {
				assert.Empty(r.Landings)
			}
		})
	}
}

func TestRebaseOldSideNewline(t *testing.T) {
	for _, tc := range []struct {
		name, sourceOld, replayOld string
		accept                     bool
	}{
		{"matching", "old", "old", true},
		{"source missing newline", "old", "old\n", false},
		{"replay missing newline", "old\n", "old", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			f := buildFixture(t, false)
			f.repo.Checkout("-b", "newline-source", f.base)
			f.repo.CommitFile("text", tc.sourceOld, "source base")
			f.source = []string{f.repo.CommitFile("text", "new\n", "source")}
			f.repo.Checkout("-b", "newline-replay", f.base)
			f.base = f.repo.CommitFile("text", tc.replayOld, "replay base")
			f.head = f.repo.CommitFile("text", "new\n", "replay")
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "rebase")
			e.Capabilities.Rebase = true
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(err)
			assert.Equal(tc.accept, r.Coverage.Complete)
			if tc.accept {
				assert.Len(r.Landings, 1)
				assert.Equal(f.head, r.Coverage.CertifiedHead)
			} else {
				assert.Empty(r.Landings)
				assert.Equal(f.base, r.Coverage.CertifiedHead)
				require.Len(r.Coverage.Gaps, 1)
				assert.Equal("source_correspondence_unproven", r.Coverage.Gaps[0].Reason)
			}
		})
	}
}

func TestRebaseRepeatedOccurrence(t *testing.T) {
	for _, insertion := range []bool{false, true} {
		f := buildFixture(t, false)
		f.repo.Checkout("-b", "occurrence-source", f.base)
		base := f.repo.CommitFile("text", "old\nkeep\nold\nkeep\n", "repeated text")
		source, replay := "new\nkeep\nold\nkeep\n", "old\nkeep\nnew\nkeep\n"
		if insertion {
			source, replay = "old\nnew\nkeep\nold\nkeep\n", "old\nkeep\nold\nnew\nkeep\n"
		}
		f.source = []string{f.repo.CommitFile("text", source, "source")}
		f.repo.Checkout("-b", "occurrence-replay", base)
		f.base = f.repo.CommitFile("other", "unrelated\n", "advance")
		f.head = f.repo.CommitFile("text", replay, "replay")
		ctx, p := f.prepare(t)
		e := fixtureEvidence(f, p, "rebase")
		e.Capabilities.Rebase = true
		r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
		require := require.New(t)
		assert := assert.New(t)
		require.NoError(err)
		assert.Empty(r.Landings)
		assert.False(r.Coverage.Complete)
		assert.Equal(f.base, r.Coverage.CertifiedHead)
		require.Len(r.Coverage.Gaps, 1)
		assert.Equal("edits_unavailable", r.Coverage.Gaps[0].Reason)
	}
}

func TestRebaseDuplicateEdits(t *testing.T) {
	f := buildFixture(t, false)
	f.repo.Checkout("-b", "repeat-source", f.base)
	f.source = []string{f.repo.CommitFile("work.txt", "new\nkeep\n", "first")}
	f.source = append(f.source, f.repo.CommitFile("work.txt", "old\nkeep\n", "undo"))
	f.source = append(f.source, f.repo.CommitFile("work.txt", "new\nkeep\n", "repeat"))
	f.repo.Checkout("-b", "repeat-replay", f.base)
	f.base = f.repo.CommitFile("unrelated", "context\n", "advance")
	f.repo.CommitFile("work.txt", "new\nkeep\n", "replay first")
	f.repo.CommitFile("work.txt", "old\nkeep\n", "replay undo")
	f.head = f.repo.CommitFile("work.txt", "new\nkeep\n", "replay repeat")
	ctx, p := f.prepare(t)
	e := fixtureEvidence(f, p, "rebase")
	e.Capabilities.Rebase = true
	r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
	require.NoError(t, err)
	assert.Empty(t, r.Landings)
	assert.False(t, r.Coverage.Complete)
}

func TestRebase(t *testing.T) {
	for _, variant := range []string{"offset", "changed bytes", "whitespace", "newline", "empty", "binary"} {
		t.Run(variant, func(t *testing.T) {
			f := buildFixture(t, false)
			f.repo.Checkout("-b", "replay", f.base)
			f.base = f.repo.CommitFile("other.txt", "unrelated\n", "advance")
			prefix := ""
			if variant == "offset" {
				prefix = "context\n"
				f.base = f.repo.CommitFile("work.txt", prefix+"old\nkeep\n", "shift")
			}
			if variant == "empty" {
				f.repo.Checkout("topic")
				f.repo.Run("commit", "--allow-empty", "-m", "empty source")
				f.source = append(slices.Clone(f.source), f.repo.Head())
				f.repo.Checkout("replay")
			}
			first := f.repo.CommitFile("work.txt", prefix+"new\nkeep\n", "rewritten first")
			content := prefix + "new\nkeep\none\ntwo\n"
			switch variant {
			case "changed bytes":
				content = prefix + "new\nkeep\none\nother\n"
			case "whitespace":
				content = prefix + "new\nkeep\none\ntwo \n"
			case "newline":
				content = prefix + "new\nkeep\none\ntwo"
			case "binary":
				content += "\x00"
			}
			last := f.repo.CommitFile("work.txt", content, "rewritten last")
			introduced := []string{first, last}
			if variant == "empty" {
				f.repo.Run("commit", "--allow-empty", "-m", "empty replay")
				introduced = append(introduced, f.repo.Head())
			}
			f.head = f.repo.Head()
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "rebase")
			e.Capabilities.Rebase = true
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			if variant != "offset" {
				assert.Empty(r.Landings)
				assert.False(r.Coverage.Complete)
				assert.Equal(f.base, r.Coverage.CertifiedHead)
				assert.NotEmpty(r.Coverage.Gaps)
				return
			}
			require.Len(r.Landings, 1)
			assert.Equal(introduced, r.Landings[0].Introduced)
			assert.Equal(f.source, r.Landings[0].Source)
			assert.Equal("3\t1\twork.txt", f.repo.Run("diff", "--numstat", r.Landings[0].Before, r.Landings[0].Terminal))
			assert.True(r.Coverage.Complete)
			assert.Equal(f.head, r.Coverage.CertifiedHead)
		})
	}
}

func TestFastForward(t *testing.T) {
	for _, variant := range []string{"complete", "disabled", "reordered", "partial", "missing method", "missing terminal", "crosses base", "merge member"} {
		t.Run(variant, func(t *testing.T) {
			f := buildFixture(t, false)
			f.head = f.source[1]
			if variant == "crosses base" {
				f.base = f.source[0]
			}
			if variant == "merge member" {
				f.head = f.repo.Head()
				f.source = append(slices.Clone(f.source), f.head)
			}
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "fast_forward")
			e.Capabilities.FastForward = true
			switch variant {
			case "disabled":
				e.Capabilities.FastForward = false
			case "reordered":
				e.Candidates[0].Source = []string{f.source[1], f.source[0]}
			case "partial":
				e.Candidates[0].SourceComplete = false
			case "missing method":
				e.Candidates[0].MethodEvidence = ""
			case "missing terminal":
				e.Candidates[0].TerminalEvidence = ""
			}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			if variant != "complete" {
				assert.Empty(r.Landings)
				assert.False(r.Coverage.Complete)
				assert.Equal(f.base, r.Coverage.CertifiedHead)
				assert.NotEmpty(r.Coverage.Gaps)
				return
			}
			assert.Equal([]landedwork.Landing{{CandidateID: "7", Method: "fast_forward", Before: f.base,
				Terminal: f.head, Source: f.source, Introduced: f.source}}, r.Landings)
			assert.True(r.Coverage.Complete)
			assert.Equal(f.head, r.Coverage.CertifiedHead)
			assert.Empty(r.Unattributed)
			assert.Equal("3\t1\twork.txt", f.repo.Run("diff", "--numstat", r.Landings[0].Before, r.Landings[0].Terminal))
		})
	}
}

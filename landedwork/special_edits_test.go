package landedwork_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestAutomaticSpecialObjects(t *testing.T) {
	for _, mode := range []string{"120000", "160000"} {
		for _, match := range []bool{false, true} {
			f := buildFixture(t, true)
			ctx, _ := f.prepare(t)
			require, assert := require.New(t), assert.New(t)
			ids := []string{f.base, f.source[0], f.source[1]}
			if mode == "120000" {
				// Index entries test symlink bytes without OS symlink privileges.
				for i, target := range []string{"old-target", "new-target", "other-target"} {
					out, _, err := f.repo.Runner.Run(ctx, f.repo.Root, strings.NewReader(target), "hash-object", "-w", "--stdin")
					require.NoError(err)
					ids[i] = strings.TrimSpace(string(out))
				}
			}
			f.repo.Checkout("-b", "special", f.base)
			f.repo.Run("update-index", "--add", "--cacheinfo", mode, ids[0], "entry")
			f.repo.Run("commit", "-m", "entry base")
			f.base = f.repo.Head()
			f.repo.Checkout("-b", "special-source")
			f.repo.Run("update-index", "--cacheinfo", mode, ids[1], "entry")
			f.repo.Run("commit", "-m", "entry source")
			f.source = []string{f.repo.Head()}
			f.repo.Checkout("-b", "special-target", f.base)
			target := ids[2]
			if match {
				target = ids[1]
			}
			f.repo.Run("update-index", "--cacheinfo", mode, target, "entry")
			f.repo.Run("commit", "-m", "entry replay")
			f.head = f.repo.Head()
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "")
			e.Candidates[0].MethodEvidence = ""
			e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(err)
			assert.Empty(r.DirectPushes)
			assert.Equal(match, r.Coverage.Complete)
			if match {
				require.Len(r.Landings, 1)
				assert.Equal([]string{"rebase", "squash"}, r.Landings[0].Proofs)
				assert.Equal([]string{f.head}, r.Landings[0].Spine)
				assert.Equal(f.head, r.Coverage.CertifiedHead)
			} else {
				assert.Empty(r.Landings)
				require.Len(r.Coverage.Gaps, 1)
				assert.Equal("source_correspondence_unproven", r.Coverage.Gaps[0].Reason)
				assert.Equal(f.base, r.Coverage.CertifiedHead)
			}
		}
	}
}

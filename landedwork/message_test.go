package landedwork_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
	gittest "go.kenn.io/kit/git/test"
)

func messageRepo(t *testing.T) *gittest.Repo {
	t.Helper()
	r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	return r
}

func messageContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// Messages reads what each commit says, keeps the trailer block apart, and
// returns them in the order asked for.
func TestMessagesSplitTheTrailerBlock(t *testing.T) {
	r := messageRepo(t)
	trailered := r.CommitFile("a.txt", "a", "Add a feature\n\nExplain why it matters.\n\nCo-authored-by: Person A <a@example.com>\nSigned-off-by: Person B\n  <b@example.com>")
	plain := r.CommitFile("b.txt", "b", "Fix: one paragraph that looks like a trailer")
	prose := r.CommitFile("c.txt", "c", "Update docs\n\nSee: the guide\nand more prose here.")
	got, err := landedwork.Messages(messageContext(t), r.Root, []string{plain, trailered, prose}, 1<<16, landedwork.Limits{InputBytes: 1 << 20})
	require.NoError(t, err)
	assert.Equal(t, []landedwork.Message{
		{Commit: plain, Text: "Fix: one paragraph that looks like a trailer"},
		{Commit: trailered, Text: "Add a feature\n\nExplain why it matters.", Trailers: []landedwork.Trailer{
			{Key: "Co-authored-by", Value: "Person A <a@example.com>"},
			{Key: "Signed-off-by", Value: "Person B <b@example.com>"},
		}},
		{Commit: prose, Text: "Update docs\n\nSee: the guide\nand more prose here."},
	}, got)
}

// The text is cut at the bound without splitting a character, and the
// trailers after the cut are still read.
func TestMessagesCutTheTextButKeepTheTrailers(t *testing.T) {
	r := messageRepo(t)
	commit := r.CommitFile("a.txt", "a", "Résumé support\n\n"+strings.Repeat("x", 40)+"\n\nSigned-off-by: Person B <b@example.com>")
	got, err := landedwork.Messages(messageContext(t), r.Root, []string{commit}, 2, landedwork.Limits{InputBytes: 1 << 20})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "R", got[0].Text, "the two-byte é does not fit and is not split")
	assert.True(t, got[0].Truncated)
	assert.Equal(t, []landedwork.Trailer{{Key: "Signed-off-by", Value: "Person B <b@example.com>"}}, got[0].Trailers)
}

func TestMessagesRefuseWhatTheyCannotRead(t *testing.T) {
	r := messageRepo(t)
	commit := r.CommitFile("a.txt", "a", "Add a feature")
	ctx := messageContext(t)
	limits := landedwork.Limits{InputBytes: 1 << 20}

	_, err := landedwork.Messages(ctx, r.Root, []string{strings.Repeat("0", len(commit))}, 64, limits)
	require.Error(t, err, "a commit the repository does not hold")

	_, err = landedwork.Messages(ctx, r.Root, []string{"HEAD"}, 64, limits)
	require.Error(t, err, "a name that is not an object ID")

	_, err = landedwork.Messages(ctx, r.Root, []string{commit}, 64, landedwork.Limits{InputBytes: 8})
	require.ErrorIs(t, err, landedwork.ErrInputBudget)
}

func TestParseIdentity(t *testing.T) {
	for value, want := range map[string]struct {
		name, email string
		ok          bool
	}{
		"Person A <a@example.com>":      {"Person A", "a@example.com", true},
		"  Person <A> <a@example.com> ": {"Person <A>", "a@example.com", true},
		"<a@example.com>":               {},
		"Person A":                      {},
		"Person A <>":                   {},
		"Person A <not an address>":     {},
		"Person A <a@example.com":       {},
	} {
		t.Run(value, func(t *testing.T) {
			name, email, ok := landedwork.ParseIdentity(value)
			assert.Equal(t, want.ok, ok)
			assert.Equal(t, want.name, name)
			assert.Equal(t, want.email, email)
		})
	}
}

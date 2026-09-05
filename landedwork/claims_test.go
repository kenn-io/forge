package landedwork_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
	"testing"
)

func TestGitClaimsUseOnlyTerminalTrailersAndKeepBytes(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	author := landedwork.Signature{Byline: []byte("Author A <author@example.org>"), Email: []byte("author@example.org")}
	message := []byte("Subject mentions Co-authored-by: Fake <fake@example.org>\n\n> Co-authored-by: Quoted <quoted@example.org>\n\nCo-authored-by: Duplicate <AUTHOR@EXAMPLE.ORG>\nCo-authored-by: Peer <21+Peer@users.noreply.GitHub.com>\nCo-authored-by: Peer Again <21+peer@users.noreply.github.com>\nCo-authored-by: Raw <\xff@EXAMPLE.ORG>\nSigned-off-by: Reviewer <reviewer@example.org>\n")
	claims := landedwork.GitClaims(author, message)
	require.Len(claims, 3)
	assert.Equal(landedwork.RoleAuthor, claims[0].Role)
	assert.Equal(landedwork.RoleCoauthor, claims[1].Role)
	assert.Equal(landedwork.ClaimProviderUserID, claims[1].Kind)
	assert.Equal(landedwork.AssuranceUnverified, claims[1].Assurance)
	assert.Equal(platform.KindGitHub, claims[1].Provider)
	assert.Equal("github.com", claims[1].Instance)
	assert.Equal("21", claims[1].ProviderUserID)
	normalized, err := claims[2].Email.Bytes()
	require.NoError(err)
	assert.Equal([]byte("\xff@example.org"), normalized)
	raw, err := claims[2].RawEmail.Bytes()
	require.NoError(err)
	assert.Equal([]byte("\xff@EXAMPLE.ORG"), raw)
}

func TestGitClaimsRejectNonTrailerAndEmptyName(t *testing.T) {
	for _, message := range []string{
		"Co-authored-by: Subject <subject@example.org>\n",
		"Subject\n\nCo-authored-by: Prose <prose@example.org>\nThis is ordinary prose.\n",
		"Subject\n\nCo-authored-by: <empty-name@example.org>\n",
		"Subject\n\nCo-authored-by: Missing brackets bare@example.org\n",
	} {
		claims := landedwork.GitClaims(landedwork.Signature{Email: []byte("primary@example.org")}, []byte(message))
		assert.Len(t, claims, 1)
	}
}

func TestDeclaredRevertCandidatesRequireExactFullLines(t *testing.T) {
	message := []byte("Subject\n\nThis reverts commit 1111111111111111111111111111111111111111.\nThis reverts commit 1111111111111111111111111111111111111111.\n> This reverts commit 2222222222222222222222222222222222222222.\nThis reverts commit abc123.\n")
	assert.Equal(t, []string{"1111111111111111111111111111111111111111"}, landedwork.DeclaredRevertCandidates(message))
}

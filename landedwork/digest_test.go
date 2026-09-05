package landedwork_test

import (
	"bytes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
	"testing"
)

func TestCanonicalEvidenceHashesDecodedBytesAndRejectsNoncanonicalInput(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	result := landedwork.Result{Landings: []landedwork.Landing{{Churn: landedwork.Churn{Files: []landedwork.FileChange{{OldPath: platform.RawBytes{Base64: "/wA="}}}}}}}
	var encoded bytes.Buffer
	require.NoError(landedwork.WriteCanonicalEvidence(&encoded, landedwork.Request{}, result))
	assert.Contains(encoded.String(), string([]byte{255, 0}))
	digest, err := landedwork.Digest(landedwork.Request{}, result)
	require.NoError(err)
	// Pin the v1 metadata-plus-length-framed-bytes encoding independently of
	// the equality/permutation checks below.
	assert.Equal("a714b884b4de1c6d7a5bf1a7085ce3f6dd6f868844b858a6b80ec0e2b38d2608", digest)
	result.Landings[0].Churn.Files[0].OldPath.Base64 = "/x==" // Decodes ff only with ignored pad bits.
	_, err = landedwork.Digest(landedwork.Request{}, result)
	require.Error(err)
}

func TestDigestCanonicalizesSetsButPreservesOrderedSources(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	request := landedwork.Request{Policy: landedwork.CodePolicy, Snapshot: platform.LandingSnapshot{
		Candidates: []platform.LandingCandidate{{ID: "2", SourceCommits: []string{"a", "b"}}, {ID: "1"}},
	}}
	result := landedwork.Result{Schema: landedwork.Schema, Landings: []landedwork.Landing{
		{Terminal: "b", Claims: landedwork.GitClaims(landedwork.Signature{Email: []byte("\xff@example.org")}, nil)},
		{Terminal: "a"},
	}}
	first, err := landedwork.Digest(request, result)
	require.NoError(err)
	request.Snapshot.Candidates[0], request.Snapshot.Candidates[1] = request.Snapshot.Candidates[1], request.Snapshot.Candidates[0]
	result.Landings[0], result.Landings[1] = result.Landings[1], result.Landings[0]
	result.Digest = "not part of the digest"
	second, err := landedwork.Digest(request, result)
	require.NoError(err)
	assert.Equal(first, second)
	request.Snapshot.Candidates[1].SourceCommits = []string{"b", "a"}
	reordered, err := landedwork.Digest(request, result)
	require.NoError(err)
	assert.NotEqual(first, reordered)
	request.Snapshot.Candidates[1].SourceCommits = []string{"a", "b"}
	request.Snapshot.Candidates[0].Additions = new(int64(0))
	explicitZero, err := landedwork.Digest(request, result)
	require.NoError(err)
	assert.NotEqual(first, explicitZero)
}

package worklist_test

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/worklist"
)

// pythonFragment was encoded outside Go, with Python's zlib (raw DEFLATE,
// wbits -15) and urlsafe base64 without padding, from compact JSON of:
//
//	{"title":"Ready to merge","recipe":"open PRs with passing CI","created_by":"triage-bot",
//	 "items":[{"item":{"type":"pr","provider":"github","owner":"acme","name":"widgets","number":42},
//	           "score":8,"section":"Ready","note":"Approved; CI green.","caveat":"Check the migration."},
//	          {"item":{"type":"issue","provider":"gitlab","platform_host":"gitlab.example.com",
//	           "owner":"group/sub","name":"tool","number":7}}]}
const pythonFragment = "v1.ZY9BS8QwEIX_Ssi5VhBBqSfZk7dlryJLmoxpsMmEyXRrKf3vTorKwt4m73uZeW_VHHgE3ekTGLcoRhWBPOhGE9iQK8EMSR1PRc2BB5VNKSF5dXgTjyUwDO7cL-JjCsbDXY8sJDDEorv3dZ90t2pe9m2ZhGbCS3BA8vaydOpFwzntgrGxnk8mVvscnAcuVZhiXw2PD1uji0US_CwTWA6Y_hpUI3L9-ZrrFXAvElV5AkhtTWwuElnwYQD7pXgAFYMnU3e0emtu8oZSJriJPJoaOY-GP5HiecDC_6CFbxPzCK3FeNXLE075vuxdf8sx4njV7GnbPrYf"

func TestParseFragmentReadsALinkEncodedOutsideGo(t *testing.T) {
	assert := assert.New(t)

	list, err := worklist.ParseFragment("#" + pythonFragment)
	require.NoError(t, err)

	assert.Equal("Ready to merge", list.Title)
	assert.Equal("open PRs with passing CI", list.Recipe)
	assert.Equal("triage-bot", list.CreatedBy)
	require.Len(t, list.Items, 2)
	assert.Equal(worklist.Item{
		Item:  worklist.ItemRef{Type: "pr", Provider: "github", Owner: "acme", Name: "widgets", Number: 42},
		Score: new(8), Section: "Ready", Note: "Approved; CI green.", Caveat: "Check the migration.",
	}, list.Items[0])
	assert.Equal(worklist.Item{Item: worklist.ItemRef{
		Type: "issue", Provider: "gitlab", PlatformHost: "gitlab.example.com",
		Owner: "group/sub", Name: "tool", Number: 7,
	}}, list.Items[1])
}

func TestKennLinkOpensImportPageWithAReadableFragment(t *testing.T) {
	list := worklist.List{
		Title: "Needs review", Recipe: "stale reviews",
		Items: []worklist.Item{{
			Item: worklist.ItemRef{
				Type: "pr", Provider: "github", PlatformRepoID: 1001,
				Owner: "acme", Name: "widgets", Number: 5,
			},
			Score: new(3), Note: "No reviewer yet.",
		}},
	}

	link, err := worklist.KennLink(list)
	require.NoError(t, err)

	fragment, ok := strings.CutPrefix(link, "kenn://kenn-forge/worklists/import#v1.")
	require.True(t, ok, link)
	assert.NotContains(t, fragment, "=", "base64url is unpadded")
	parsed, err := worklist.ParseFragment("v1." + fragment)
	require.NoError(t, err)
	assert.Equal(t, list, parsed)
}

func TestParseFragmentRejectsWhatItCannotRead(t *testing.T) {
	deflated := func(data []byte) string {
		var buf bytes.Buffer
		writer, err := flate.NewWriter(&buf, flate.BestCompression)
		require.NoError(t, err)
		_, err = writer.Write(data)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		return "v1." + base64.RawURLEncoding.EncodeToString(buf.Bytes())
	}
	tests := []struct {
		name     string
		fragment string
	}{
		{"unknown version", "v2." + strings.TrimPrefix(pythonFragment, "v1.")},
		{"no version", strings.TrimPrefix(pythonFragment, "v1.")},
		// Valid JSON whose title only Validate would reject, so only the
		// decompression bound reports it as unreadable.
		{"decompresses past the bound", deflated([]byte(`{"title":"` + strings.Repeat("a", 9<<20) + `","items":[]}`))},
		{"too long", oversizedFragment(t)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := worklist.ParseFragment(tt.fragment)
			require.ErrorIs(t, err, worklist.ErrInvalidFragment)
		})
	}
}

func TestParseFragmentReportsTheFieldOfAReadableListOverALimit(t *testing.T) {
	var buf bytes.Buffer
	writer, err := flate.NewWriter(&buf, flate.BestCompression)
	require.NoError(t, err)
	_, err = writer.Write([]byte(`{"title":"T","recipe":"","items":[{"item":` +
		`{"type":"pr","provider":"github","owner":"acme","name":"widgets","number":1},"score":11}]}`))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	_, err = worklist.ParseFragment("v1." + base64.RawURLEncoding.EncodeToString(buf.Bytes()))

	require.NotErrorIs(t, err, worklist.ErrInvalidFragment)
	fieldErr, ok := errors.AsType[*worklist.FieldError](err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, "items[0].score", fieldErr.Field)
}

func TestFragmentRefusesAListTooLargeForForgeToRead(t *testing.T) {
	// Control characters encode as six-byte JSON escapes but compress to
	// almost nothing, so only the decompressed bound catches this list.
	escaped := worklist.List{Title: "Escapes"}
	for number := 1; number <= worklist.MaxItems; number++ {
		escaped.Items = append(escaped.Items, worklist.Item{
			Item: worklist.ItemRef{Type: "pr", Provider: "github", Owner: "acme", Name: "widgets", Number: number},
			Note: strings.Repeat("\x01", worklist.MaxText), Caveat: strings.Repeat("\x01", worklist.MaxText),
		})
	}
	tests := []struct {
		name string
		list worklist.List
	}{
		{"link too long", incompressibleList()},
		{"list JSON too large", escaped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := worklist.Fragment(tt.list)

			require.ErrorIs(t, err, worklist.ErrTooLarge)
		})
	}
}

// oversizedFragment encodes, without Fragment's own bound, a list whose
// fragment is longer than Forge accepts.
func oversizedFragment(t *testing.T) string {
	data, err := json.Marshal(incompressibleList())
	require.NoError(t, err)
	var buf bytes.Buffer
	writer, err := flate.NewWriter(&buf, flate.BestCompression)
	require.NoError(t, err)
	_, err = writer.Write(data)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	fragment := "v1." + base64.RawURLEncoding.EncodeToString(buf.Bytes())
	require.Greater(t, len(fragment), worklist.MaxFragmentLength)
	return fragment
}

// incompressibleList stays within every field limit, but its random notes
// leave too little for DEFLATE to remove.
func incompressibleList() worklist.List {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	random := rand.New(rand.NewPCG(1, 2))
	note := func() string {
		text := make([]byte, worklist.MaxText)
		for i := range text {
			text[i] = alphabet[random.IntN(len(alphabet))]
		}
		return string(text)
	}
	list := worklist.List{Title: "Large"}
	for number := 1; number <= 150; number++ {
		list.Items = append(list.Items, worklist.Item{
			Item: worklist.ItemRef{Type: "pr", Provider: "github", Owner: "acme", Name: "widgets", Number: number},
			Note: note(),
		})
	}
	return list
}

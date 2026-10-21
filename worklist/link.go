package worklist

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
)

// A link carries a list in its fragment as "v1." followed by the list's JSON,
// compressed with raw DEFLATE (RFC 1951) and encoded as unpadded base64url.
// Browsers never send a fragment to a server, so its length is bounded only
// by where the link travels; GitHub renders a link of at most 32,768
// characters.
//
// Compression is raw DEFLATE, not zstd: links are also built in browsers
// and scripts, where CompressionStream offers only gzip, deflate, and
// deflate-raw in every major browser, and Go's standard library has no zstd.
// On measured lists with distinct prose, DEFLATE shrinks the link about 3x,
// enough for a 200-item list (about 24,000 characters) to fit GitHub's limit.
const (
	fragmentPrefix = "v1."

	// KennImportURL opens Forge's import page through the Kenn desktop app,
	// which sends kenn://kenn-forge/ links to the recipient's own Forge.
	KennImportURL = "kenn://kenn-forge/worklists/import"
	// ImportPath is the import page's path under a Forge origin.
	ImportPath = "/worklists/import"

	// MaxFragmentLength bounds the encoded fragment Forge accepts.
	MaxFragmentLength = 512 << 10
	// maxListJSON bounds the decompressed list. A list at every limit with
	// four-byte characters stays under it.
	maxListJSON = 8 << 20
)

var (
	// ErrInvalidFragment reports a fragment that is not a worklist link this
	// version of Forge can read.
	ErrInvalidFragment = errors.New("not a worklist link")
	// ErrTooLarge reports a list whose link would be longer than Forge
	// accepts. Fragment never returns a link ParseFragment rejects.
	ErrTooLarge = errors.New("worklist too large for a link")
)

// Fragment validates the list and encodes it for a link's fragment, without
// the leading '#'. It does not check where the link will travel; GitHub, for
// one, stops rendering links past 32,768 characters.
func Fragment(l List) (string, error) {
	if err := l.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(l)
	if err != nil {
		return "", fmt.Errorf("encode worklist: %w", err)
	}
	if len(data) > maxListJSON {
		return "", fmt.Errorf("%w: list larger than %d bytes", ErrTooLarge, maxListJSON)
	}
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.BestCompression)
	if err != nil {
		return "", fmt.Errorf("compress worklist: %w", err)
	}
	if _, err := writer.Write(data); err != nil {
		return "", fmt.Errorf("compress worklist: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("compress worklist: %w", err)
	}
	fragment := fragmentPrefix + base64.RawURLEncoding.EncodeToString(compressed.Bytes())
	if len(fragment) > MaxFragmentLength {
		return "", fmt.Errorf("%w: link longer than %d characters", ErrTooLarge, MaxFragmentLength)
	}
	return fragment, nil
}

// KennLink returns a kenn:// link that opens the list in the recipient's
// Forge for them to review and create.
func KennLink(l List) (string, error) {
	fragment, err := Fragment(l)
	if err != nil {
		return "", err
	}
	return KennImportURL + "#" + fragment, nil
}

// ParseFragment decodes and validates a fragment, with or without its
// leading '#'. Errors wrap ErrInvalidFragment, or are a *FieldError for a
// readable list that breaks a limit.
func ParseFragment(fragment string) (List, error) {
	fragment = strings.TrimPrefix(fragment, "#")
	if len(fragment) > MaxFragmentLength {
		return List{}, fmt.Errorf("%w: longer than %d characters", ErrInvalidFragment, MaxFragmentLength)
	}
	encoded, ok := strings.CutPrefix(fragment, fragmentPrefix)
	if !ok {
		return List{}, fmt.Errorf("%w: unknown version", ErrInvalidFragment)
	}
	compressed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return List{}, fmt.Errorf("%w: %w", ErrInvalidFragment, err)
	}
	data, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(compressed)), maxListJSON+1))
	if err != nil {
		return List{}, fmt.Errorf("%w: %w", ErrInvalidFragment, err)
	}
	if len(data) > maxListJSON {
		return List{}, fmt.Errorf("%w: list larger than %d bytes", ErrInvalidFragment, maxListJSON)
	}
	var l List
	if err := json.Unmarshal(data, &l); err != nil {
		return List{}, fmt.Errorf("%w: %w", ErrInvalidFragment, err)
	}
	if err := l.Validate(); err != nil {
		return List{}, err
	}
	return l, nil
}

package report

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"

	"go.kenn.io/forge/platform"
)

// outputBuffer enforces the report envelope before growing the rendered body.
// A sticky failure also covers Markdown's deliberately ignored fmt results.
type outputBuffer struct {
	strings.Builder
	err error
}

func (b *outputBuffer) Write(data []byte) (int, error) {
	if b.err == nil && int64(b.Len())+int64(len(data)) > MaxDetailedTextBytes {
		b.err = platform.ErrPageLimit
	}
	if b.err != nil {
		return 0, b.err
	}
	return b.Builder.Write(data)
}

func (b *outputBuffer) WriteString(data string) (int, error) {
	if b.err == nil && int64(b.Len())+int64(len(data)) > MaxDetailedTextBytes {
		b.err = platform.ErrPageLimit
	}
	if b.err != nil {
		return 0, b.err
	}
	return b.Builder.WriteString(data)
}

// RenderJSON preserves the archive's existing wire semantics while streaming
// encoding through the same finite envelope as Markdown.
func RenderJSON(model Model) (string, error) {
	var out outputBuffer
	if err := json.MarshalWrite(&out, model, jsonv1.DefaultOptionsV1(), jsontext.WithIndent("  ")); err != nil {
		return "", err
	}
	if _, err := out.WriteString("\n"); err != nil {
		return "", err
	}
	return out.String(), nil
}

package landedwork

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"strings"

	gitdiff "github.com/sourcegraph/go-diff/diff"
	"go.kenn.io/forge/platform"
)

// Patch contains version-1 ordered edit records, not a fuzzy patch ID. Only
// hunk offsets and unchanged context are omitted. Empty patches cannot prove
// rewritten commit identity.
type Patch struct {
	Bytes []byte
	Empty bool
}
type textEdit struct {
	Deleted platform.RawBytes `json:"deleted"`
	Added   platform.RawBytes `json:"added"`
}
type fileEdit struct {
	OldPath platform.RawBytes `json:"old_path"`
	NewPath platform.RawBytes `json:"new_path"`
	OldMode string            `json:"old_mode"`
	NewMode string            `json:"new_mode"`
	Binary  bool              `json:"binary"`
	OldBlob string            `json:"old_blob,omitempty"`
	NewBlob string            `json:"new_blob,omitempty"`
	Edits   []textEdit        `json:"edits"`
}

func (g *Git) Patch(ctx context.Context, parent, head string, meter *platform.Meter) (Patch, error) {
	files, err := g.Diff(ctx, parent, head, meter)
	if err != nil {
		return Patch{}, err
	}
	args := []string{"diff-tree", "--no-commit-id", "-r", "--patch", "--full-index", "--unified=0", "--no-ext-diff", "--no-textconv", "--no-color", "--find-renames=100%", "--diff-algorithm=myers", "--ignore-submodules=none", "--src-prefix=a/", "--dst-prefix=b/"}
	if parent == "" {
		args = append(args, "--root", head)
	} else {
		args = append(args, parent, head)
	}
	args = append(args, "--")
	output, err := g.run(ctx, meter, nil, args...)
	if err != nil {
		return Patch{}, err
	}
	reader := gitdiff.NewMultiFileDiffReader(bytes.NewReader(output))
	edits := make([]fileEdit, 0, len(files.Files))
	invalid := errors.New("patch correspondence unavailable")
	for _, file := range files.Files {
		parsed, trailing, err := reader.ReadFileWithTrailingContent()
		if err != nil || trailing != "" {
			return Patch{}, invalid
		}
		oldPath, _ := file.OldPath.Bytes()
		newPath, _ := file.NewPath.Bytes()
		oldName, newName := strings.TrimPrefix(parsed.OrigName, "a/"), strings.TrimPrefix(parsed.NewName, "b/")
		if parsed.OrigName == "/dev/null" {
			oldName = ""
		}
		if parsed.NewName == "/dev/null" {
			newName = ""
		}
		if oldName != string(oldPath) || newName != string(newPath) {
			return Patch{}, invalid
		}
		entry := fileEdit{OldPath: file.OldPath, NewPath: file.NewPath, OldMode: file.OldMode, NewMode: file.NewMode, Binary: file.Binary, Edits: []textEdit{}}
		if file.Binary {
			entry.OldBlob = file.OldBlob
			entry.NewBlob = file.NewBlob
		}
		for _, hunk := range parsed.Hunks {
			var removed, added bytes.Buffer
			body := hunk.Body
			offset := 0
			for len(body) > 0 {
				line, rest, newline := bytes.Cut(body, []byte{'\n'})
				consumed := len(line)
				if newline {
					consumed++
				}
				if len(line) == 0 {
					return Patch{}, invalid
				}
				content := line[1:]
				switch line[0] {
				case '-':
					removed.Write(content)
					if newline && offset+consumed != int(hunk.OrigNoNewlineAt) {
						removed.WriteByte('\n')
					}
				case '+':
					added.Write(content)
					if newline {
						added.WriteByte('\n')
					}
				default:
					return Patch{}, invalid // unified=0 must not introduce context
				}
				body = rest
				offset += consumed
			}
			entry.Edits = append(entry.Edits, textEdit{Deleted: platform.NewRawBytes(removed.Bytes()), Added: platform.NewRawBytes(added.Bytes())})
		}
		edits = append(edits, entry)
	}
	if _, trailing, err := reader.ReadFileWithTrailingContent(); err != io.EOF || trailing != "" {
		return Patch{}, invalid
	}
	encoded, err := json.Marshal(edits, json.Deterministic(true))
	if err != nil {
		return Patch{}, err
	}
	if err := meter.Bytes(int64(len(encoded))); err != nil {
		return Patch{}, err
	}
	return Patch{Bytes: encoded, Empty: len(edits) == 0}, nil
}

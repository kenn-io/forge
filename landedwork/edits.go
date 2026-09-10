package landedwork

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"

	gitdiff "github.com/sourcegraph/go-diff/diff"
)

var errEdits = errors.New("edit correspondence unavailable")

type editRun struct {
	removed, added string
	anchor         insertionAnchor
}

type insertionAnchor struct {
	before, after  string
	atStart, atEnd bool
}
type fileEdit struct {
	path, oldMode, newMode, oldID, newID string
	binary                               bool
	runs                                 []editRun
}

func sameEdits(a, b []fileEdit) bool {
	return slices.EqualFunc(a, b, func(a, b fileEdit) bool {
		if a.path != b.path || a.oldMode != b.oldMode || a.newMode != b.newMode || a.binary != b.binary {
			return false
		}
		if a.binary {
			return a.oldID == b.oldID && a.newID == b.newID
		}
		return slices.Equal(a.runs, b.runs)
	})
}

// Callers physically verify the boundary commits before comparing their trees.
// A different path/mode inventory disproves correspondence without parsing text.
func (v *objectView) compareTreeEdits(ctx context.Context, sourceBefore, source, before, after string) ([]fileEdit, error) {
	a, err := v.treeFiles(ctx, sourceBefore, source)
	if err != nil {
		return nil, err
	}
	b, err := v.treeFiles(ctx, before, after)
	if err != nil {
		return nil, err
	}
	if !slices.EqualFunc(a, b, func(a, b fileEdit) bool {
		return a.path == b.path && a.oldMode == b.oldMode && a.newMode == b.newMode
	}) {
		return nil, errCorrespondence
	}
	if len(a) == 0 {
		return nil, errEdits
	}
	for i := range a {
		if err := v.fileEdits(ctx, sourceBefore, source, &a[i]); err != nil {
			return nil, err
		}
		if err := v.fileEdits(ctx, before, after, &b[i]); err != nil {
			return nil, err
		}
	}
	if !sameEdits(a, b) {
		return nil, errCorrespondence
	}
	return a, nil
}

func (v *objectView) treeFiles(ctx context.Context, before, after string) ([]fileEdit, error) {
	raw, err := v.run(ctx, "diff", "--raw", "-z", "--no-abbrev", "--no-renames", "--no-ext-diff", "--no-textconv", before, after, "--")
	if err != nil {
		return nil, err
	}
	return parseEditFiles(raw)
}

// Metadata is NUL-delimited; Git path quoting never changes the comparison key.
func parseEditFiles(raw []byte) ([]fileEdit, error) {
	var files []fileEdit
	for len(raw) > 0 {
		header, tail, ok := bytes.Cut(raw, []byte{0})
		if !ok {
			return nil, errEdits
		}
		path, rest, ok := bytes.Cut(tail, []byte{0})
		fields := strings.Fields(string(header))
		if !ok || len(path) == 0 || len(fields) != 5 || !strings.HasPrefix(fields[0], ":") {
			return nil, errEdits
		}
		oldMode, newMode := strings.TrimPrefix(fields[0], ":"), fields[1]
		if !editMode(oldMode) || !editMode(newMode) || !objectID(fields[2]) || !objectID(fields[3]) || !strings.Contains("ADMT", fields[4]) || len(fields[4]) != 1 {
			return nil, errEdits
		}
		files = append(files, fileEdit{path: string(path), oldMode: oldMode, newMode: newMode, oldID: fields[2], newID: fields[3]})
		raw = rest
	}
	slices.SortFunc(files, func(a, b fileEdit) int { return strings.Compare(a.path, b.path) })
	for i := 1; i < len(files); i++ {
		if files[i-1].path == files[i].path {
			return nil, errEdits
		}
	}
	return files, nil
}

func editMode(mode string) bool {
	switch mode {
	case "000000", "100644", "100755", "120000", "160000":
		return true
	}
	return false
}

func (v *objectView) fileEdits(ctx context.Context, before, after string, f *fileEdit) error {
	var old []byte
	for _, side := range []struct{ mode, id string }{{f.oldMode, f.oldID}, {f.newMode, f.newID}} {
		if side.mode == "000000" {
			continue
		}
		if side.mode == "160000" {
			f.binary = true
			continue
		}
		blob, err := v.run(ctx, "cat-file", "blob", side.id)
		if err != nil {
			return err
		}
		if side.mode == "120000" || bytes.IndexByte(blob, 0) >= 0 {
			f.binary = true
		}
		if side.id == f.oldID {
			old = blob
		}
	}
	if f.binary {
		return nil
	}
	// Require one patch for the raw metadata path. File-to-directory changes can
	// match descendants too; ambiguous output stays unproven rather than guessed.
	patch, err := v.run(ctx, "--literal-pathspecs", "diff", "--patch", "--text", "-U0", "--full-index", "--no-renames",
		"--no-ext-diff", "--no-textconv", "--no-color", "--diff-algorithm=myers", "--no-indent-heuristic", before, after, "--", f.path)
	if err != nil {
		return err
	}
	parsed, err := gitdiff.ParseMultiFileDiff(patch)
	if err != nil {
		return errors.Join(errEdits, err)
	}
	if len(parsed) != 1 {
		// go-diff can also split header-like removed/added lines into files.
		// This safe false negative loses proof, not edit differences.
		return errEdits
	}
	for _, h := range parsed[0].Hunks {
		run, err := editHunk(h)
		if err != nil {
			return err
		}
		if run.removed != "" {
			// Offset-free correspondence is valid only for a unique occurrence.
			if !uniqueBytes(old, []byte(run.removed)) {
				return errEdits
			}
		} else {
			run.anchor, err = insertionSite(old, h.OrigStartLine)
			if err != nil {
				return err
			}
		}
		f.runs = append(f.runs, run)
	}
	if f.oldID != f.newID && len(f.runs) == 0 {
		// Adding/removing an empty file has no hunk but still changes its mode.
		if f.oldMode != "000000" && f.newMode != "000000" {
			return errEdits
		}
	}
	return nil
}

func uniqueBytes(text, part []byte) bool {
	i := bytes.Index(text, part)
	return i >= 0 && i == bytes.LastIndex(text, part)
}

// An insertion has no removed bytes. Pin it to its adjacent original lines,
// requiring that neighborhood to occur once, including file-edge identity.
func insertionSite(old []byte, position int32) (insertionAnchor, error) {
	lines := bytes.SplitAfter(old, []byte{'\n'})
	if len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	i := int(position)
	if i < 0 || i > len(lines) {
		return insertionAnchor{}, errEdits
	}
	a := insertionAnchor{atStart: i == 0, atEnd: i == len(lines)}
	if i > 0 {
		a.before = string(lines[i-1])
	}
	if i < len(lines) {
		a.after = string(lines[i])
	}
	if len(old) > 0 && !uniqueBytes(old, []byte(a.before+a.after)) {
		return insertionAnchor{}, errEdits
	}
	return a, nil
}

func editHunk(h *gitdiff.Hunk) (editRun, error) {
	var removed, added strings.Builder
	offset, oldLines, newLines := 0, int32(0), int32(0)
	for len(h.Body) > offset {
		line := h.Body[offset:]
		if end := bytes.IndexByte(line, '\n'); end >= 0 {
			line = line[:end+1]
		}
		offset += len(line)
		content := line[1:]
		switch line[0] {
		case '-':
			if int32(offset) == h.OrigNoNewlineAt {
				content = bytes.TrimSuffix(content, []byte{'\n'})
			}
			removed.Write(content)
			oldLines++
		case '+':
			added.Write(content)
			newLines++
		default:
			return editRun{}, errEdits
		}
	}
	if oldLines != h.OrigLines || newLines != h.NewLines {
		return editRun{}, errEdits
	}
	return editRun{removed: removed.String(), added: added.String()}, nil
}

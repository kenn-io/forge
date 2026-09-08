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

type editRun struct{ removed, added string }
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

func (v *objectView) commitEdits(ctx context.Context, id string) ([]fileEdit, error) {
	parents, err := v.parents(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(parents) != 1 {
		return nil, errEdits
	}
	if _, err = v.parents(ctx, parents[0]); err != nil {
		return nil, err
	}
	raw, err := v.run(ctx, "diff", "--raw", "-z", "--no-abbrev", "--no-renames", "--no-ext-diff", "--no-textconv", parents[0], id, "--")
	if err != nil {
		return nil, err
	}
	files, err := parseEditFiles(raw)
	if err != nil {
		return nil, err
	}
	for i := range files {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if err = v.fileEdits(ctx, parents[0], id, &files[i]); err != nil {
			return nil, err
		}
	}
	return files, nil
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
	}
	if f.binary {
		return nil
	}
	// Each literal path has exactly one patch. The raw metadata supplies its name.
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
		return errEdits
	}
	for _, h := range parsed[0].Hunks {
		run, err := editHunk(h)
		if err != nil {
			return err
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

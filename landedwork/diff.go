package landedwork

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"go.kenn.io/forge/platform"
)

type FileChange struct {
	OldPath   platform.RawBytes `json:"old_path"`
	NewPath   platform.RawBytes `json:"new_path"`
	OldMode   string            `json:"old_mode"`
	NewMode   string            `json:"new_mode"`
	OldBlob   string            `json:"old_blob"`
	NewBlob   string            `json:"new_blob"`
	Additions *int64            `json:"additions"`
	Deletions *int64            `json:"deletions"`
	Binary    bool              `json:"binary"`
	Renamed   bool              `json:"renamed"`
}
type TreeDiff struct {
	Files []FileChange `json:"files"`
}

func (g *Git) Diff(ctx context.Context, old, new string, meter *platform.Meter) (TreeDiff, error) {
	if !fullObjectID(new) || old != "" && !fullObjectID(old) {
		return TreeDiff{}, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "diff"}
	}
	args := []string{"diff-tree", "--no-commit-id", "-r", "--raw", "--numstat", "-z", "--abbrev=64", "--no-ext-diff", "--no-textconv", "--no-color", "--find-renames=100%", "--diff-algorithm=myers", "--ignore-submodules=none"}
	if old == "" {
		args = append(args, "--root", new)
	} else {
		args = append(args, old, new)
	}
	args = append(args, "--")
	output, err := g.run(ctx, meter, nil, args...)
	if err != nil {
		return TreeDiff{}, err
	}
	return parseTreeDiff(output, meter)
}

func parseTreeDiff(output []byte, meter *platform.Meter) (TreeDiff, error) {
	invalid := errors.New("malformed Git tree diff")
	next := func() ([]byte, bool) {
		value, rest, ok := bytes.Cut(output, []byte{0})
		if ok {
			output = rest
		}
		return value, ok
	}
	result := TreeDiff{Files: []FileChange{}}
	for len(output) > 0 && output[0] == ':' {
		header, ok := next()
		if !ok {
			return TreeDiff{}, invalid
		}
		fields := strings.Fields(string(header[1:]))
		if len(fields) != 5 || !fullObjectID(fields[2]) || !fullObjectID(fields[3]) {
			return TreeDiff{}, invalid
		}
		if err := meter.Records(1); err != nil {
			return TreeDiff{}, err
		}
		name, ok := next()
		if !ok || len(name) == 0 {
			return TreeDiff{}, invalid
		}
		file := FileChange{OldMode: fields[0], NewMode: fields[1], OldBlob: fields[2], NewBlob: fields[3], OldPath: platform.NewRawBytes(name), NewPath: platform.NewRawBytes(name)}
		switch fields[4] {
		case "A":
			file.OldPath = platform.NewRawBytes(nil)
		case "D":
			file.NewPath = platform.NewRawBytes(nil)
		case "M", "T":
		case "R100":
			name, ok = next()
			if !ok || len(name) == 0 {
				return TreeDiff{}, invalid
			}
			file.NewPath = platform.NewRawBytes(name)
			file.Renamed = true
		default:
			return TreeDiff{}, invalid
		}
		result.Files = append(result.Files, file)
	}
	for i := range result.Files {
		stat, ok := next()
		if !ok {
			return TreeDiff{}, invalid
		}
		added, rest, ok := bytes.Cut(stat, []byte{'\t'})
		if !ok {
			return TreeDiff{}, invalid
		}
		deleted, name, ok := bytes.Cut(rest, []byte{'\t'})
		if !ok {
			return TreeDiff{}, invalid
		}
		file := &result.Files[i]
		oldPath, _ := file.OldPath.Bytes()
		newPath, _ := file.NewPath.Bytes()
		if file.Renamed {
			if len(name) != 0 {
				return TreeDiff{}, invalid
			}
			oldName, ok := next()
			if !ok || !bytes.Equal(oldName, oldPath) {
				return TreeDiff{}, invalid
			}
			name, ok = next()
			if !ok || !bytes.Equal(name, newPath) {
				return TreeDiff{}, invalid
			}
		} else {
			expected := newPath
			if len(expected) == 0 {
				expected = oldPath
			}
			if !bytes.Equal(name, expected) {
				return TreeDiff{}, invalid
			}
		}
		if bytes.Equal(added, []byte{'-'}) && bytes.Equal(deleted, []byte{'-'}) {
			file.Binary = true
			continue
		}
		additions, err := strconv.ParseInt(string(added), 10, 64)
		if err != nil || additions < 0 {
			return TreeDiff{}, invalid
		}
		deletions, err := strconv.ParseInt(string(deleted), 10, 64)
		if err != nil || deletions < 0 {
			return TreeDiff{}, invalid
		}
		file.Additions = &additions
		file.Deletions = &deletions
	}
	if len(output) != 0 {
		return TreeDiff{}, invalid
	}
	return result, nil
}

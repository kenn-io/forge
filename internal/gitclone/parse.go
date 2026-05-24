package gitclone

import (
	"bytes"
	"strings"

	godiff "github.com/sourcegraph/go-diff/diff"
)

// ParseRawZ parses the output of `git diff --raw -z` into file metadata.
// Returns files in output order (same order as patch output).
func ParseRawZ(data []byte) []DiffFile {
	// Split on NUL. The format is:
	//   :oldmode newmode oldhash newhash status\0path\0
	//   For renames/copies: :... R100\0oldpath\0newpath\0
	parts := bytes.Split(data, []byte{0})
	var files []DiffFile

	i := 0
	for i < len(parts) {
		part := string(parts[i])
		if !strings.HasPrefix(part, ":") {
			i++
			continue
		}

		// Parse the status letter from the end of the header.
		fields := strings.Fields(part)
		if len(fields) < 5 {
			i++
			continue
		}
		statusRaw := fields[4]
		status, isRenameOrCopy := rawStatusToString(statusRaw)

		i++ // move to path
		if i >= len(parts) {
			break
		}
		path := string(parts[i])

		var oldPath string
		if isRenameOrCopy {
			oldPath = path
			i++ // move to new path
			if i >= len(parts) {
				break
			}
			path = string(parts[i])
		}

		if oldPath == "" {
			oldPath = path
		}

		files = append(files, DiffFile{
			Path:    path,
			OldPath: oldPath,
			Status:  status,
		})
		i++
	}
	return files
}

func rawStatusToString(s string) (status string, isRenameOrCopy bool) {
	if len(s) == 0 {
		return "modified", false
	}
	switch s[0] {
	case 'A':
		return "added", false
	case 'D':
		return "deleted", false
	case 'M':
		return "modified", false
	case 'R':
		return "renamed", true
	case 'C':
		return "copied", true
	case 'T':
		return "modified", false // type change
	default:
		return "modified", false
	}
}

// ParsePatch parses unified diff patch output and merges it with
// pre-populated file metadata from ParseRawZ. Files are correlated by
// output order (git emits them in the same order).
func ParsePatch(patch []byte, rawFiles []DiffFile) []DiffFile {
	fileDiffs, _ := godiff.ParseMultiFileDiff(patch)
	if len(fileDiffs) == 0 {
		return rawFiles
	}

	for i, fd := range fileDiffs {
		if i >= len(rawFiles) {
			break
		}

		// Detect binary from extended headers.
		for _, ext := range fd.Extended {
			if strings.HasPrefix(ext, "Binary files ") || strings.HasPrefix(ext, "GIT binary patch") {
				rawFiles[i].IsBinary = true
				break
			}
		}

		// Convert hunks.
		for _, h := range fd.Hunks {
			hunk := Hunk{
				OldStart: int(h.OrigStartLine),
				OldCount: int(h.OrigLines),
				NewStart: int(h.NewStartLine),
				NewCount: int(h.NewLines),
				Section:  h.Section,
			}
			oldNum := int(h.OrigStartLine)
			newNum := int(h.NewStartLine)

			// The library handles "\ No newline at end of file" internally:
			// - Orig side: sets OrigNoNewlineAt to the byte offset after the line
			// - New side: removes the trailing \n from Body
			body := h.Body
			newSideNoNewline := len(body) > 0 && body[len(body)-1] != '\n'
			bodyLines := strings.Split(string(body), "\n")

			byteOffset := 0
			for j, line := range bodyLines {
				if j == len(bodyLines)-1 && line == "" {
					continue
				}

				lineByteEnd := byteOffset + len(line) + 1 // +1 for \n separator

				if len(line) == 0 {
					hunk.Lines = append(hunk.Lines, Line{
						Type: "context", Content: "", OldNum: oldNum, NewNum: newNum,
					})
					oldNum++
					newNum++
					byteOffset = lineByteEnd
					continue
				}

				isLastRealLine := j == len(bodyLines)-1 ||
					(j == len(bodyLines)-2 && bodyLines[len(bodyLines)-1] == "")

				switch line[0] {
				case ' ':
					// Context lines appear on both sides; check both no-newline signals.
					noNL := (newSideNoNewline && isLastRealLine) ||
						(h.OrigNoNewlineAt > 0 && int32(lineByteEnd) == h.OrigNoNewlineAt)
					hunk.Lines = append(hunk.Lines, Line{
						Type: "context", Content: line[1:], OldNum: oldNum, NewNum: newNum, NoNewline: noNL,
					})
					oldNum++
					newNum++
				case '+':
					noNL := newSideNoNewline && isLastRealLine
					hunk.Lines = append(hunk.Lines, Line{
						Type: "add", Content: line[1:], NewNum: newNum, NoNewline: noNL,
					})
					newNum++
					rawFiles[i].Additions++
				case '-':
					noNL := h.OrigNoNewlineAt > 0 && int32(lineByteEnd) == h.OrigNoNewlineAt
					hunk.Lines = append(hunk.Lines, Line{
						Type: "delete", Content: line[1:], OldNum: oldNum, NoNewline: noNL,
					})
					oldNum++
					rawFiles[i].Deletions++
				}
				byteOffset = lineByteEnd
			}
			rawFiles[i].Hunks = append(rawFiles[i].Hunks, hunk)
		}
		rawFiles[i].Patch = buildPatch(rawFiles[i])
	}

	return rawFiles
}

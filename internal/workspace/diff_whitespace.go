package workspace

import "go.kenn.io/middleman/internal/gitclone"

func isGitWhitespace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	default:
		return false
	}
}

// gitWhitespaceRecordEqual matches xdiff's XDF_IGNORE_WHITESPACE record
// comparison: ASCII C-locale whitespace is discarded while line records keep
// their order and cardinality.
func gitWhitespaceRecordEqual(left, right string) bool {
	i, j := 0, 0
	for {
		for i < len(left) && isGitWhitespace(left[i]) {
			i++
		}
		for j < len(right) && isGitWhitespace(right[j]) {
			j++
		}
		if i == len(left) || j == len(right) {
			for i < len(left) && isGitWhitespace(left[i]) {
				i++
			}
			for j < len(right) && isGitWhitespace(right[j]) {
				j++
			}
			return i == len(left) && j == len(right)
		}
		if left[i] != right[j] {
			return false
		}
		i++
		j++
	}
}

func classifyWhitespaceOnly(files []gitclone.DiffFile) int {
	count := 0
	for i := range files {
		file := &files[i]
		if file.Status != "modified" || file.IsBinary || len(file.Hunks) == 0 {
			continue
		}
		whitespaceOnly := true
		for _, hunk := range file.Hunks {
			if !hunkWhitespaceOnly(hunk) {
				whitespaceOnly = false
				break
			}
		}
		file.IsWhitespaceOnly = whitespaceOnly
		if whitespaceOnly {
			count++
		}
	}
	return count
}

func hunkWhitespaceOnly(hunk gitclone.Hunk) bool {
	oldRecords := make([]string, 0, hunk.OldCount)
	newRecords := make([]string, 0, hunk.NewCount)
	for _, line := range hunk.Lines {
		switch line.Type {
		case "context":
			oldRecords = append(oldRecords, line.Content)
			newRecords = append(newRecords, line.Content)
		case "delete":
			oldRecords = append(oldRecords, line.Content)
		case "add":
			newRecords = append(newRecords, line.Content)
		}
	}
	if len(oldRecords) != len(newRecords) {
		return false
	}
	for i := range oldRecords {
		if !gitWhitespaceRecordEqual(oldRecords[i], newRecords[i]) {
			return false
		}
	}
	return true
}

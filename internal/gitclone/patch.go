package gitclone

import (
	"strconv"
	"strings"
)

func BuildPatch(file DiffFile) string {
	if file.IsBinary || len(file.Hunks) == 0 {
		return ""
	}

	oldPath := file.OldPath
	if oldPath == "" {
		oldPath = file.Path
	}
	oldHeaderPath := "a/" + oldPath
	if file.Status == "added" {
		oldHeaderPath = "/dev/null"
	}
	newHeaderPath := "b/" + file.Path
	if file.Status == "deleted" {
		newHeaderPath = "/dev/null"
	}

	var lines []string
	lines = append(lines, "diff --git a/"+oldPath+" b/"+file.Path)
	lines = append(lines, fileModeHeaders(file)...)
	lines = append(lines, "--- "+oldHeaderPath, "+++ "+newHeaderPath)
	for _, hunk := range file.Hunks {
		oldRange := formatPatchRange(hunk.OldStart, hunk.OldCount)
		newRange := formatPatchRange(hunk.NewStart, hunk.NewCount)
		header := "@@ -" + oldRange + " +" + newRange + " @@"
		if hunk.Section != "" {
			header += " " + hunk.Section
		}
		lines = append(lines, header)
		for _, line := range hunk.Lines {
			lines = append(lines, patchLinePrefix(line.Type)+line.Content)
			if line.NoNewline {
				lines = append(lines, `\ No newline at end of file`)
			}
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func buildPatch(file DiffFile) string {
	return BuildPatch(file)
}

func fileModeHeaders(file DiffFile) []string {
	switch file.Status {
	case "added":
		return []string{"new file mode 100644"}
	case "deleted":
		return []string{"deleted file mode 100644"}
	case "renamed":
		if file.OldPath != "" && file.OldPath != file.Path {
			return []string{"rename from " + file.OldPath, "rename to " + file.Path}
		}
	}
	return nil
}

func formatPatchRange(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + "," + strconv.Itoa(count)
}

func patchLinePrefix(lineType string) string {
	switch lineType {
	case "add":
		return "+"
	case "delete":
		return "-"
	default:
		return " "
	}
}

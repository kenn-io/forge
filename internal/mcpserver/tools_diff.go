package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxMCPDiffFileBytes = 10 << 20

type getItemDiffInput struct {
	Item         itemRefInput `json:"item"`
	EmitDiffFile bool         `json:"emit_diff_file,omitempty"`
}

type diffFileRow struct {
	Path        string `json:"path"`
	OldPath     string `json:"old_path,omitempty"`
	Status      string `json:"status"`
	IsBinary    bool   `json:"is_binary"`
	IsGenerated bool   `json:"is_generated"`
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
}

type diffFileHandle struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type getItemDiffOutput struct {
	Stale          bool            `json:"stale"`
	TotalAdditions int             `json:"total_additions"`
	TotalDeletions int             `json:"total_deletions"`
	Files          []diffFileRow   `json:"files"`
	DiffFile       *diffFileHandle `json:"diff_file,omitempty"`
}

type daemonDiffResponse struct {
	Stale bool             `json:"stale"`
	Files []daemonDiffFile `json:"files"`
}

type daemonDiffFile struct {
	Path        string `json:"path"`
	OldPath     string `json:"old_path"`
	Status      string `json:"status"`
	IsBinary    bool   `json:"is_binary"`
	IsGenerated bool   `json:"is_generated"`
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
	Patch       string `json:"patch"`
}

func (s *Server) registerDiffTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "middleman_get_item_diff",
		Description: "Return cached PR diff evidence. By default this is a compact file summary; " +
			"set emit_diff_file to write the full unified diff to a local temp file.",
	}, wrapTool(s.getItemDiff))
}

func (s *Server) getItemDiff(ctx context.Context, in getItemDiffInput) (getItemDiffOutput, error) {
	if err := validateItemRef(in.Item); err != nil {
		return getItemDiffOutput{}, err
	}
	if in.Item.Type != "pr" {
		return getItemDiffOutput{}, &daemonError{
			Kind:    "invalid_request",
			Message: "diff is only available for prs",
		}
	}

	basePath := itemPath("pulls", in.Item)
	var summary daemonDiffResponse
	if err := s.daemon.getJSON(ctx, basePath+"/files", nil, &summary); err != nil {
		return getItemDiffOutput{}, diffRouteError(err)
	}
	out := getItemDiffOutput{
		Stale: summary.Stale,
		Files: make([]diffFileRow, 0, len(summary.Files)),
	}
	for _, file := range summary.Files {
		out.TotalAdditions += file.Additions
		out.TotalDeletions += file.Deletions
		out.Files = append(out.Files, diffFileRow{
			Path:        file.Path,
			OldPath:     file.OldPath,
			Status:      file.Status,
			IsBinary:    file.IsBinary,
			IsGenerated: file.IsGenerated,
			Additions:   file.Additions,
			Deletions:   file.Deletions,
		})
	}
	if !in.EmitDiffFile {
		return out, nil
	}

	var diff daemonDiffResponse
	if err := s.daemon.getJSON(ctx, basePath+"/diff", nil, &diff); err != nil {
		return getItemDiffOutput{}, diffRouteError(err)
	}
	if err := validateDiffMatchesSummary(summary.Files, diff.Files); err != nil {
		return getItemDiffOutput{}, err
	}
	data, err := serializeDiffPatches(diff.Files)
	if err != nil {
		return getItemDiffOutput{}, err
	}
	store, err := s.diffStore()
	if err != nil {
		return getItemDiffOutput{}, &daemonError{Kind: "daemon_error", Message: "create diff temp store: " + err.Error()}
	}
	path, size, err := store.write(diffFileName(in.Item), data)
	if err != nil {
		return getItemDiffOutput{}, &daemonError{Kind: "daemon_error", Message: "write diff file: " + err.Error()}
	}
	out.DiffFile = &diffFileHandle{Path: path, Bytes: size}
	return out, nil
}

func (s *Server) diffStore() (*diffFileStore, error) {
	if s.diffs != nil {
		return s.diffs, nil
	}
	store, err := newDiffFileStore()
	if err != nil {
		return nil, err
	}
	s.diffs = store
	return store, nil
}

func serializeDiffPatches(files []daemonDiffFile) ([]byte, error) {
	var buf bytes.Buffer
	for _, file := range files {
		if file.Patch == "" {
			return nil, &daemonError{
				Kind:    "daemon_error",
				Message: "daemon returned an empty patch for " + file.Path,
			}
		}
		if buf.Len()+len(file.Patch) > maxMCPDiffFileBytes {
			return nil, &daemonError{
				Kind:    "diff_too_large",
				Message: "diff is too large for MCP temp-file handoff; use the daemon API or a local checkout",
			}
		}
		buf.WriteString(file.Patch)
	}
	return buf.Bytes(), nil
}

func validateDiffMatchesSummary(summary []daemonDiffFile, diff []daemonDiffFile) error {
	if len(summary) != len(diff) {
		return diffMismatchError("", "file_count", len(summary), len(diff))
	}
	files := map[diffIdentity]daemonDiffFile{}
	for _, file := range summary {
		key := file.diffIdentity()
		if _, exists := files[key]; exists {
			return diffMismatchError(file.Path, "duplicate_summary_file", 1, 2)
		}
		files[key] = file
	}
	for _, file := range diff {
		key := file.diffIdentity()
		summaryFile, ok := files[key]
		if !ok {
			return diffMismatchError(file.Path, "file_identity", "summary", "diff")
		}
		if err := validateDiffFileMetadata(summaryFile, file); err != nil {
			return err
		}
		delete(files, key)
	}
	for _, file := range files {
		return diffMismatchError(file.Path, "missing_diff_file", "summary", "diff")
	}
	return nil
}

func validateDiffFileMetadata(summary daemonDiffFile, diff daemonDiffFile) error {
	if summary.IsBinary != diff.IsBinary {
		return diffMismatchError(summary.Path, "is_binary", summary.IsBinary, diff.IsBinary)
	}
	if summary.IsGenerated != diff.IsGenerated {
		return diffMismatchError(summary.Path, "is_generated", summary.IsGenerated, diff.IsGenerated)
	}
	if summary.Additions != diff.Additions {
		return diffMismatchError(summary.Path, "additions", summary.Additions, diff.Additions)
	}
	if summary.Deletions != diff.Deletions {
		return diffMismatchError(summary.Path, "deletions", summary.Deletions, diff.Deletions)
	}
	return nil
}

func diffMismatchError(path string, field string, summary any, diff any) *daemonError {
	details := map[string]any{
		"field":   field,
		"summary": summary,
		"diff":    diff,
	}
	if path != "" {
		details["path"] = path
	}
	return &daemonError{
		Kind:    "diff_incomplete",
		Message: "daemon diff response does not match file summary",
		Details: details,
	}
}

type diffIdentity struct {
	path    string
	oldPath string
	status  string
}

func (f daemonDiffFile) diffIdentity() diffIdentity {
	return diffIdentity{
		path:    f.Path,
		oldPath: f.OldPath,
		status:  f.Status,
	}
}

func diffRouteError(err error) error {
	var derr *daemonError
	if !errors.As(err, &derr) {
		return err
	}
	if derr.Kind == "not_found" && strings.Contains(derr.Message, "pull request not found") {
		return err
	}
	msg := strings.ToLower(derr.Message)
	if derr.Kind == "not_found" ||
		strings.Contains(msg, "clone manager") ||
		strings.Contains(msg, "diff not available") ||
		strings.Contains(msg, "file list not available") ||
		strings.Contains(msg, "diff view not available") ||
		strings.Contains(msg, "files view not available") {
		return &daemonError{
			Kind:    "diff_unavailable",
			Message: derr.Message,
			Details: derr.Details,
		}
	}
	return err
}

func diffFileName(ref itemRefInput) string {
	return fmt.Sprintf("%s-%s-%s-%s-pr-%d.diff",
		sanitizeDiffName(ref.Provider),
		sanitizeDiffName(ref.PlatformHost),
		sanitizeDiffName(ref.Owner),
		sanitizeDiffName(ref.Name),
		ref.Number,
	)
}

func sanitizeDiffName(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r == '.' || r == '_' || r == '-' ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if unicode.IsSpace(r) {
			b.WriteByte('_')
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}

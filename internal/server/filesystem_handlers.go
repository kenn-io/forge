package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Filesystem discovery endpoints back project-registration UX: a client
// browsing for a checkout completes directory paths and resolves an
// arbitrary path to its registerable repository root. Both operate on
// this daemon's local filesystem; fleet proxies expose them for remote
// hosts, where the browsing client has no filesystem access of its own.

type filesystemCompleteInput struct {
	// Path is the partial path being typed. A trailing slash lists the
	// directory's children; otherwise the last component is a prefix
	// filter against its parent's children.
	Path string `query:"path" required:"true"`
}

type filesystemCompleteOutput struct {
	Body struct {
		Completions []string `json:"completions"`
	}
}

type filesystemValidateRepoInput struct {
	Path string `query:"path" required:"true"`
}

type filesystemValidateRepoOutput struct {
	Body struct {
		IsValid bool `json:"is_valid"`
		// RootPath is the repository root when valid: a subdirectory
		// inside a checkout resolves to its toplevel so it cannot
		// register as its own project; bare repositories resolve to
		// the path itself.
		RootPath string `json:"root_path,omitempty"`
		Message  string `json:"message,omitempty"`
	}
}

func (s *Server) completeFilesystemPath(
	_ context.Context, input *filesystemCompleteInput,
) (*filesystemCompleteOutput, error) {
	partial := input.Path

	var searchDir string
	var namePrefix string
	if before, ok := strings.CutSuffix(partial, "/"); ok {
		searchDir = before
		if searchDir == "" {
			searchDir = "/"
		}
	} else {
		searchDir = filepath.Dir(partial)
		namePrefix = filepath.Base(partial)
	}

	out := &filesystemCompleteOutput{}
	out.Body.Completions = []string{}
	entries, err := os.ReadDir(searchDir)
	if err != nil {
		// Nonexistent or unreadable prefixes complete to nothing; the
		// caller is mid-keystroke, not asserting the path exists.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) ||
			errors.Is(err, os.ErrInvalid) {
			return out, nil
		}
		return nil, problemInternal("read directory: " + err.Error())
	}
	for _, entry := range entries {
		if !completionEntryIsDir(searchDir, entry) {
			continue
		}
		if namePrefix != "" && !strings.HasPrefix(entry.Name(), namePrefix) {
			continue
		}
		out.Body.Completions = append(
			out.Body.Completions,
			filepath.Join(searchDir, entry.Name())+"/",
		)
	}
	return out, nil
}

// completionEntryIsDir reports whether a directory entry completes as
// a directory. DirEntry.IsDir does not follow symlinks, so symlinked
// checkout locations resolve through os.Stat — validation would accept
// them, and the browser should offer them.
func completionEntryIsDir(searchDir string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(searchDir, entry.Name()))
	if err != nil {
		return false
	}
	return info.IsDir()
}

func (s *Server) validateFilesystemRepo(
	ctx context.Context, input *filesystemValidateRepoInput,
) (*filesystemValidateRepoOutput, error) {
	path := filepath.Clean(input.Path)
	out := &filesystemValidateRepoOutput{}

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			out.Body.Message = "Directory not found"
			return out, nil
		}
		return nil, problemInternal("stat path: " + err.Error())
	}
	if !info.IsDir() {
		out.Body.Message = "Directory not found"
		return out, nil
	}

	if _, err := gitDiscoveryOutput(ctx, path, "rev-parse", "--git-dir"); err != nil {
		out.Body.Message = "Not a git repository"
		return out, nil
	}

	// Resolve to the repository root. Bare repositories have no
	// toplevel — the path itself is the root. Strip exactly the single
	// trailing newline git appends: a directory name ending in newline
	// characters is unusual but valid, and a broader trim would eat
	// those path characters.
	root := path
	if toplevel, err := gitDiscoveryOutput(
		ctx, path, "rev-parse", "--show-toplevel",
	); err == nil {
		if resolved := strings.TrimSuffix(toplevel, "\n"); resolved != "" {
			root = resolved
		}
	}

	out.Body.IsValid = true
	out.Body.RootPath = root
	return out, nil
}

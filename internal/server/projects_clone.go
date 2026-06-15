package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	gitcmd "go.kenn.io/kit/git/cmd"

	"go.kenn.io/middleman/internal/db"
)

// Clone-and-register backs remote project acquisition: the host that
// will own the checkout clones the repository URL to a local path and
// registers it as a project in one operation. The fleet proxy exposes
// it for remote hosts; home-relative destinations expand on this host
// because only it knows its home directory.

type cloneProjectInput struct {
	Body struct {
		// URL is the repository to clone (any URL git accepts).
		URL string `json:"url"`
		// Path is the destination directory; "~/"-prefixed paths
		// resolve against this daemon's home.
		Path string `json:"path"`
		// Branch optionally checks out a specific branch.
		Branch string `json:"branch,omitempty"`
		// DisplayName defaults to the destination's base name.
		DisplayName string `json:"display_name,omitempty"`
	}
}

func (s *Server) cloneProject(
	ctx context.Context, input *cloneProjectInput,
) (*registerProjectOutput, error) {
	rawURL := strings.TrimSpace(input.Body.URL)
	if rawURL == "" {
		return nil, problemValidation("body.url", "url is required")
	}
	rawPath := expandHomeCWD(strings.TrimSpace(input.Body.Path))
	if rawPath == "" {
		return nil, problemValidation("body.path", "path is required")
	}
	abs, err := filepath.Abs(rawPath)
	if err != nil {
		return nil, problemValidation("body.path", "resolve path: "+err.Error())
	}
	if _, statErr := os.Stat(abs); statErr == nil {
		return nil, problemConflict(
			CodeDestinationExists,
			"destination already exists: "+abs,
			nil,
		)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, problemInternal("create destination parent: " + err.Error())
	}
	// Reserve the destination before cloning: git clones happily into
	// an existing empty directory, and creating it here means every
	// later rollback removes a directory this request owns — a
	// concurrent creator loses the Mkdir race and conflicts instead.
	if err := os.Mkdir(abs, 0o755); err != nil {
		if os.IsExist(err) {
			return nil, problemConflict(
				CodeDestinationExists,
				"destination already exists: "+abs,
				nil,
			)
		}
		return nil, problemInternal("create destination: " + err.Error())
	}

	args := []string{"clone"}
	if branch := strings.TrimSpace(input.Body.Branch); branch != "" {
		args = append(args, "-b", branch)
	}
	args = append(args, rawURL, abs)
	if _, err := gitcmd.New().Output(ctx, filepath.Dir(abs), args...); err != nil {
		// git cleans up most failed clones itself, but checkout or
		// submodule failures can leave a partial directory that would
		// make every retry hit destinationExists.
		_ = os.RemoveAll(abs)
		return nil, problemBadRequest(
			CodeBadRequest,
			"git clone failed: "+err.Error(),
			nil,
		)
	}

	displayName := strings.TrimSpace(input.Body.DisplayName)
	if displayName == "" {
		displayName = filepath.Base(abs)
	}
	created, err := s.registerProjectAtPath(ctx, abs, displayName, nil, "")
	if err != nil {
		// Remove the checkout this request created so a retry does not
		// hit destinationExists over a half-registered clone — but only
		// when no project row landed. Registration can fail after the
		// insert (e.g. the discovery pass is cancelled), and deleting
		// the checkout then would orphan the registered project.
		if _, lookupErr := s.db.GetProjectByLocalPath(
			context.WithoutCancel(ctx), abs,
		); errors.Is(lookupErr, db.ErrProjectNotFound) {
			_ = os.RemoveAll(abs)
		}
		return nil, err
	}
	return &registerProjectOutput{Body: projectResponseFromDB(created)}, nil
}

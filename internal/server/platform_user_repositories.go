package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// The user-repositories endpoint backs "pick one of your repositories"
// flows: it lists the repositories the authenticated platform CLI user
// can see, so a client can offer a clone/import picker without its own
// platform credentials. Backed by the gh CLI on this host; auth and
// missing-binary failures get distinct problem codes so the UI can say
// "install gh" versus "run gh auth login".

// userRepositoriesTimeout bounds each gh subprocess page so a hung
// credential helper or stalled network call surfaces a recognisable
// timeout instead of pinning the picker indefinitely.
const userRepositoriesTimeout = 30 * time.Second

const (
	defaultUserRepositoriesLimit = 100
	maxUserRepositoriesLimit     = 1000
	userRepositoriesPageSize     = 100
)

type listUserRepositoriesInput struct {
	// Provider selects the platform CLI to list through. Only
	// "github" (the default) is implemented today; other providers
	// return an unsupportedCapability problem rather than silently
	// listing the wrong platform.
	Provider string `query:"provider"`
	// PlatformHost targets a specific platform host (e.g. a GitHub
	// Enterprise deployment); empty uses the CLI's default host.
	PlatformHost string `query:"platform_host"`
	// Limit caps how many repositories are returned; values outside
	// (0, 1000] fall back to the default of 100.
	Limit int `query:"limit"`
}

type userRepository struct {
	NameWithOwner string `json:"name_with_owner"`
	SSHURL        string `json:"ssh_url,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

type listUserRepositoriesOutput struct {
	Body struct {
		Repositories []userRepository `json:"repositories"`
	}
}

// ghUserRepository mirrors the REST repository fields consumed from
// `gh api user/repos`.
type ghUserRepository struct {
	FullName      string `json:"full_name"`
	SSHURL        string `json:"ssh_url"`
	DefaultBranch string `json:"default_branch"`
}

func (s *Server) listUserRepositories(
	ctx context.Context, input *listUserRepositoriesInput,
) (*listUserRepositoriesOutput, error) {
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	platformHost := strings.TrimSpace(input.PlatformHost)
	if provider != "" && provider != "github" {
		return nil, newProblem(
			http.StatusConflict,
			CodeUnsupportedCapability,
			"Unsupported provider capability",
			map[string]any{
				"capability":   "user-repositories",
				"provider":     provider,
				"platformHost": platformHost,
			},
		)
	}
	limit := input.Limit
	if limit <= 0 || limit > maxUserRepositoriesLimit {
		limit = defaultUserRepositoriesLimit
	}

	run := s.toolingRun
	if run == nil {
		run = defaultToolingRunner
	}

	resp := &listUserRepositoriesOutput{}
	resp.Body.Repositories = make([]userRepository, 0, limit)
	// per_page stays constant across pages: GitHub page offsets are
	// per_page-relative, so shrinking the final request would re-fetch
	// earlier items instead of the tail. Truncate after appending.
	for page := 1; len(resp.Body.Repositories) < limit; page++ {
		raw, err := s.fetchUserRepositoriesPage(
			ctx, run, platformHost, userRepositoriesPageSize, page,
		)
		if err != nil {
			return nil, err
		}
		for _, r := range raw {
			resp.Body.Repositories = append(resp.Body.Repositories, userRepository{
				NameWithOwner: r.FullName,
				SSHURL:        r.SSHURL,
				DefaultBranch: r.DefaultBranch,
			})
		}
		if len(resp.Body.Repositories) > limit {
			resp.Body.Repositories = resp.Body.Repositories[:limit]
		}
		// A short page means the listing is exhausted.
		if len(raw) < userRepositoriesPageSize {
			break
		}
	}
	return resp, nil
}

// fetchUserRepositoriesPage runs one gh api page. user/repos with all
// affiliations covers organization and collaborator repositories,
// which `gh repo list` (owner-only) omits — maintainers mostly work in
// repos they do not own. A non-empty host targets that deployment via
// gh's --hostname (self-hosted GitHub), using its stored credentials.
func (s *Server) fetchUserRepositoriesPage(
	ctx context.Context,
	run toolingRunner,
	platformHost string,
	pageSize, page int,
) ([]ghUserRepository, error) {
	args := []string{"api"}
	if platformHost != "" {
		args = append(args, "--hostname", platformHost)
	}
	args = append(args, fmt.Sprintf(
		"user/repos?per_page=%d&page=%d&affiliation=owner,collaborator,organization_member&sort=updated",
		pageSize, page,
	))
	subCtx, cancel := context.WithTimeout(ctx, userRepositoriesTimeout)
	defer cancel()
	out, err := run(subCtx, "gh", args...)
	if err != nil {
		if errors.Is(subCtx.Err(), context.DeadlineExceeded) {
			return nil, problemUpstream(
				"listing repositories timed out — check the network or rerun after `gh auth status`",
				"github", platformHost,
			)
		}
		return nil, userRepositoriesProblem(err, platformHost)
	}
	var raw []ghUserRepository
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, problemInternal(
			"decode repository listing: " + err.Error(),
		)
	}
	return raw, nil
}

// userRepositoriesProblem classifies a gh subprocess failure: a missing
// binary means "install gh", an auth-flavored failure means "run
// gh auth login", anything else surfaces as a bad-gateway-style
// upstream error with the CLI's own message.
func userRepositoriesProblem(err error, platformHost string) error {
	var execErr *exec.Error
	if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
		return problemNotFound(
			CodeToolMissing,
			"the gh CLI is not installed on this host",
			nil,
		)
	}
	message := err.Error()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		message = strings.TrimSpace(string(exitErr.Stderr))
	}
	lowered := strings.ToLower(message)
	if strings.Contains(lowered, "auth") ||
		strings.Contains(lowered, "logged in") ||
		strings.Contains(lowered, "authentication") {
		return newProblem(
			http.StatusForbidden,
			CodeToolUnauthenticated,
			"gh is not authenticated on this host: "+message,
			nil,
		)
	}
	return problemUpstream("gh api user/repos failed: "+message, "github", platformHost)
}

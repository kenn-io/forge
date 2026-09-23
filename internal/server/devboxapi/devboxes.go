package devboxapi

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/tokenauth"
)

type CreateDevboxWorkspaceInput struct {
	ConnectionID string `path:"connection_id"`
	Body         struct {
		Provider            string `json:"provider"`
		PlatformHost        string `json:"platform_host"`
		Owner               string `json:"owner"`
		Name                string `json:"name"`
		MRNumber            int    `json:"mr_number,omitempty"`
		IssueNumber         int    `json:"issue_number,omitempty"`
		Branch              string `json:"branch,omitempty"`
		ReuseExistingBranch bool   `json:"reuse_existing_branch,omitempty"`
	}
}

func DevboxResponseProblem(response *http.Response) error {
	var problem struct {
		Detail string `json:"detail"`
	}
	_ = json.UnmarshalRead(io.LimitReader(response.Body, 64<<10), &problem)
	if problem.Detail == "" {
		problem.Detail = http.StatusText(response.StatusCode)
	}
	return httpapi.NewProblem(response.StatusCode, httpapi.CodeUpstreamError, problem.Detail, nil)
}

type DevboxProxyRoute struct{ Method, Path, Operation string }

var DevboxProxyRoutes = []DevboxProxyRoute{
	{"GET", "/workspaces", "list-devbox-workspaces"},
	{"GET", "/workspaces/{id}", "get-devbox-workspace"},
	{"GET", "/workspaces/{id}/agent-sessions", "list-devbox-agent-sessions"},
	{"GET", "/workspaces/{id}/commits", "get-devbox-commits"},
	{"GET", "/workspaces/{id}/diff", "get-devbox-diff"},
	{"GET", "/workspaces/{id}/diff/watch", "watch-devbox-diff"},
	{"GET", "/workspaces/{id}/file-preview", "get-devbox-file-preview"},
	{"GET", "/workspaces/{id}/files", "get-devbox-files"},
	{"POST", "/workspaces/{id}/retry", "retry-devbox-workspace"},
	{"POST", "/workspaces/{id}/refresh", "refresh-devbox-workspace"},
	{"POST", "/workspaces/{id}/push", "push-devbox-workspace"},
	{"POST", "/workspaces/{id}/pull", "pull-devbox-workspace"},
	{"DELETE", "/workspaces/{id}", "delete-devbox-workspace"},
	{"GET", "/workspaces/{id}/runtime", "get-devbox-runtime"},
	{"POST", "/workspaces/{id}/runtime/sessions", "launch-devbox-session"},
	{"DELETE", "/workspaces/{id}/runtime/sessions/{session_key}", "stop-devbox-session"},
	{"PATCH", "/workspaces/{id}/runtime/sessions/{session_key}", "rename-devbox-session"},
	{"GET", "/workspaces/{id}/runtime/sessions/{session_key}/attach-spec", "get-devbox-attach-spec"},
	{"POST", "/workspaces/{id}/runtime/sessions/{session_key}/initial-message", "send-devbox-initial-message"},
	{"POST", "/workspaces/{id}/runtime/agent-handoffs", "launch-devbox-handoff"},
	{"POST", "/terminal/paste-image", "store-devbox-paste-image"},
}

func (s *Handlers) DevboxAttributionSource(workspace workspaceapi.WorkspaceResponse) tokenauth.Source {
	s.CfgMu.Lock()
	defer s.CfgMu.Unlock()
	if (*s.Cfg) == nil || (*s.TokenSources) == nil || workspace.Repo.Provider != "github" || workspace.PlatformHost != "github.com" {
		return nil
	}
	repo := config.Repo{Platform: "github", PlatformHost: "github.com", Owner: workspace.RepoOwner, Name: workspace.RepoName}
	for _, candidate := range (*s.Cfg).Repos {
		if candidate.PlatformOrDefault() == "github" && candidate.PlatformHostOrDefault() == "github.com" && strings.EqualFold(candidate.Owner, repo.Owner) && strings.EqualFold(candidate.Name, repo.Name) {
			repo = candidate
			break
		}
	}
	return (*s.TokenSources).Upsert((*s.Cfg).ResolveGitHubRepoTokenSource(repo))
}

func CopyDevboxEvents(w http.ResponseWriter, body io.Reader) {
	buffer := make([]byte, 4096)
	controller := http.NewResponseController(w)
	for {
		n, err := body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			_ = controller.Flush()
		}
		if err != nil {
			return
		}
	}
}

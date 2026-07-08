package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/kata"
)

// kataDaemonReadTimeout bounds server-side reads against a Kata daemon so a
// hung remote daemon cannot pin the API handler.
const kataDaemonReadTimeout = 20 * time.Second

// kataDaemonProjectsReadTimeout bounds the best-effort project-name read.
// The issue detail is the critical path: the handler waits on this budget
// only when the issue payload carries no project name of its own, and
// expiry falls back to that payload name.
const kataDaemonProjectsReadTimeout = 3 * time.Second

// maxKataDaemonReadBytes caps how much of a daemon response middleman will
// buffer for a single task detail read.
const maxKataDaemonReadBytes = 8 << 20

type kataTaskDetailInput struct {
	IssueUID string `path:"issue_uid" doc:"Kata issue UID"`
	DaemonID string `header:"X-Middleman-Kata-Daemon" doc:"Kata daemon id; the effective default daemon when empty"`
}

type kataTaskDetailResponse struct {
	Detail          any                         `json:"detail" doc:"Verbatim Kata daemon issue detail payload"`
	ETag            string                      `json:"etag,omitempty" doc:"Daemon issue detail ETag, when the daemon provided one"`
	WorkspaceTarget kataWorkspaceTargetResponse `json:"workspace_target"`
}

type kataTaskDetailOutput struct {
	// The response depends on the selected daemon, so caches must key on the
	// daemon header just like the passthrough proxy does.
	Vary string `header:"Vary"`
	Body kataTaskDetailResponse
}

type kataDaemonReadResult struct {
	status int
	header http.Header
	body   []byte
	err    error
}

// kataTaskDetail serves the task detail pane in one round trip: it reads the
// issue detail (and project names) from the selected daemon server-side and
// attaches the resolved workspace target, so the frontend does not need a
// separate workspace-target request that would land after the pane renders.
func (s *Server) kataTaskDetail(
	ctx context.Context,
	input *kataTaskDetailInput,
) (out *kataTaskDetailOutput, err error) {
	// Every outcome of this handler depends on the daemon selection header,
	// so problem responses must declare Vary just like the success path (and
	// the passthrough proxy); otherwise a cache could reuse one daemon's
	// error for another daemon's request.
	defer func() {
		if err != nil {
			err = huma.ErrorWithHeaders(err, http.Header{"Vary": []string{kataDaemonHeaderName}})
		}
	}()
	issueUID := strings.TrimSpace(input.IssueUID)
	if issueUID == "" {
		return nil, problemValidation("path.issue_uid", "issue_uid is required")
	}
	daemon, problem := selectKataDaemonForID(input.DaemonID)
	if problem != nil {
		return nil, problem
	}
	client, baseURL, err := kataDaemonHTTPClient(daemon)
	if err != nil {
		return nil, problemBadRequest("", "invalid Kata daemon target", map[string]any{"daemon": daemon.ID})
	}

	ctx, cancel := context.WithTimeout(ctx, kataDaemonReadTimeout)
	defer cancel()
	projectsCtx, cancelProjects := context.WithTimeout(ctx, kataDaemonProjectsReadTimeout)
	defer cancelProjects()

	detailCh := make(chan kataDaemonReadResult, 1)
	projectsCh := make(chan kataDaemonReadResult, 1)
	go func() {
		detailCh <- kataDaemonGet(ctx, client, daemon, baseURL+"/api/v1/issues/"+url.PathEscape(issueUID))
	}()
	go func() {
		projectsCh <- kataDaemonGet(projectsCtx, client, daemon, baseURL+"/api/v1/projects")
	}()
	// Issue read outcomes (including errors) return without joining the
	// best-effort projects read; the buffered channel lets its goroutine
	// finish on its own after the handler returns.
	detail := <-detailCh

	if detail.err != nil {
		return nil, newProblem(
			http.StatusBadGateway,
			CodeUpstreamError,
			"Kata daemon is unreachable",
			map[string]any{"daemon": daemon.ID},
		)
	}
	if detail.status == http.StatusNotFound {
		return nil, problemNotFound(CodeNotFound, "Kata task not found", map[string]any{
			"daemon":    daemon.ID,
			"issue_uid": issueUID,
		})
	}
	if detail.status < http.StatusOK || detail.status >= http.StatusMultipleChoices {
		return nil, newProblem(
			http.StatusBadGateway,
			CodeUpstreamError,
			fmt.Sprintf("Kata daemon issue read failed with status %d", detail.status),
			map[string]any{"daemon": daemon.ID},
		)
	}

	var parsedDetail struct {
		Issue struct {
			UID         string `json:"uid"`
			ProjectUID  string `json:"project_uid"`
			ProjectName string `json:"project_name"`
			ShortID     string `json:"short_id"`
			QualifiedID string `json:"qualified_id"`
			Title       string `json:"title"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(detail.body, &parsedDetail); err != nil || parsedDetail.Issue.ProjectUID == "" {
		return nil, newProblem(
			http.StatusBadGateway,
			CodeUpstreamError,
			"Kata daemon returned an unexpected issue payload",
			map[string]any{"daemon": daemon.ID},
		)
	}

	projectName := parsedDetail.Issue.ProjectName
	if projectName == "" {
		// Only an empty payload name is worth waiting (briefly) for the
		// projects listing; otherwise take it only when already available so
		// a slow projects route never delays the detail response.
		projects := <-projectsCh
		projectName = kataProjectNameForUID(projects, parsedDetail.Issue.ProjectUID, projectName)
	} else {
		select {
		case projects := <-projectsCh:
			projectName = kataProjectNameForUID(projects, parsedDetail.Issue.ProjectUID, projectName)
		default:
		}
	}
	metadata := db.WorkspaceKataMetadata{
		DaemonID:    daemon.ID,
		ProjectUID:  parsedDetail.Issue.ProjectUID,
		ProjectName: projectName,
		IssueUID:    issueUID,
		ShortID:     parsedDetail.Issue.ShortID,
		QualifiedID: parsedDetail.Issue.QualifiedID,
		Title:       parsedDetail.Issue.Title,
	}
	target, err := s.kataWorkspaceTargetForMetadata(ctx, metadata)
	if err != nil {
		return nil, err
	}

	return &kataTaskDetailOutput{
		Vary: kataDaemonHeaderName,
		Body: kataTaskDetailResponse{
			Detail:          json.RawMessage(detail.body),
			ETag:            detail.header.Get("ETag"),
			WorkspaceTarget: target,
		},
	}, nil
}

func kataDaemonGet(ctx context.Context, client *http.Client, d kata.Daemon, target string) kataDaemonReadResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return kataDaemonReadResult{err: err}
	}
	if token := kataDaemonForwardToken(d); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return kataDaemonReadResult{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKataDaemonReadBytes))
	if err != nil {
		return kataDaemonReadResult{err: err}
	}
	return kataDaemonReadResult{status: resp.StatusCode, header: resp.Header, body: body}
}

// kataDaemonHTTPClient builds an HTTP client and base URL for server-side
// reads against a resolved daemon, reusing the proxy's target parsing so
// unix-socket daemons work identically.
func kataDaemonHTTPClient(d kata.Daemon) (*http.Client, string, error) {
	target, transport, err := kataDaemonProxyTarget(d.URL)
	if err != nil {
		return nil, "", err
	}
	if transport == nil {
		transport = newDefaultKataDaemonTransport()
	}
	base := strings.TrimSuffix(target.String(), "/")
	// Like the proxy and health probe, never follow daemon redirects: a
	// misconfigured or malicious daemon must not bounce server-side reads
	// (and their Authorization header) to another target.
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, base, nil
}

// kataProjectNameForUID resolves the project name for repo mapping from the
// daemon's projects listing, falling back to whatever name the issue payload
// itself carried.
func kataProjectNameForUID(projects kataDaemonReadResult, projectUID, fallback string) string {
	if projects.err != nil || projects.status != http.StatusOK {
		return fallback
	}
	var parsed struct {
		Projects []struct {
			UID  string `json:"uid"`
			Name string `json:"name"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(projects.body, &parsed); err != nil {
		return fallback
	}
	for _, project := range parsed.Projects {
		if project.UID == projectUID && project.Name != "" {
			return project.Name
		}
	}
	return fallback
}

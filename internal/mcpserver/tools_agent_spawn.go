package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.kenn.io/forge/internal/platform"
)

const (
	defaultAgentHandoffTimeout = 5 * time.Minute
	maxAgentHandoffTimeout     = 15 * time.Minute
	agentHandoffPollInterval   = 10 * time.Millisecond
	maxAgentInitialMessage     = 64 << 10
)

type workspaceSourceInput struct {
	Type  string                `json:"type" jsonschema:"source type: item or adhoc"`
	Item  *itemRefInput         `json:"item,omitempty"`
	AdHoc *adHocWorkspaceSource `json:"adhoc,omitempty"`
}

type adHocWorkspaceSource struct {
	Repo   repoFilterInput `json:"repo"`
	Branch string          `json:"branch,omitempty"`
}

type spawnWorkspaceWithAgentInput struct {
	Source         workspaceSourceInput `json:"source"`
	AgentTarget    string               `json:"agent_target"`
	InitialMessage string               `json:"initial_message"`
	Timeout        string               `json:"timeout,omitempty"`
}

type spawnedWorkspace struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reused bool   `json:"reused"`
}

type spawnedRuntime struct {
	SessionKey string `json:"session_key"`
	TargetKey  string `json:"target_key"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
}

type spawnWorkspaceWithAgentOutput struct {
	Stage            string                   `json:"stage"`
	Source           workspaceSourceInput     `json:"source"`
	Workspace        spawnedWorkspace         `json:"workspace"`
	Runtime          spawnedRuntime           `json:"runtime"`
	CodingSession    workspaceAgentSessionRow `json:"coding_session"`
	InitialMessage   *agentInitialMessageRow  `json:"initial_message,omitempty"`
	MessageDelivered bool                     `json:"message_delivered"`
}

type daemonSpawnWorkspace struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	Created      bool    `json:"created"`
	GitHeadRef   string  `json:"git_head_ref"`
	ErrorMessage *string `json:"error_message"`
}

type daemonSpawnRuntime struct {
	Key       string    `json:"key"`
	TargetKey string    `json:"target_key"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type daemonWorkspaceRuntime struct {
	Sessions []daemonSpawnRuntime `json:"sessions"`
}

func (s *Server) spawnWorkspaceWithAgent(
	ctx context.Context,
	in spawnWorkspaceWithAgentInput,
) (spawnWorkspaceWithAgentOutput, error) {
	normalized, timeout, err := validateSpawnWorkspaceInput(in)
	if err != nil {
		return spawnWorkspaceWithAgentOutput{}, err
	}
	in = normalized
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	targets, err := s.listAgentTargets(ctx, listAgentTargetsInput{})
	if err != nil {
		return spawnWorkspaceWithAgentOutput{}, err
	}
	target, ok := findAgentTarget(targets.Targets, in.AgentTarget)
	if !ok {
		return spawnWorkspaceWithAgentOutput{}, fmt.Errorf(
			"agent_target %q is not a supported coding-agent target", in.AgentTarget,
		)
	}
	if !target.Available {
		return spawnWorkspaceWithAgentOutput{}, fmt.Errorf(
			"agent_target %q is unavailable: %s", in.AgentTarget, target.DisabledReason,
		)
	}

	out := spawnWorkspaceWithAgentOutput{Source: in.Source}
	workspace, reused, err := s.resolveOrCreateWorkspace(ctx, in.Source)
	if err != nil {
		return out, handoffFailure(ctx, err, out, "", "workspace_created")
	}
	if in.Source.AdHoc != nil && in.Source.AdHoc.Branch == "" {
		out.Source.AdHoc.Branch = workspace.GitHeadRef
	}
	out.Stage = "workspace_created"
	out.Workspace = spawnedWorkspace{ID: workspace.ID, Status: workspace.Status, Reused: reused}

	workspace, err = s.waitForWorkspaceReady(ctx, workspace.ID)
	if err != nil {
		return out, handoffFailure(ctx, err, out, "workspace_created", "workspace_ready")
	}
	out.Stage = "workspace_ready"
	out.Workspace.Status = workspace.Status

	var runtime daemonSpawnRuntime
	if err := s.daemon.postJSON(
		ctx,
		"/api/v1/workspaces/"+seg(workspace.ID)+"/runtime/sessions",
		map[string]any{"target_key": in.AgentTarget},
		&runtime,
	); err != nil {
		return out, handoffFailure(ctx, err, out, "workspace_ready", "runtime_launched")
	}
	if strings.TrimSpace(runtime.Key) == "" {
		return out, handoffFailure(
			ctx,
			errors.New("daemon runtime response missing key"), out,
			"workspace_ready", "runtime_launched",
		)
	}
	out.Stage = "runtime_launched"
	out.Runtime = spawnedRuntime{
		SessionKey: runtime.Key,
		TargetKey:  runtime.TargetKey,
		Status:     runtime.Status,
		CreatedAt:  formatMCPTime(runtime.CreatedAt),
	}

	codingSession, err := s.waitForCodingSession(ctx, workspace.ID, runtime.Key)
	if err != nil {
		return out, handoffFailure(ctx, err, out, "runtime_launched", "coding_session_observed")
	}
	out.Stage = "coding_session_observed"
	out.CodingSession = codingSession

	receipt, err := s.submitInitialAgentMessage(
		ctx, workspace.ID, runtime.Key, codingSession, in.InitialMessage,
	)
	if err != nil {
		return out, handoffFailure(ctx, err, out, "coding_session_observed", "message_delivered")
	}
	out.InitialMessage = &receipt
	if receipt.State != "delivered" {
		return out, handoffFailure(
			ctx,
			fmt.Errorf("initial message receipt state is %s", receipt.State), out,
			"coding_session_observed", "message_delivered",
		)
	}
	out.Stage = "message_delivered"
	out.MessageDelivered = true
	return out, nil
}

func validateSpawnWorkspaceInput(
	in spawnWorkspaceWithAgentInput,
) (spawnWorkspaceWithAgentInput, time.Duration, error) {
	timeout := defaultAgentHandoffTimeout
	if strings.TrimSpace(in.Timeout) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(in.Timeout))
		if err != nil || parsed <= 0 {
			return in, 0, fmt.Errorf("timeout must be a positive duration")
		}
		timeout = parsed
	}
	if timeout > maxAgentHandoffTimeout {
		return in, 0, fmt.Errorf("timeout must not exceed 15m")
	}
	in.AgentTarget = strings.ToLower(strings.TrimSpace(in.AgentTarget))
	if in.AgentTarget == "" {
		return in, 0, fmt.Errorf("agent_target is required")
	}
	message, err := normalizeSpawnInitialMessage(in.InitialMessage)
	if err != nil {
		return in, 0, err
	}
	in.InitialMessage = message
	in.Source.Type = strings.ToLower(strings.TrimSpace(in.Source.Type))
	switch in.Source.Type {
	case "item":
		if in.Source.Item == nil || in.Source.AdHoc != nil {
			return in, 0, fmt.Errorf("source must contain exactly one tagged item")
		}
		item, err := normalizeSpawnItem(*in.Source.Item)
		if err != nil {
			return in, 0, err
		}
		in.Source.Item = &item
	case "adhoc":
		if in.Source.AdHoc == nil || in.Source.Item != nil {
			return in, 0, fmt.Errorf("source must contain exactly one tagged adhoc repo")
		}
		repo, err := normalizeSpawnRepo(in.Source.AdHoc.Repo)
		if err != nil {
			return in, 0, err
		}
		in.Source.AdHoc.Repo = repo
		in.Source.AdHoc.Branch = strings.TrimSpace(in.Source.AdHoc.Branch)
	default:
		return in, 0, fmt.Errorf("source.type must be item or adhoc")
	}
	return in, timeout, nil
}

func normalizeSpawnItem(item itemRefInput) (itemRefInput, error) {
	item.Type = strings.ToLower(strings.TrimSpace(item.Type))
	item.Provider = strings.ToLower(strings.TrimSpace(item.Provider))
	item.PlatformHost = strings.TrimSpace(item.PlatformHost)
	item.Owner = strings.Trim(strings.TrimSpace(item.Owner), "/")
	item.Name = strings.Trim(strings.TrimSpace(item.Name), "/")
	if err := validateItemRef(item); err != nil {
		return item, err
	}
	kind, err := platform.NormalizeKind(item.Provider)
	if err != nil {
		return item, err
	}
	metadata, ok := platform.MetadataFor(kind)
	if !ok {
		return item, fmt.Errorf("unsupported provider %q", item.Provider)
	}
	item.Provider = string(kind)
	if item.PlatformHost == "" {
		item.PlatformHost = metadata.DefaultHost
	}
	return item, nil
}

func normalizeSpawnRepo(repo repoFilterInput) (repoFilterInput, error) {
	repo.Provider = strings.ToLower(strings.TrimSpace(repo.Provider))
	repo.PlatformHost = strings.TrimSpace(repo.PlatformHost)
	repo.RepoPath = strings.Trim(strings.TrimSpace(repo.RepoPath), "/")
	repo.Owner = strings.Trim(strings.TrimSpace(repo.Owner), "/")
	repo.Name = strings.Trim(strings.TrimSpace(repo.Name), "/")
	if repo.Provider == "" {
		return repo, fmt.Errorf("repo provider is required")
	}
	kind, err := platform.NormalizeKind(repo.Provider)
	if err != nil {
		return repo, err
	}
	metadata, ok := platform.MetadataFor(kind)
	if !ok {
		return repo, fmt.Errorf("unsupported provider %q", repo.Provider)
	}
	repo.Provider = string(kind)
	if repo.PlatformHost == "" {
		repo.PlatformHost = metadata.DefaultHost
	}
	if repo.RepoPath != "" {
		parts := strings.Split(repo.RepoPath, "/")
		if len(parts) < 2 || slicesContainEmpty(parts) {
			return repo, fmt.Errorf("repo_path must contain an owner and repository name")
		}
		pathOwner := strings.Join(parts[:len(parts)-1], "/")
		pathName := parts[len(parts)-1]
		if (repo.Owner != "" || repo.Name != "") &&
			(repo.Owner != pathOwner || repo.Name != pathName) {
			return repo, fmt.Errorf("repo_path conflicts with repo owner or name")
		}
		repo.Owner = pathOwner
		repo.Name = pathName
	}
	if repo.Owner == "" {
		return repo, fmt.Errorf("repo owner is required")
	}
	if repo.Name == "" {
		return repo, fmt.Errorf("repo name is required")
	}
	if repo.RepoPath == "" {
		repo.RepoPath = repo.Owner + "/" + repo.Name
	}
	return repo, nil
}

func slicesContainEmpty(values []string) bool {
	return slices.Contains(values, "")
}

func normalizeSpawnInitialMessage(message string) (string, error) {
	if !utf8.ValidString(message) {
		return "", fmt.Errorf("initial_message must be valid UTF-8")
	}
	message = strings.ReplaceAll(message, "\r\n", "\n")
	message = strings.ReplaceAll(message, "\r", "\n")
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("initial_message must not be blank")
	}
	for _, value := range message {
		if value != '\n' && !unicode.IsPrint(value) {
			return "", fmt.Errorf("initial_message contains an unsafe control character")
		}
	}
	if len(message) > maxAgentInitialMessage {
		return "", fmt.Errorf("initial_message must not exceed 64 KiB")
	}
	return message, nil
}

func findAgentTarget(targets []agentTargetRow, key string) (agentTargetRow, bool) {
	for _, target := range targets {
		if target.Key == key {
			return target, true
		}
	}
	return agentTargetRow{}, false
}

func (s *Server) resolveOrCreateWorkspace(
	ctx context.Context,
	source workspaceSourceInput,
) (daemonSpawnWorkspace, bool, error) {
	if source.Item != nil {
		switch source.Item.Type {
		case "pr":
			return s.resolveOrCreatePRWorkspace(ctx, *source.Item)
		case "issue":
			return s.resolveOrCreateIssueWorkspace(ctx, *source.Item)
		}
	}
	if source.AdHoc != nil {
		return s.createAdHocWorkspace(ctx, *source.AdHoc)
	}
	return daemonSpawnWorkspace{}, false, fmt.Errorf("unsupported workspace source")
}

func (s *Server) resolveOrCreatePRWorkspace(
	ctx context.Context,
	item itemRefInput,
) (daemonSpawnWorkspace, bool, error) {
	var detail daemonPullDetail
	if err := s.daemon.getJSON(ctx, itemPath("pulls", item), nil, &detail); err != nil {
		return daemonSpawnWorkspace{}, false, err
	}
	if detail.MergeRequest == nil {
		return daemonSpawnWorkspace{}, false, fmt.Errorf("daemon pull detail missing merge_request")
	}
	if detail.Workspace != nil && strings.TrimSpace(detail.Workspace.ID) != "" {
		return daemonSpawnWorkspace{
			ID: detail.Workspace.ID, Status: detail.Workspace.Status,
		}, true, nil
	}
	var workspace daemonSpawnWorkspace
	err := s.daemon.postJSON(ctx, "/api/v1/workspaces", map[string]any{
		"provider":             item.Provider,
		"platform_host":        item.PlatformHost,
		"owner":                item.Owner,
		"name":                 item.Name,
		"mr_number":            item.Number,
		"suppress_auto_assign": true,
	}, &workspace)
	if err != nil {
		return daemonSpawnWorkspace{}, false, err
	}
	if strings.TrimSpace(workspace.ID) == "" {
		return daemonSpawnWorkspace{}, false, fmt.Errorf("daemon workspace response missing id")
	}
	return workspace, !workspace.Created, nil
}

func (s *Server) resolveOrCreateIssueWorkspace(
	ctx context.Context,
	item itemRefInput,
) (daemonSpawnWorkspace, bool, error) {
	var detail daemonIssueDetail
	if err := s.daemon.getJSON(ctx, itemPath("issues", item), nil, &detail); err != nil {
		return daemonSpawnWorkspace{}, false, err
	}
	if detail.Issue == nil {
		return daemonSpawnWorkspace{}, false, fmt.Errorf("daemon issue detail missing issue")
	}
	if detail.Workspace != nil && strings.TrimSpace(detail.Workspace.ID) != "" {
		return daemonSpawnWorkspace{
			ID: detail.Workspace.ID, Status: detail.Workspace.Status,
		}, true, nil
	}
	var workspace daemonSpawnWorkspace
	if err := s.daemon.postJSON(
		ctx,
		itemPath("issues", item)+"/workspace",
		map[string]any{"suppress_auto_assign": true},
		&workspace,
	); err != nil {
		return daemonSpawnWorkspace{}, false, err
	}
	if strings.TrimSpace(workspace.ID) == "" {
		return daemonSpawnWorkspace{}, false, fmt.Errorf("daemon workspace response missing id")
	}
	return workspace, !workspace.Created, nil
}

func (s *Server) createAdHocWorkspace(
	ctx context.Context,
	source adHocWorkspaceSource,
) (daemonSpawnWorkspace, bool, error) {
	body := map[string]any{}
	if source.Branch != "" {
		body["branch"] = source.Branch
	}
	var workspace daemonSpawnWorkspace
	if err := s.daemon.postJSON(
		ctx, repoWorkspacePath(source.Repo), body, &workspace,
	); err != nil {
		return daemonSpawnWorkspace{}, false, err
	}
	if strings.TrimSpace(workspace.ID) == "" {
		return daemonSpawnWorkspace{}, false, fmt.Errorf("daemon workspace response missing id")
	}
	return workspace, !workspace.Created, nil
}

func repoWorkspacePath(repo repoFilterInput) string {
	if repo.PlatformHost != "" {
		return fmt.Sprintf(
			"/api/v1/host/%s/repo/%s/%s/%s/workspaces",
			seg(repo.PlatformHost), seg(repo.Provider), seg(repo.Owner), seg(repo.Name),
		)
	}
	return fmt.Sprintf(
		"/api/v1/repo/%s/%s/%s/workspaces",
		seg(repo.Provider), seg(repo.Owner), seg(repo.Name),
	)
}

func (s *Server) waitForWorkspaceReady(
	ctx context.Context,
	workspaceID string,
) (daemonSpawnWorkspace, error) {
	path := "/api/v1/workspaces/" + seg(workspaceID)
	for {
		var workspace daemonSpawnWorkspace
		if err := s.daemon.getJSON(ctx, path, nil, &workspace); err != nil {
			return workspace, err
		}
		switch workspace.Status {
		case "ready":
			return workspace, nil
		case "error":
			message := "workspace setup failed"
			if workspace.ErrorMessage != nil && strings.TrimSpace(*workspace.ErrorMessage) != "" {
				message += ": " + strings.TrimSpace(*workspace.ErrorMessage)
			}
			return workspace, errors.New(message)
		}
		if err := waitAgentHandoffPoll(ctx); err != nil {
			return workspace, err
		}
	}
}

func (s *Server) waitForCodingSession(
	ctx context.Context,
	workspaceID string,
	runtimeSessionKey string,
) (workspaceAgentSessionRow, error) {
	for {
		response, err := s.listWorkspaceAgentSessions(
			ctx, listWorkspaceAgentSessionsInput{WorkspaceID: workspaceID},
		)
		if err != nil {
			return workspaceAgentSessionRow{}, err
		}
		for _, session := range response.Sessions {
			if session.RuntimeSessionKey == runtimeSessionKey {
				return session, nil
			}
		}
		if err := s.ensureRuntimeStillLive(ctx, workspaceID, runtimeSessionKey); err != nil {
			return workspaceAgentSessionRow{}, err
		}
		if err := waitAgentHandoffPoll(ctx); err != nil {
			return workspaceAgentSessionRow{}, err
		}
	}
}

func (s *Server) ensureRuntimeStillLive(
	ctx context.Context,
	workspaceID string,
	runtimeSessionKey string,
) error {
	var runtime daemonWorkspaceRuntime
	if err := s.daemon.getJSON(
		ctx, "/api/v1/workspaces/"+seg(workspaceID)+"/runtime", nil, &runtime,
	); err != nil {
		return err
	}
	for _, session := range runtime.Sessions {
		if session.Key != runtimeSessionKey {
			continue
		}
		if session.Status == "starting" || session.Status == "running" {
			return nil
		}
		break
	}
	return fmt.Errorf("agent runtime exited before its coding session was observed")
}

func waitAgentHandoffPoll(ctx context.Context) error {
	timer := time.NewTimer(agentHandoffPollInterval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (s *Server) submitInitialAgentMessage(
	ctx context.Context,
	workspaceID string,
	runtimeSessionKey string,
	session workspaceAgentSessionRow,
	message string,
) (agentInitialMessageRow, error) {
	path := "/api/v1/workspaces/" + seg(workspaceID) +
		"/runtime/sessions/" + seg(runtimeSessionKey) + "/initial-message"
	var receipt daemonAgentInitialMessage
	err := s.daemon.postJSON(ctx, path, map[string]any{
		"agent":      session.Agent,
		"session_id": session.SessionID,
		"message":    message,
	}, &receipt)
	if err != nil {
		var daemonErr *daemonError
		if !errors.As(err, &daemonErr) || !daemonErr.Ambiguous {
			return agentInitialMessageRow{}, err
		}
		if getErr := s.daemon.getJSON(ctx, path, nil, &receipt); getErr != nil {
			return agentInitialMessageRow{}, err
		}
	}
	row := agentInitialMessageRow{
		State: receipt.State, MessageBytes: receipt.MessageBytes,
	}
	if receipt.DeliveredAt != nil {
		row.DeliveredAt = formatMCPTime(*receipt.DeliveredAt)
	}
	return row, nil
}

func handoffFailure(
	ctx context.Context,
	cause error,
	state spawnWorkspaceWithAgentOutput,
	lastCompletedStage string,
	failedStage string,
) *daemonError {
	result := &daemonError{
		Kind:      "agent_handoff_failed",
		Message:   cause.Error(),
		Retryable: false,
		Details: map[string]any{
			"failed_stage":      failedStage,
			"message_delivered": false,
		},
	}
	var daemonErr *daemonError
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		result.Kind = "agent_handoff_timeout"
		result.Message = "agent handoff timed out"
	} else if errors.As(cause, &daemonErr) {
		result.Kind = daemonErr.Kind
		result.Code = daemonErr.Code
		result.Message = daemonErr.Message
		result.Ambiguous = daemonErr.Ambiguous
		maps.Copy(result.Details, daemonErr.Details)
	} else if errors.Is(cause, context.DeadlineExceeded) {
		result.Kind = "agent_handoff_timeout"
		result.Message = "agent handoff timed out"
	}
	if lastCompletedStage != "" {
		result.Details["last_completed_stage"] = lastCompletedStage
	}
	if state.Workspace.ID != "" {
		result.Details["workspace_id"] = state.Workspace.ID
		result.Details["workspace_status"] = state.Workspace.Status
		result.Details["workspace_reused"] = state.Workspace.Reused
	}
	if state.Runtime.SessionKey != "" {
		result.Details["runtime_session_key"] = state.Runtime.SessionKey
		result.Details["target_key"] = state.Runtime.TargetKey
	}
	if state.CodingSession.SessionID != "" {
		result.Details["agent"] = state.CodingSession.Agent
		result.Details["session_id"] = state.CodingSession.SessionID
	}
	if state.InitialMessage != nil {
		result.Details["initial_message_state"] = state.InitialMessage.State
	}
	return result
}

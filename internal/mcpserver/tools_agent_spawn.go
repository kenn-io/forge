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
	defaultAgentHandoffTimeout      = 5 * time.Minute
	maxAgentHandoffTimeout          = 15 * time.Minute
	defaultAgentHandoffPollInterval = 250 * time.Millisecond
	messageStatusRecoveryTimeout    = 6 * time.Second
	maxAgentInitialMessage          = 64 << 10
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
	Stage          string                   `json:"stage"`
	Source         workspaceSourceInput     `json:"source"`
	Workspace      spawnedWorkspace         `json:"workspace"`
	Runtime        spawnedRuntime           `json:"runtime"`
	CodingSession  workspaceAgentSessionRow `json:"coding_session"`
	InitialMessage *agentInitialMessageRow  `json:"initial_message,omitempty"`
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

	out := spawnWorkspaceWithAgentOutput{Source: in.Source}
	targets, err := s.listAgentTargets(ctx, listAgentTargetsInput{})
	if err != nil {
		return out, handoffFailure(ctx, err, out, "", "")
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
	out.Workspace.Status = workspace.Status
	if err != nil {
		return out, handoffFailure(ctx, err, out, "workspace_created", "workspace_ready")
	}
	out.Stage = "workspace_ready"
	out.Workspace.Status = workspace.Status

	runtime, err := s.backend.LaunchWorkspaceRuntime(ctx, workspace.ID, in.AgentTarget)
	if err != nil {
		return out, handoffFailure(ctx, err, out, "workspace_ready", "runtime_launched")
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

	messageStatus, err := s.submitInitialAgentMessage(
		ctx, workspace.ID, runtime.Key, codingSession, in.InitialMessage,
	)
	if err != nil {
		return out, handoffFailure(ctx, err, out, "coding_session_observed", "message_delivered")
	}
	out.InitialMessage = &messageStatus
	if messageStatus.State != "delivered" {
		stateErr := &Error{
			Kind:      "agent_handoff_failed",
			Message:   fmt.Sprintf("initial message state is %s", messageStatus.State),
			Ambiguous: messageStatus.State == "pending" || messageStatus.State == "uncertain",
			Details:   map[string]any{"initial_message_state": messageStatus.State},
		}
		return out, handoffFailure(
			ctx, stateErr, out, "coding_session_observed", "message_delivered",
		)
	}
	out.Stage = "message_delivered"
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
	item.PlatformRepoID = strings.TrimSpace(item.PlatformRepoID)
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
	repo.PlatformRepoID = strings.TrimSpace(repo.PlatformRepoID)
	repo.RepoPath = strings.Trim(strings.TrimSpace(repo.RepoPath), "/")
	repo.Owner = strings.Trim(strings.TrimSpace(repo.Owner), "/")
	repo.Name = strings.Trim(strings.TrimSpace(repo.Name), "/")
	if repo.Provider == "" {
		return repo, fmt.Errorf("repo provider is required")
	}
	if repo.PlatformRepoID == "" {
		return repo, fmt.Errorf("repo platform_repo_id is required")
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
) (Workspace, bool, error) {
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
	return Workspace{}, false, fmt.Errorf("unsupported workspace source")
}

func (s *Server) resolveOrCreatePRWorkspace(
	ctx context.Context,
	item itemRefInput,
) (Workspace, bool, error) {
	detail, err := s.backend.GetPull(ctx, itemIdentity(item))
	if err != nil {
		return Workspace{}, false, err
	}
	if detail.Workspace != nil {
		return Workspace{ID: detail.Workspace.ID, Status: detail.Workspace.Status}, true, nil
	}
	workspace, err := s.backend.CreatePullWorkspace(ctx, itemIdentity(item), true)
	if err == nil {
		return workspace, !workspace.Created, nil
	}
	if !isWorkspaceAlreadyExistsError(err) {
		return Workspace{}, false, err
	}
	detail, readErr := s.backend.GetPull(ctx, itemIdentity(item))
	if readErr != nil {
		return Workspace{}, false, readErr
	}
	if detail.Workspace == nil {
		return Workspace{}, false, err
	}
	return Workspace{ID: detail.Workspace.ID, Status: detail.Workspace.Status}, true, nil
}

func isWorkspaceAlreadyExistsError(err error) bool {
	var backendErr *Error
	if !errors.As(err, &backendErr) || backendErr == nil {
		return false
	}
	return backendErr.Kind == "conflict" &&
		backendErr.Code == ErrorCodeWorkspaceAlreadyExists
}

func (s *Server) resolveOrCreateIssueWorkspace(
	ctx context.Context,
	item itemRefInput,
) (Workspace, bool, error) {
	detail, err := s.backend.GetIssue(ctx, itemIdentity(item))
	if err != nil {
		return Workspace{}, false, err
	}
	if detail.Workspace != nil {
		return Workspace{ID: detail.Workspace.ID, Status: detail.Workspace.Status}, true, nil
	}
	workspace, err := s.backend.CreateIssueWorkspace(ctx, itemIdentity(item), true)
	if err != nil {
		return Workspace{}, false, err
	}
	return workspace, !workspace.Created, nil
}

func (s *Server) createAdHocWorkspace(
	ctx context.Context,
	source adHocWorkspaceSource,
) (Workspace, bool, error) {
	repository, err := source.Repo.repositoryIdentity()
	if err != nil {
		return Workspace{}, false, err
	}
	workspace, err := s.backend.CreateAdHocWorkspace(ctx, repository, source.Branch)
	if err != nil {
		return Workspace{}, false, err
	}
	return workspace, !workspace.Created, nil
}

func (s *Server) waitForWorkspaceReady(
	ctx context.Context,
	workspaceID string,
) (Workspace, error) {
	for {
		workspace, err := s.backend.GetWorkspace(ctx, workspaceID)
		if err != nil {
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
		if err := s.waitAgentHandoffPoll(ctx); err != nil {
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
		if err := s.waitAgentHandoffPoll(ctx); err != nil {
			return workspaceAgentSessionRow{}, err
		}
	}
}

func (s *Server) ensureRuntimeStillLive(
	ctx context.Context,
	workspaceID string,
	runtimeSessionKey string,
) error {
	runtime, err := s.backend.GetWorkspaceRuntime(ctx, workspaceID)
	if err != nil {
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

func (s *Server) waitAgentHandoffPoll(ctx context.Context) error {
	interval := s.agentHandoffPollInterval
	if interval <= 0 {
		interval = defaultAgentHandoffPollInterval
	}
	timer := time.NewTimer(interval)
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
	messageStatus, err := s.backend.SubmitInitialMessage(ctx, InitialMessageRequest{
		WorkspaceID: workspaceID, RuntimeSessionKey: runtimeSessionKey,
		Agent: session.Agent, SessionID: session.SessionID, Message: message,
	})
	if err != nil {
		var backendErr *Error
		if !errors.As(err, &backendErr) || !backendErr.Ambiguous {
			return agentInitialMessageRow{}, err
		}
		messageStatus, err = s.recoverInitialMessageStatus(
			ctx, workspaceID, runtimeSessionKey, backendErr,
		)
		if err != nil {
			return agentInitialMessageRow{}, err
		}
	}
	row := agentInitialMessageRow{
		State: messageStatus.State, MessageBytes: messageStatus.MessageBytes,
	}
	if messageStatus.DeliveredAt != nil {
		row.DeliveredAt = formatMCPTime(*messageStatus.DeliveredAt)
	}
	return row, nil
}

func (s *Server) recoverInitialMessageStatus(
	ctx context.Context,
	workspaceID string,
	runtimeSessionKey string,
	original *Error,
) (InitialMessageStatus, error) {
	timeout := messageStatusRecoveryTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	for {
		recovered, err := s.backend.GetInitialMessage(recoveryCtx, workspaceID, runtimeSessionKey)
		if err != nil {
			return InitialMessageStatus{}, original
		}
		if recovered.State == "delivered" {
			return recovered, nil
		}
		if recovered.State != "pending" {
			return InitialMessageStatus{}, initialMessageRecoveryError(original, recovered.State)
		}
		if err := s.waitAgentHandoffPoll(recoveryCtx); err != nil {
			return InitialMessageStatus{}, initialMessageRecoveryError(original, recovered.State)
		}
	}
}

func initialMessageRecoveryError(original *Error, state string) *Error {
	recovered := *original
	recovered.Details = maps.Clone(original.Details)
	if recovered.Details == nil {
		recovered.Details = make(map[string]any)
	}
	recovered.Details["initial_message_state"] = state
	return &recovered
}

func handoffFailure(
	ctx context.Context,
	cause error,
	state spawnWorkspaceWithAgentOutput,
	lastCompletedStage string,
	failedStage string,
) *Error {
	result := &Error{
		Kind: "agent_handoff_failed", Message: cause.Error(), Retryable: false,
		Details: map[string]any{},
	}
	var backendErr *Error
	if errors.As(cause, &backendErr) {
		result.Kind = backendErr.Kind
		result.Code = backendErr.Code
		result.Message = backendErr.Message
		result.Ambiguous = backendErr.Ambiguous
		maps.Copy(result.Details, backendErr.Details)
	}
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		result.Kind = "agent_handoff_timeout"
		result.Message = "agent handoff timed out"
	} else if backendErr == nil && errors.Is(cause, context.DeadlineExceeded) {
		result.Kind = "agent_handoff_timeout"
		result.Message = "agent handoff timed out"
	}
	if failedStage != "" {
		result.Details["failed_stage"] = failedStage
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

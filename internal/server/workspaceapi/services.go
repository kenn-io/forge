package workspaceapi

import (
	"time"

	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

type CreatePullWorkspaceRequest struct {
	Provider           string
	PlatformHost       string
	Owner              string
	Name               string
	Number             int
	SuppressAutoAssign bool
}

type CreateIssueWorkspaceRequest struct {
	Provider               string
	PlatformHost           string
	Owner                  string
	Name                   string
	Number                 int
	GitHeadRef             *string
	ReuseExistingBranch    bool
	ReuseExistingDirectory bool
	SuppressAutoAssign     bool
}

type CreateAdHocWorkspaceRequest struct {
	Provider            string
	PlatformHost        string
	Owner               string
	Name                string
	Branch              *string
	ReuseExistingBranch bool
}

type WorkspaceResult struct {
	Workspace WorkspaceResponse
}

type WorkspaceRuntimeResult struct {
	LaunchTargets []localruntime.LaunchTarget
	Sessions      []localruntime.SessionInfo
}

type AgentSessionResult struct {
	Agent             string
	SessionID         string
	RuntimeSessionKey string
	TargetKey         string
	State             agentactivity.State
	UpdatedAt         time.Time
	InitialMessage    *InitialMessageResult
}

type InitialMessageRequest struct {
	WorkspaceID       string
	RuntimeSessionKey string
	Agent             string
	SessionID         string
	Message           string
}

type InitialMessageResult struct {
	Agent        string
	SessionID    string
	State        string
	MessageBytes int
	ReservedAt   time.Time
	DeliveredAt  *time.Time
}

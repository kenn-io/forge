package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/modules/compose"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.kenn.io/middleman/internal/fleet"
)

var fleetContainerUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestFleetContainerReadE2E(t *testing.T) {
	if os.Getenv("MIDDLEMAN_FLEET_CONTAINER_E2E") != "1" {
		t.Skip("set MIDDLEMAN_FLEET_CONTAINER_E2E=1 to run fleet container e2e")
	}

	assert := assert.New(t)
	require := require.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()

	hubPort := envOrDefault("MIDDLEMAN_FLEET_HUB_PORT", freeLoopbackPort(t))
	memberPort := envOrDefault("MIDDLEMAN_FLEET_MEMBER_PORT", freeLoopbackPort(t))
	stackID := compose.StackIdentifier(envOrDefault("MIDDLEMAN_FLEET_COMPOSE_PROJECT", "middleman-fleet-e2e"))
	stack, err := compose.NewDockerComposeWith(
		compose.WithStackFiles(filepath.Join(repoRoot(t), "scripts/e2e/fleet/docker-compose.yml")),
		stackID,
	)
	require.NoError(err)

	composeStack := stack.
		WithEnv(map[string]string{
			"MIDDLEMAN_FLEET_HUB_PORT":    hubPort,
			"MIDDLEMAN_FLEET_MEMBER_PORT": memberPort,
		}).
		WaitForService("hub", waitForFleetContainerPublishedHTTP()).
		WaitForService("member", waitForFleetContainerInternalHTTP()).
		WaitForService("member-ssh", waitForFleetContainerInternalHTTP())
	err = composeStack.Up(ctx, compose.Wait(true))
	hubContainer, hubContainerErr := composeStack.ServiceContainer(ctx, "hub")
	memberContainer, memberContainerErr := composeStack.ServiceContainer(ctx, "member")
	memberSSHContainer, memberSSHContainerErr := composeStack.ServiceContainer(ctx, "member-ssh")
	if err != nil {
		if hubContainerErr == nil {
			t.Logf("hub logs:\n%s", containerLogs(ctx, hubContainer))
		}
		if memberContainerErr == nil {
			t.Logf("member logs:\n%s", containerLogs(ctx, memberContainer))
		}
		if memberSSHContainerErr == nil {
			t.Logf("member-ssh logs:\n%s", containerLogs(ctx, memberSSHContainer))
		}
		require.NoError(err)
	}
	require.NoError(hubContainerErr)
	require.NoError(memberContainerErr)
	require.NoError(memberSSHContainerErr)
	if os.Getenv("MIDDLEMAN_KEEP_FLEET_FIXTURE") == "1" {
		t.Logf("keeping fleet Compose stack %s at http://127.0.0.1:%s", stackID, hubPort)
	} else {
		t.Cleanup(func() {
			assert.NoError(composeStack.Down(
				context.Background(),
				compose.RemoveOrphans(true),
				compose.RemoveVolumes(true),
			))
		})
	}

	seedFleetContainerMember(t, ctx, memberContainer)

	hubURL, err := hubContainer.PortEndpoint(ctx, "18091/tcp", "http")
	require.NoError(err)
	var snap fleet.Snapshot
	require.Eventually(func() bool {
		resp, getErr := (&http.Client{Timeout: 10 * time.Second}).Get(
			hubURL + "/api/v1/snapshot?include_peers=true",
		)
		if getErr != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var next fleet.Snapshot
		if err := json.NewDecoder(resp.Body).Decode(&next); err != nil {
			return false
		}
		member := fleetContainerHostByKey(next.Hosts, "member")
		if member == nil || len(member.TmuxSessions) != 1 {
			return false
		}
		snap = next
		return true
	}, 20*time.Second, 500*time.Millisecond, "member tmux inventory must fan out")

	hub := fleetContainerHostByKey(snap.Hosts, "hub")
	require.NotNil(hub, "hub host present")
	assert.True(hub.Reachable)
	assertFleetContainerUUID(t, hub.ID)
	assert.NotEqual("hub", hub.ID)

	member := fleetContainerHostByKey(snap.Hosts, "member")
	require.NotNil(member, "member host present")
	assert.True(member.Reachable)
	assertFleetContainerUUID(t, member.ID)
	assert.NotEqual("member", member.ID)
	require.Len(member.TmuxSessions, 1, "member host tmux inventory must fan out from its raw snapshot")
	assert.Equal("middleman-fleet-member-ws-7", member.TmuxSessions[0].Name)
	assert.Equal("worktree:/data/member/worktrees/widget-pr-7", member.TmuxSessions[0].WorktreeKey)

	down := fleetContainerHostByKey(snap.Hosts, "down")
	require.NotNil(down, "unreachable peer host present")
	assert.False(down.Reachable)
	require.NotNil(down.Error)
	assert.NotEmpty(*down.Error)

	project := fleetContainerProjectByName(snap.Projects, "fleet-widget")
	require.NotNil(project, "seeded member project present")
	assert.Equal(member.ID, project.HostID)
	assertFleetContainerUUID(t, project.ID)
	assert.NotEqual("repo:/data/member/projects/fleet-widget", project.ID)

	worktree := fleetContainerWorktreeByName(snap.Worktrees, "widget-pr-7")
	require.NotNil(worktree, "seeded member workspace worktree present")
	assert.Equal(member.ID, worktree.HostID)
	require.NotNil(worktree.LinkedPRNumber)
	assert.Equal(7, *worktree.LinkedPRNumber)
	assertFleetContainerUUID(t, worktree.ID)
	assert.NotEqual("worktree:/data/member/worktrees/widget-pr-7", worktree.ID)

	session := fleetContainerSessionByWorktreeID(snap.Sessions, worktree.ID)
	require.NotNil(session, "DB-synthesized main tmux session present")
	assert.Equal(member.ID, session.HostID)
	assert.Equal("tmux", session.RuntimeKind)
	assert.Equal("running", session.Status)
	assertFleetContainerUUID(t, session.ID)
	assert.NotEqual("session:fleet-member-ws-7:main", session.ID)

	assertHubPolicyAvailability(t, member.OperationAvailability)
	assertOfflineAvailability(t, down.OperationAvailability)
}

func TestFleetContainerDriveE2E(t *testing.T) {
	if os.Getenv("MIDDLEMAN_FLEET_DRIVE_CONTAINER_E2E") != "1" {
		t.Skip("set MIDDLEMAN_FLEET_DRIVE_CONTAINER_E2E=1 to run fleet drive container e2e")
	}

	assert := assert.New(t)
	require := require.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()

	hubURL, memberContainer, memberSSHContainer := startFleetDriveContainerStack(t, ctx)
	seedFleetContainerMemberForDrive(t, ctx, memberContainer)
	seedFleetContainerMemberSSHForDrive(t, ctx, memberSSHContainer)
	assertFleetContainerWorkspaceDiffSurface(t, hubURL, "member")
	assertFleetContainerWorkspaceDiffSurface(t, hubURL, "member-ssh")

	var launched struct {
		Key         string `json:"key"`
		WorkspaceID string `json:"workspace_id"`
		TargetKey   string `json:"target_key"`
		Status      string `json:"status"`
	}
	status, body := postFleetContainerJSON(
		t,
		hubURL+"/api/v1/fleet/hosts/member/workspaces/fleet-member-ws-7/runtime/sessions",
		map[string]any{"target_key": "drive-helper"},
		&launched,
	)
	require.Equal(http.StatusOK, status, string(body))
	assert.Equal("fleet-member-ws-7", launched.WorkspaceID)
	assert.Equal("drive-helper", launched.TargetKey)
	assert.Equal("running", launched.Status)
	require.NotEmpty(launched.Key)

	terminalCtx, terminalCancel := context.WithTimeout(ctx, 15*time.Second)
	defer terminalCancel()
	fleetContainerWebSocketReadUntil(
		t,
		terminalCtx,
		fleetContainerWSURL(
			hubURL,
			"/ws/v1/fleet/hosts/member/workspaces/fleet-member-ws-7/runtime/sessions/"+
				escapePath(launched.Key)+"/terminal?cols=80&rows=24",
		),
		"drive-helper-ready",
	)
	fleetContainerWebSocketWriteRead(
		t,
		terminalCtx,
		fleetContainerWSURL(
			hubURL,
			"/ws/v1/fleet/hosts/member/workspaces/fleet-member-ws-7/terminal?cols=80&rows=24",
		),
		"printf tmux-drive-ok\\n",
		"tmux-drive-ok",
	)

	status, body = deleteFleetContainer(
		t,
		hubURL+"/api/v1/fleet/hosts/member/workspaces/fleet-member-ws-7/runtime/sessions/"+escapePath(launched.Key),
	)
	require.Equal(http.StatusNoContent, status, string(body))

	var sshLaunch struct {
		Key         string `json:"key"`
		WorkspaceID string `json:"workspace_id"`
		TargetKey   string `json:"target_key"`
		Status      string `json:"status"`
	}
	status, body = postFleetContainerJSON(
		t,
		hubURL+"/api/v1/fleet/hosts/member-ssh/workspaces/fleet-member-ws-7/runtime/sessions",
		map[string]any{"target_key": "ssh-interactive-helper"},
		&sshLaunch,
	)
	require.Equal(http.StatusOK, status, string(body))
	assert.Equal("fleet-member-ws-7", sshLaunch.WorkspaceID)
	assert.Equal("ssh-interactive-helper", sshLaunch.TargetKey)
	assert.Equal("running", sshLaunch.Status)
	require.NotEmpty(sshLaunch.Key)

	sshTerminalCtx, sshTerminalCancel := context.WithTimeout(ctx, 45*time.Second)
	defer sshTerminalCancel()
	fleetContainerWebSocketReadUntil(
		t,
		sshTerminalCtx,
		fleetContainerWSURL(
			hubURL,
			"/ws/v1/fleet/hosts/member-ssh/workspaces/fleet-member-ws-7/runtime/sessions/"+
				escapePath(sshLaunch.Key)+"/terminal?cols=80&rows=24",
		),
		"ssh-helper-ready",
	)
	fleetContainerWebSocketWriteRead(
		t,
		sshTerminalCtx,
		fleetContainerWSURL(
			hubURL,
			"/ws/v1/fleet/hosts/member-ssh/workspaces/fleet-member-ws-7/runtime/sessions/"+
				escapePath(sshLaunch.Key)+"/terminal?cols=80&rows=24",
		),
		"hello-over-ssh\n",
		"ssh:hello-over-ssh",
	)

	status, body = deleteFleetContainer(
		t,
		hubURL+"/api/v1/fleet/hosts/member-ssh/workspaces/fleet-member-ws-7/runtime/sessions/"+escapePath(sshLaunch.Key),
	)
	require.Equal(http.StatusNoContent, status, string(body))

	projectID, worktreeID := fleetContainerRegisteredWorktreeIDs(
		t, ctx, memberContainer,
	)
	var registeredLaunch struct {
		Key        string `json:"key"`
		ProjectID  string `json:"project_id"`
		WorktreeID string `json:"worktree_id"`
		TargetKey  string `json:"target_key"`
		Status     string `json:"status"`
	}
	status, body = postFleetContainerJSON(
		t,
		hubURL+"/api/v1/fleet/hosts/member/projects/"+escapePath(projectID)+
			"/worktrees/"+escapePath(worktreeID)+"/runtime/sessions",
		map[string]any{"target_key": "drive-helper"},
		&registeredLaunch,
	)
	require.Equal(http.StatusOK, status, string(body))
	assert.Equal(projectID, registeredLaunch.ProjectID)
	assert.Equal(worktreeID, registeredLaunch.WorktreeID)
	assert.Equal("drive-helper", registeredLaunch.TargetKey)
	assert.Equal("running", registeredLaunch.Status)
	require.NotEmpty(registeredLaunch.Key)

	registeredTmux := fleetContainerRuntimeTmuxSessionName(
		"project-worktree:"+worktreeID, registeredLaunch.Key,
	)
	require.Equal(
		0,
		fleetContainerExecCode(
			t, ctx, memberContainer, "tmux", "has-session", "-t", registeredTmux,
		),
	)

	var (
		driveSnap          fleet.Snapshot
		member             *fleet.HostSummary
		registeredWorktree *fleet.WorktreeSummary
		summarySession     *fleet.SessionSummary
	)
	sessionScopedKey := "session:" + registeredLaunch.Key
	require.Eventually(func() bool {
		getFleetContainerJSON(
			t, hubURL+"/api/v1/snapshot?include_peers=true", &driveSnap,
		)
		member = fleetContainerHostByKey(driveSnap.Hosts, "member")
		registeredWorktree = fleetContainerWorktreeByName(
			driveSnap.Worktrees, "registered-runtime",
		)
		summarySession = fleetContainerSessionByScopedKey(
			driveSnap.Sessions, sessionScopedKey,
		)
		return member != nil &&
			registeredWorktree != nil &&
			summarySession != nil
	}, 20*time.Second, 500*time.Millisecond)

	require.NotNil(registeredWorktree)
	assert.Equal(member.ID, registeredWorktree.HostID)
	require.NotNil(summarySession)
	assert.Equal(member.ID, summarySession.HostID)
	require.NotNil(summarySession.WorktreeID)
	assert.Equal(registeredWorktree.ID, *summarySession.WorktreeID)
	assert.Equal("agent", summarySession.RuntimeKind)

	status, body = deleteFleetContainer(
		t,
		hubURL+"/api/v1/fleet/hosts/member/projects/"+escapePath(projectID)+
			"/worktrees/"+escapePath(worktreeID)+"/runtime/sessions/"+
			escapePath(registeredLaunch.Key),
	)
	require.Equal(http.StatusNoContent, status, string(body))
	require.NotEqual(
		0,
		fleetContainerExecCode(
			t, ctx, memberContainer, "tmux", "has-session", "-t", registeredTmux,
		),
	)

	status, body = deleteFleetContainer(
		t,
		hubURL+"/api/v1/fleet/hosts/member/workspaces/fleet-member-ws-7?force=true",
	)
	require.Equal(http.StatusNoContent, status, string(body))

	var snap fleet.Snapshot
	getFleetContainerJSON(t, hubURL+"/api/v1/snapshot?include_peers=true", &snap)

	memberAfterDelete := fleetContainerHostByKey(snap.Hosts, "member")
	require.NotNil(memberAfterDelete)
	assert.Nil(fleetContainerWorktreeByHostAndName(
		snap.Worktrees, memberAfterDelete.ID, "widget-pr-7",
	))
	for _, tmuxSession := range memberAfterDelete.TmuxSessions {
		assert.NotEqual("middleman-fleet-member-ws-7", tmuxSession.Name)
	}
}

func waitForFleetContainerPublishedHTTP() wait.Strategy {
	return wait.ForListeningPort("18091/tcp").WithStartupTimeout(5 * time.Minute)
}

func waitForFleetContainerInternalHTTP() wait.Strategy {
	return wait.ForExec([]string{
		"curl", "-fsS", "http://127.0.0.1:8091/healthz",
	}).WithStartupTimeout(5 * time.Minute)
}

func startFleetDriveContainerStack(
	t *testing.T,
	ctx context.Context,
) (string, testcontainers.Container, testcontainers.Container) {
	t.Helper()
	assert := assert.New(t)
	require := require.New(t)

	hubPort := envOrDefault("MIDDLEMAN_FLEET_DRIVE_HUB_PORT", freeLoopbackPort(t))
	memberPort := envOrDefault("MIDDLEMAN_FLEET_DRIVE_MEMBER_PORT", freeLoopbackPort(t))
	stackID := compose.StackIdentifier(envOrDefault("MIDDLEMAN_FLEET_DRIVE_COMPOSE_PROJECT", "middleman-fleet-drive-e2e"))
	stack, err := compose.NewDockerComposeWith(
		compose.WithStackFiles(filepath.Join(repoRoot(t), "scripts/e2e/fleet/docker-compose.yml")),
		stackID,
	)
	require.NoError(err)

	composeStack := stack.
		WithEnv(map[string]string{
			"MIDDLEMAN_FLEET_HUB_PORT":    hubPort,
			"MIDDLEMAN_FLEET_MEMBER_PORT": memberPort,
		}).
		WaitForService("hub", waitForFleetContainerPublishedHTTP()).
		WaitForService("member", waitForFleetContainerInternalHTTP()).
		WaitForService("member-ssh", waitForFleetContainerInternalHTTP())
	err = composeStack.Up(ctx, compose.Wait(true))
	hubContainer, hubContainerErr := composeStack.ServiceContainer(ctx, "hub")
	memberContainer, memberContainerErr := composeStack.ServiceContainer(ctx, "member")
	memberSSHContainer, memberSSHContainerErr := composeStack.ServiceContainer(ctx, "member-ssh")
	if err != nil {
		if hubContainerErr == nil {
			t.Logf("hub logs:\n%s", containerLogs(ctx, hubContainer))
		}
		if memberContainerErr == nil {
			t.Logf("member logs:\n%s", containerLogs(ctx, memberContainer))
		}
		if memberSSHContainerErr == nil {
			t.Logf("member-ssh logs:\n%s", containerLogs(ctx, memberSSHContainer))
		}
		require.NoError(err)
	}
	require.NoError(hubContainerErr)
	require.NoError(memberContainerErr)
	require.NoError(memberSSHContainerErr)
	if os.Getenv("MIDDLEMAN_KEEP_FLEET_FIXTURE") == "1" {
		t.Logf("keeping fleet drive Compose stack %s at http://127.0.0.1:%s", stackID, hubPort)
	} else {
		t.Cleanup(func() {
			assert.NoError(composeStack.Down(
				context.Background(),
				compose.RemoveOrphans(true),
				compose.RemoveVolumes(true),
			))
		})
	}

	hubURL, err := hubContainer.PortEndpoint(ctx, "18091/tcp", "http")
	require.NoError(err)
	return hubURL, memberContainer, memberSSHContainer
}

func seedFleetContainerMember(
	t *testing.T,
	ctx context.Context,
	container testcontainers.Container,
) {
	t.Helper()
	seedFleetContainerMemberWithArgs(t, ctx, container, "-start-tmux")
}

func seedFleetContainerMemberForDrive(
	t *testing.T,
	ctx context.Context,
	container testcontainers.Container,
) {
	t.Helper()
	seedFleetContainerMemberWithArgs(t, ctx, container, "-start-tmux")
}

func seedFleetContainerMemberWithArgs(
	t *testing.T,
	ctx context.Context,
	container testcontainers.Container,
	extraArgs ...string,
) {
	t.Helper()
	seedFleetContainerWithArgs(t, ctx, container, "/data/member", extraArgs...)
}

func seedFleetContainerMemberSSHForDrive(
	t *testing.T,
	ctx context.Context,
	container testcontainers.Container,
) {
	t.Helper()
	seedFleetContainerWithArgs(t, ctx, container, "/data/member-ssh", "-start-tmux")
}

func seedFleetContainerWithArgs(
	t *testing.T,
	ctx context.Context,
	container testcontainers.Container,
	dataRoot string,
	extraArgs ...string,
) {
	t.Helper()
	args := []string{
		"go", "run", "./scripts/e2e/fleet/seed",
		"-db", dataRoot + "/middleman.db",
		"-project-path", dataRoot + "/projects/fleet-widget",
		"-worktree-path", dataRoot + "/worktrees/widget-pr-7",
	}
	args = append(args, extraArgs...)
	code, reader, err := container.Exec(ctx, args, tcexec.WithWorkingDir("/app"), tcexec.Multiplexed())
	require.NoError(t, err)
	output, readErr := io.ReadAll(reader)
	require.NoError(t, readErr)
	require.Equal(t, 0, code, string(output)+"\n"+containerLogs(ctx, container))
}

func fleetContainerRegisteredWorktreeIDs(
	t *testing.T,
	ctx context.Context,
	container testcontainers.Container,
) (string, string) {
	t.Helper()
	var projects struct {
		Projects []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"projects"`
	}
	getFleetContainerJSONFromContainer(t, ctx, container, "/api/v1/projects", &projects)
	var projectID string
	for _, project := range projects.Projects {
		if project.DisplayName == "fleet-widget" {
			projectID = project.ID
			break
		}
	}
	require.NotEmpty(t, projectID)

	var worktrees struct {
		Worktrees []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"worktrees"`
	}
	getFleetContainerJSONFromContainer(
		t, ctx, container,
		"/api/v1/projects/"+escapePath(projectID)+"/worktrees",
		&worktrees,
	)
	for _, worktree := range worktrees.Worktrees {
		if strings.HasSuffix(worktree.Path, "/registered-runtime") {
			return projectID, worktree.ID
		}
	}
	require.Fail(t, "registered-runtime worktree not found")
	return "", ""
}

func getFleetContainerJSONFromContainer(
	t *testing.T,
	ctx context.Context,
	container testcontainers.Container,
	targetPath string,
	out any,
) {
	t.Helper()
	code, reader, err := container.Exec(
		ctx,
		[]string{"curl", "-fsS", "http://127.0.0.1:8091" + targetPath},
		tcexec.Multiplexed(),
	)
	require.NoError(t, err)
	body, readErr := io.ReadAll(reader)
	require.NoError(t, readErr)
	require.Equal(t, 0, code, string(body)+"\n"+containerLogs(ctx, container))
	require.NoError(t, json.Unmarshal(body, out), string(body))
}

func getFleetContainerJSON(t *testing.T, targetURL string, out any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, targetURL, http.NoBody)
	require.NoError(t, err)
	resp := doFleetContainerHTTPRequest(t, req, nil)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.NoError(t, json.Unmarshal(body, out), string(body))
}

func assertFleetContainerWorkspaceDiffSurface(
	t *testing.T,
	hubURL string,
	hostKey string,
) {
	t.Helper()
	assert := assert.New(t)
	require := require.New(t)
	baseURL := hubURL + "/api/v1/fleet/hosts/" + escapePath(hostKey) +
		"/workspaces/fleet-member-ws-7"

	var files struct {
		Files []fleetContainerDiffFile `json:"files"`
	}
	getFleetContainerJSON(t, baseURL+"/files?base=head", &files)
	dirtyFile := fleetContainerDiffFileByPath(files.Files, "dirty.txt")
	require.NotNil(dirtyFile)
	assert.Equal("added", dirtyFile.Status)

	var diff struct {
		Files []fleetContainerDiffFile `json:"files"`
	}
	getFleetContainerJSON(t, baseURL+"/diff?base=merge-target", &diff)
	featureFile := fleetContainerDiffFileByPath(diff.Files, "feature.txt")
	require.NotNil(featureFile)
	assert.Contains(featureFile.Patch, "+feature")

	var commits struct {
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
	}
	getFleetContainerJSON(t, baseURL+"/commits", &commits)
	require.NotEmpty(commits.Commits)
	assert.Equal("feature commit", commits.Commits[len(commits.Commits)-1].Message)

	var preview struct {
		Path     string `json:"path"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	getFleetContainerJSON(t, baseURL+"/file-preview?base=head&path=dirty.txt", &preview)
	assert.Equal("dirty.txt", preview.Path)
	assert.Equal("base64", preview.Encoding)
	content, err := base64.StdEncoding.DecodeString(preview.Content)
	require.NoError(err)
	assert.Equal("dirty\n", string(content))
}

type fleetContainerDiffFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Patch  string `json:"patch"`
}

func fleetContainerDiffFileByPath(files []fleetContainerDiffFile, path string) *fleetContainerDiffFile {
	for i := range files {
		if files[i].Path == path {
			return &files[i]
		}
	}
	return nil
}

func fleetContainerExecCode(
	t *testing.T,
	ctx context.Context,
	container testcontainers.Container,
	args ...string,
) int {
	t.Helper()
	code, reader, err := container.Exec(ctx, args, tcexec.Multiplexed())
	require.NoError(t, err)
	_, err = io.ReadAll(reader)
	require.NoError(t, err)
	return code
}

func fleetContainerRuntimeTmuxSessionName(scope, sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return "middleman-" + fleetContainerTmuxSessionSafeComponent(scope) + "-" +
		hex.EncodeToString(sum[:8])
}

func fleetContainerTmuxSessionSafeComponent(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_' || r == '-':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func postFleetContainerJSON(
	t *testing.T,
	targetURL string,
	body any,
	out any,
) (int, []byte) {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp := doFleetContainerHTTPRequest(t, req, payload)
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if out != nil && len(respBody) > 0 && resp.StatusCode < 400 {
		require.NoError(t, json.Unmarshal(respBody, out), string(respBody))
	}
	return resp.StatusCode, respBody
}

func deleteFleetContainer(t *testing.T, targetURL string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, targetURL, http.NoBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp := doFleetContainerHTTPRequest(t, req, nil)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}

func doFleetContainerHTTPRequest(
	t *testing.T,
	req *http.Request,
	body []byte,
) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	var lastErr error
	for range 10 {
		next := req.Clone(req.Context())
		if body != nil {
			next.Body = io.NopCloser(bytes.NewReader(body))
			next.ContentLength = int64(len(body))
		}
		resp, err := client.Do(next)
		if err == nil {
			return resp
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	require.NoError(t, lastErr)
	return nil
}

func fleetContainerWSURL(baseURL, path string) string {
	return "ws" + strings.TrimPrefix(baseURL, "http") + path
}

func fleetContainerWebSocketReadUntil(
	t *testing.T,
	ctx context.Context,
	wsURL string,
	needle string,
) {
	t.Helper()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil && resp != nil && resp.Body != nil {
		body, readErr := io.ReadAll(resp.Body)
		require.NoError(t, readErr)
		require.NoError(t, err, string(body))
	}
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "done")
	fleetContainerReadWebSocketUntil(t, ctx, conn, needle)
}

func fleetContainerWebSocketWriteRead(
	t *testing.T,
	ctx context.Context,
	wsURL string,
	input string,
	needle string,
) {
	t.Helper()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil && resp != nil && resp.Body != nil {
		body, readErr := io.ReadAll(resp.Body)
		require.NoError(t, readErr)
		require.NoError(t, err, string(body))
	}
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	require.NoError(t, conn.Write(ctx, websocket.MessageBinary, []byte(input)))
	fleetContainerReadWebSocketUntil(t, ctx, conn, needle)
}

func fleetContainerReadWebSocketUntil(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	needle string,
) {
	t.Helper()
	var got strings.Builder
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		got.WriteString(string(data))
		if strings.Contains(got.String(), needle) {
			return
		}
	}
	require.Contains(t, got.String(), needle)
}

func assertFleetContainerUUID(t *testing.T, value string) {
	t.Helper()
	require.NotEmpty(t, value)
	require.True(t, fleetContainerUUIDPattern.MatchString(value), "value %q must be a UUID", value)
}

func assertHubPolicyAvailability(
	t *testing.T,
	availability map[string]fleet.HostOperationAvailability,
) {
	t.Helper()
	require := require.New(t)
	assert := assert.New(t)
	hubReason := "Operation not routed via hub."

	// Every named operation is hub-routed now (clone via the fleet
	// clone proxy, project add/remove via the write-forward routes),
	// so none may carry the hub-suppression reason.
	for _, op := range []string{
		fleet.OpRepositoryClone,
		fleet.OpProjectAdd,
		fleet.OpProjectRemove,
		fleet.OpWorktreeCreate,
		fleet.OpPullRequestImport,
		fleet.OpWorktreeDelete,
		fleet.OpSessionEnsure,
		fleet.OpSessionKill,
		fleet.OpDurableSessions,
	} {
		got, ok := availability[op]
		require.True(ok, "operation availability includes %s", op)
		if got.UnavailableReason != nil {
			assert.NotEqual(hubReason, *got.UnavailableReason, "routable op %s must not carry hub policy reason", op)
		}
	}
}

func assertOfflineAvailability(
	t *testing.T,
	availability map[string]fleet.HostOperationAvailability,
) {
	t.Helper()
	require.NotEmpty(t, availability)
	for op, got := range availability {
		assert.False(t, got.Available, "offline op %s", op)
		require.NotNil(t, got.UnavailableReason, "offline op %s", op)
		assert.Equal(t, "Host is offline.", *got.UnavailableReason, "offline op %s", op)
	}
}

func fleetContainerHostByKey(hosts []fleet.HostSummary, configKey string) *fleet.HostSummary {
	for i := range hosts {
		if hosts[i].ConfigKey == configKey {
			return &hosts[i]
		}
	}
	return nil
}

func fleetContainerProjectByName(projects []fleet.ProjectSummary, name string) *fleet.ProjectSummary {
	for i := range projects {
		if projects[i].Name == name {
			return &projects[i]
		}
	}
	return nil
}

func fleetContainerWorktreeByName(worktrees []fleet.WorktreeSummary, name string) *fleet.WorktreeSummary {
	for i := range worktrees {
		if worktrees[i].Name == name {
			return &worktrees[i]
		}
	}
	return nil
}

func fleetContainerWorktreeByHostAndName(
	worktrees []fleet.WorktreeSummary,
	hostID string,
	name string,
) *fleet.WorktreeSummary {
	for i := range worktrees {
		if worktrees[i].HostID == hostID && worktrees[i].Name == name {
			return &worktrees[i]
		}
	}
	return nil
}

func fleetContainerSessionByWorktreeID(sessions []fleet.SessionSummary, worktreeID string) *fleet.SessionSummary {
	for i := range sessions {
		if sessions[i].WorktreeID != nil && *sessions[i].WorktreeID == worktreeID {
			return &sessions[i]
		}
	}
	return nil
}

func fleetContainerSessionByScopedKey(
	sessions []fleet.SessionSummary,
	scopedKey string,
) *fleet.SessionSummary {
	for i := range sessions {
		if sessions[i].ScopedKey == scopedKey {
			return &sessions[i]
		}
	}
	return nil
}

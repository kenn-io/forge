package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

type fakeFleetCommandRunner struct {
	tokenRequest  fleetEnrollmentTokenOptions
	joinRequest   fleetJoinOptions
	tokenResult   federation.EnrollmentToken
	joinResult    federation.LocalEnrollment
	prepareResult server.SpokePreparationReport
	abortResult   server.SpokePreparationAbortReport
	abortRequest  fleetAbortPreparationOptions
	revokeRequest fleetRevokeOptions
	err           error
}

func (r *fakeFleetCommandRunner) AbortPreparation(
	_ context.Context, options fleetAbortPreparationOptions,
) (server.SpokePreparationAbortReport, error) {
	r.abortRequest = options
	return r.abortResult, r.err
}

func (r *fakeFleetCommandRunner) Revoke(
	_ context.Context, options fleetRevokeOptions,
) error {
	r.revokeRequest = options
	return r.err
}

func (r *fakeFleetCommandRunner) PrepareSpoke(
	_ context.Context, options fleetPrepareOptions,
) (server.SpokePreparationReport, error) {
	return r.prepareResult, r.err
}

func (r *fakeFleetCommandRunner) CreateEnrollmentToken(
	_ context.Context, options fleetEnrollmentTokenOptions,
) (federation.EnrollmentToken, error) {
	r.tokenRequest = options
	return r.tokenResult, r.err
}

func (r *fakeFleetCommandRunner) Join(
	_ context.Context, options fleetJoinOptions,
) (federation.LocalEnrollment, error) {
	r.joinRequest = options
	return r.joinResult, r.err
}

func TestFleetAbortPreparationSupportsExplicitLocalRecovery(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	runner := &fakeFleetCommandRunner{abortResult: server.SpokePreparationAbortReport{
		EnrollmentID: "11111111111111111111111111111111", ProviderWritesOpen: true,
	}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{},
		Runner: runner,
	})
	cmd.SetArgs([]string{"abort-preparation", "--force", "--config", "/tmp/forge.toml"})

	require.NoError(cmd.Execute())
	assert.True(runner.abortRequest.Force)
	assert.Equal("/tmp/forge.toml", runner.abortRequest.ConfigPath)
	assert.Contains(stdout.String(), "standalone provider writes are open")
	assert.Contains(stdout.String(), "fleet revoke 11111111111111111111111111111111")
}

func TestFleetAbortPreparationReportsRequiredRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	runner := &fakeFleetCommandRunner{abortResult: server.SpokePreparationAbortReport{
		EnrollmentID: "11111111111111111111111111111111", HubRevoked: true,
		RestartRequired: true,
	}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{},
		Runner: runner,
	})
	cmd.SetArgs([]string{"abort-preparation"})

	require.NoError(cmd.Execute())
	assert.Contains(stdout.String(), "restart the daemon")
	assert.NotContains(stdout.String(), "provider writes are open")
}

func TestFleetRevokePassesEnrollmentID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	runner := &fakeFleetCommandRunner{}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{},
		Runner: runner,
	})
	cmd.SetArgs([]string{"revoke", "11111111111111111111111111111111"})

	require.NoError(cmd.Execute())
	assert.Equal("11111111111111111111111111111111", runner.revokeRequest.EnrollmentID)
	assert.Contains(stdout.String(), "revoked")
}

func TestFleetRevokeAcceptsNoContentResponse(t *testing.T) {
	require := require.New(t)
	var gotMethod, gotPath string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(daemon.Close)
	configPath := archiveCLITestConfig(t, daemon.URL, "", "local-secret")

	err := (daemonFleetCommandRunner{}).Revoke(t.Context(), fleetRevokeOptions{
		ConfigPath: configPath, Timeout: time.Second,
		EnrollmentID: "11111111111111111111111111111111",
	})

	require.NoError(err)
	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/api/v1/fleet/enrollments/11111111111111111111111111111111", gotPath)
}

func TestFleetRevokePassesTheDaemonMutationGuard(t *testing.T) {
	require := require.New(t)
	const (
		hubID        = "11111111111111111111111111111111"
		nodeID       = "22222222222222222222222222222222"
		enrollmentID = "33333333333333333333333333333333"
	)
	spoke := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/api/v1/fleet/enrollments/"+enrollmentID, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(spoke.Close)
	root := t.TempDir()
	serverConfigPath := filepath.Join(root, "server.toml")
	writeMinimalConfig(t, serverConfigPath, filepath.Join(root, "server-data"), reserveFreePort(t))
	serverConfig, err := config.Load(serverConfigPath)
	require.NoError(err)
	serverConfig.API.RequireAuth = true
	serverConfig.Fleet = config.Fleet{
		Enabled: true, Role: config.FleetRoleHub,
		BaseURL: "https://hub.example",
		Members: []config.FleetMember{{
			NodeID: nodeID, BaseURL: spoke.URL,
			State: federation.EnrollmentActive,
		}},
	}
	require.NoError(serverConfig.Save(serverConfigPath))
	enrollments, err := federation.Open(
		filepath.Join(root, "enrollments.json"), federation.StoreOptions{},
	)
	require.NoError(err)
	credentials, err := federationauth.Open(filepath.Join(root, "credentials.json"))
	require.NoError(err)
	token, err := enrollments.CreateOneTimeToken(federation.Identity{
		NodeID: hubID, BaseURL: "https://hub.example",
	}, time.Now().Add(time.Minute))
	require.NoError(err)
	_, err = enrollments.Begin(t.Context(), token.Token, federation.JoinRequest{
		EnrollmentID: enrollmentID, NodeID: nodeID, Platform: "linux",
		BaseURL: spoke.URL, ProtocolVersion: federation.ProtocolVersion,
		HubCredential: "hub-to-spoke",
	})
	require.NoError(err)
	require.NoError(credentials.StoreOutbound(
		nodeID, "hub-to-spoke", federationauth.HubToSpokeScopes(),
	))

	srv := server.NewWithConfig(
		dbtest.Open(t), nil, nil, nil, serverConfig, serverConfigPath,
		server.ServerOptions{
			DaemonAccess: server.DaemonAccessOptions{
				Token: "local-secret", RequireAPIAuth: true,
			},
			FederationSpokeID: hubID, FederationEnrollments: enrollments,
			FederationCredentials: credentials, HostCheckAllowLoopbackAnyPort: true,
			FederationHTTPClient:               spoke.Client(),
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(srv.Shutdown(ctx))
	})
	daemon := httptest.NewServer(srv)
	t.Cleanup(daemon.Close)
	clientConfigPath := archiveCLITestConfig(t, daemon.URL, "", "local-secret")

	err = (daemonFleetCommandRunner{}).Revoke(t.Context(), fleetRevokeOptions{
		ConfigPath: clientConfigPath, Timeout: time.Second, EnrollmentID: enrollmentID,
	})

	require.NoError(err)
	revoked, err := enrollments.Get(t.Context(), enrollmentID)
	require.NoError(err)
	require.Equal(federation.EnrollmentRevoked, revoked.State)
}

func TestFleetEnrollmentTokenPrintsSecretExactlyOnce(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const secret = "one-time-enrollment-secret"
	runner := &fakeFleetCommandRunner{tokenResult: federation.EnrollmentToken{
		Token: secret,
	}}
	var stdout, stderr bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		Runner: runner,
	})
	cmd.SetArgs([]string{
		"enrollment-token", "--base-url", "https://studio.example",
		"--name", "Studio", "--ttl", "10m", "--config", "/tmp/forge.toml",
	})

	require.NoError(cmd.Execute())
	assert.Equal(secret+"\n", stdout.String())
	assert.Empty(stderr.String())
	assert.Equal("https://studio.example", runner.tokenRequest.BaseURL)
	assert.Equal("Studio", runner.tokenRequest.Name)
	assert.Equal(10*time.Minute, runner.tokenRequest.TTL)
	assert.Equal("/tmp/forge.toml", runner.tokenRequest.ConfigPath)
}

func TestFleetJoinRejectsTokenArgumentWithoutEchoingSecret(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const secret = "must-not-appear"
	var stdout, stderr bytes.Buffer
	cmd := newRootCommand(cliOptions{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		FleetRunner: &fakeFleetCommandRunner{},
	})
	cmd.SetArgs([]string{
		"fleet", "join", "https://studio.example", "--base-url",
		"https://spoke.example", "--token", secret,
	})

	err := cmd.Execute()
	require.ErrorContains(err, "unknown flag: --token")
	assert.NotContains(err.Error(), secret)
	assert.NotContains(stdout.String(), secret)
	assert.NotContains(stderr.String(), secret)
}

func TestFleetJoinReadsEnrollmentTokenFromStdin(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const secret = "stdin-enrollment-secret"
	runner := &fakeFleetCommandRunner{joinResult: federation.LocalEnrollment{
		EnrollmentID: "11111111111111111111111111111111",
		State:        federation.EnrollmentPending,
	}}
	var stdout, stderr bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader(secret + "\n"), Stdout: &stdout, Stderr: &stderr,
		Runner: runner,
	})
	cmd.SetArgs([]string{
		"join", "https://studio.example", "--base-url", "https://spoke.example",
	})

	require.NoError(cmd.Execute())
	assert.Equal(secret, runner.joinRequest.Token)
	assert.NotContains(stdout.String(), secret)
	assert.NotContains(stderr.String(), secret)
	assert.Contains(stdout.String(), "preparation required")
}

func TestFleetJoinReadsEnrollmentTokenFromFile(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const secret = "file-enrollment-secret"
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(os.WriteFile(path, []byte(secret+"\n"), 0o600))
	runner := &fakeFleetCommandRunner{joinResult: federation.LocalEnrollment{
		EnrollmentID: "11111111111111111111111111111111",
		State:        federation.EnrollmentPending,
	}}
	var stdout, stderr bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader("ignored"), Stdout: &stdout, Stderr: &stderr,
		Runner: runner,
	})
	cmd.SetArgs([]string{
		"join", "https://studio.example", "--base-url", "https://spoke.example",
		"--token-file", path,
	})

	require.NoError(cmd.Execute())
	assert.Equal(secret, runner.joinRequest.Token)
	assert.NotContains(stdout.String(), secret)
	assert.NotContains(stderr.String(), secret)
}

func TestFleetJoinUsesInteractiveSecretPromptWithoutEchoing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const secret = "prompt-enrollment-secret"
	runner := &fakeFleetCommandRunner{joinResult: federation.LocalEnrollment{
		EnrollmentID: "11111111111111111111111111111111",
		State:        federation.EnrollmentPending,
	}}
	var stdout, stderr bytes.Buffer
	var prompt string
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		Runner: runner,
		ReadInteractiveSecret: func(got string) (string, error) {
			prompt = got
			return secret, nil
		},
		StdinIsTerminal: true,
	})
	cmd.SetArgs([]string{
		"join", "https://studio.example", "--base-url", "https://spoke.example",
	})

	require.NoError(cmd.Execute())
	assert.Equal("Enrollment token: ", prompt)
	assert.Equal(secret, runner.joinRequest.Token)
	assert.NotContains(stdout.String(), secret)
	assert.NotContains(stderr.String(), secret)
}

func TestFleetJoinFailureNeverAddsTokenToErrorOrOutput(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const secret = "failure-enrollment-secret"
	runner := &fakeFleetCommandRunner{err: errors.New("hub unavailable")}
	var stdout, stderr bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader(secret), Stdout: &stdout, Stderr: &stderr,
		Runner: runner,
	})
	cmd.SetArgs([]string{
		"join", "https://studio.example", "--base-url", "https://spoke.example",
	})

	err := cmd.Execute()
	require.Error(err)
	assert.NotContains(err.Error(), secret)
	assert.NotContains(stdout.String(), secret)
	assert.NotContains(stderr.String(), secret)
}

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/serviceauth"
	"go.kenn.io/forge/internal/testutil/gitsafe"
)

const serviceTestOwnerID = 12345

type serviceTestTransport func(*http.Request) (*http.Response, error)

func (f serviceTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGitHubCredentialAndExecUsePersistedServiceAuthorization(t *testing.T) {
	if os.Getenv("KENN_FORGE_GITHUB_CHILD") == "1" {
		fmt.Printf(
			"token=%s\nhost=%s\ngithub=%s\nforge=%s\nenterprise=%s\nargs=%q\n",
			os.Getenv("GH_TOKEN"), os.Getenv("GH_HOST"), os.Getenv("GITHUB_TOKEN"),
			os.Getenv("KENN_FORGE_GITHUB_TOKEN"), os.Getenv("GH_ENTERPRISE_TOKEN"), os.Args,
		)
		if marker := os.Getenv("KENN_FORGE_GITHUB_CHILD_MARKER"); marker != "" {
			_ = os.WriteFile(marker, []byte("ran"), 0o600)
		}
		if code, _ := strconv.Atoi(os.Getenv("KENN_FORGE_GITHUB_CHILD_EXIT")); code != 0 {
			os.Exit(code)
		}
		return
	}
	assert := assert.New(t)
	require := require.New(t)

	bin := buildForge(t)
	configOne, optsOne := writeServiceCLIConfig(t)
	authorizeServiceCLI(t, optsOne, "token-one")
	configTwo, optsTwo := writeServiceCLIConfig(t)
	authorizeServiceCLI(t, optsTwo, "token-two")

	runner := gitsafe.MutableRunner(t)
	for _, tc := range []struct {
		config string
		token  string
	}{{configOne, "token-one"}, {configTwo, "token-two"}} {
		helper := "!" + shellquote.Join(bin, "github", "credential", "--config", tc.config)
		stdout, stderr, err := runner.Run(
			t.Context(), "", strings.NewReader("protocol=https\nhost=github.com\npath=acme/widget.git\n\n"),
			"-c", "credential.helper=", "-c", "credential.helper="+helper,
			"-c", "credential.useHttpPath=true", "credential", "fill",
		)
		require.NoError(err, string(stderr))
		assert.Contains(string(stdout), "password="+tc.token+"\n")
	}

	var unrelated bytes.Buffer
	command := procutil.Command(bin, "github", "credential", "--config", configOne, "get")
	command.Stdin = strings.NewReader("protocol=https\nhost=example.com\n\n")
	command.Stdout = &unrelated
	require.NoError(command.Run())
	assert.Empty(unrelated.String())

	testBinary, err := os.Executable()
	require.NoError(err)
	childArgs := []string{
		"github", "exec", "--config", configOne, "--", testBinary,
		"-test.run=^TestGitHubCredentialAndExecUsePersistedServiceAuthorization$", "--", "two words", "$literal",
	}
	command = procutil.Command(bin, childArgs...)
	command.Env = append(os.Environ(),
		"KENN_FORGE_GITHUB_CHILD=1",
		"GH_TOKEN=poison-gh",
		"GITHUB_TOKEN=poison-github",
		"KENN_FORGE_GITHUB_TOKEN=poison-forge",
		"GH_ENTERPRISE_TOKEN=poison-enterprise",
		"GH_HOST=enterprise.example",
	)
	var output bytes.Buffer
	command.Stdout = &output
	require.NoError(command.Run())
	assert.Contains(output.String(), "token=token-one")
	assert.Contains(output.String(), "host=github.com")
	assert.Contains(output.String(), "github=\n")
	assert.Contains(output.String(), "forge=\n")
	assert.Contains(output.String(), "enterprise=\n")
	assert.Contains(output.String(), "two words")
	assert.Contains(output.String(), "$literal")

	command = procutil.Command(bin, "github", "credential", "--config", configOne, "erase")
	command.Stdin = strings.NewReader("protocol=https\nhost=github.com\npassword=token-one\n\n")
	require.NoError(command.Run())
	refreshManager := serviceCLIRefreshManager(t, optsOne, "token-refreshed")
	refreshed, err := refreshManager.Token(t.Context())
	require.NoError(err)
	assert.Equal("token-refreshed", refreshed)

	authorizeServiceCLI(t, optsOne, "token-rotated")
	command = procutil.Command(bin, childArgs...)
	command.Env = append(os.Environ(), "KENN_FORGE_GITHUB_CHILD=1")
	output.Reset()
	command.Stdout = &output
	require.NoError(command.Run())
	assert.Contains(output.String(), "token=token-rotated")

	missingConfig, _ := writeServiceCLIConfig(t)
	marker := filepath.Join(t.TempDir(), "child-ran")
	command = procutil.Command(bin,
		"github", "exec", "--config", missingConfig, "--", testBinary,
		"-test.run=^TestGitHubCredentialAndExecUsePersistedServiceAuthorization$",
	)
	command.Env = append(os.Environ(),
		"KENN_FORGE_GITHUB_CHILD=1",
		"KENN_FORGE_GITHUB_CHILD_MARKER="+marker,
	)
	err = command.Run()
	require.Error(err)
	assert.NoFileExists(marker)

	if runtime.GOOS != "windows" {
		askpassMarker := filepath.Join(t.TempDir(), "askpass-ran")
		askpass := filepath.Join(t.TempDir(), "askpass")
		require.NoError(os.WriteFile(
			askpass,
			[]byte("#!/bin/sh\ntouch \"$ASKPASS_MARKER\"\nprintf 'fallback-token\\n'\n"),
			0o700,
		))
		promptRunner := gitsafe.MutableRunner(t)
		promptRunner.Env = setTestEnvironment(promptRunner.Env, "GIT_TERMINAL_PROMPT", "1")
		promptRunner.Env = setTestEnvironment(promptRunner.Env, "GIT_ASKPASS", askpass)
		promptRunner.Env = setTestEnvironment(promptRunner.Env, "ASKPASS_MARKER", askpassMarker)
		helper := "!" + shellquote.Join(bin, "github", "credential", "--config", missingConfig)
		_, _, err = promptRunner.Run(
			t.Context(), "", strings.NewReader("protocol=https\nhost=github.com\n\n"),
			"-c", "credential.helper=", "-c", "credential.helper="+helper,
			"credential", "fill",
		)
		require.Error(err)
		assert.NoFileExists(askpassMarker, "quit=true must stop Git askpass fallback")
	}

	command = procutil.Command(bin, childArgs...)
	command.Env = append(os.Environ(),
		"KENN_FORGE_GITHUB_CHILD=1", "KENN_FORGE_GITHUB_CHILD_EXIT=23",
	)
	err = command.Run()
	require.Error(err)
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	require.True(ok)
	assert.Equal(23, exitErr.ExitCode())
}

func setTestEnvironment(base []string, name, value string) []string {
	prefix := name + "="
	out := make([]string, 0, len(base)+1)
	for _, entry := range base {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func TestGitHubCommandsRequireServiceMode(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	require.NoError(os.WriteFile(
		configPath, []byte(fmt.Sprintf("data_dir = %q\n", root)), 0o600,
	))

	var output bytes.Buffer
	err := runGitHubCredential(
		t.Context(), configPath, "get",
		strings.NewReader("protocol=https\nhost=github.com\n\n"), &output,
	)
	require.NoError(err)
	assert.Equal("quit=true\n\n", output.String())
	err = runGitHubExec(
		t.Context(), configPath, []string{"unused-child"},
		strings.NewReader(""), io.Discard, io.Discard,
	)
	require.EqualError(err, "GitHub service account mode is not enabled")
}

func serviceCLIRefreshManager(
	t *testing.T, opts serviceauth.Options, refreshedToken string,
) *serviceauth.Manager {
	t.Helper()
	opts.HTTPClient = &http.Client{Transport: serviceTestTransport(func(req *http.Request) (*http.Response, error) {
		body := fmt.Sprintf(
			`{"access_token":%q,"refresh_token":"refresh-rotated","expires_in":3600,"refresh_token_expires_in":86400,"token_type":"bearer"}`,
			refreshedToken,
		)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	manager, err := serviceauth.New(opts)
	require.NoError(t, err)
	return manager
}

func writeServiceCLIConfig(t *testing.T) (string, serviceauth.Options) {
	t.Helper()
	root := t.TempDir()
	secret := filepath.Join(root, "client-secret")
	require.NoError(t, os.WriteFile(secret, []byte("fixture-secret\n"), 0o600))
	configPath := filepath.Join(root, "config.toml")
	contents := fmt.Sprintf(`data_dir = %q

[service]
enabled = true
github_user_id = %d
github_client_id = "fixture-client"
github_client_secret_file = %q
base_url = "https://forge.example"
`, root, serviceTestOwnerID, secret)
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0o600))
	return configPath, serviceauth.Options{
		ClientID: "fixture-client", ClientSecretFile: secret,
		BaseURL: "https://forge.example", DataDir: root, OwnerID: serviceTestOwnerID,
	}
}

func authorizeServiceCLI(t *testing.T, opts serviceauth.Options, token string) {
	t.Helper()
	opts.HTTPClient = &http.Client{Transport: serviceTestTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Host + req.URL.Path {
		case "github.com/login/oauth/access_token":
			body = fmt.Sprintf(
				`{"access_token":%q,"refresh_token":"refresh-fixture","expires_in":3600,"refresh_token_expires_in":86400,"token_type":"bearer"}`,
				token,
			)
		case "api.github.com/user":
			body = fmt.Sprintf(`{"login":"owner","id":%d}`, serviceTestOwnerID)
		default:
			return nil, fmt.Errorf("unexpected fixture request %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	manager, err := serviceauth.New(opts)
	require.NoError(t, err)
	start := httptest.NewRecorder()
	manager.StartLogin(start, httptest.NewRequest(http.MethodGet, opts.BaseURL+"/auth/github", nil))
	location, err := url.Parse(start.Header().Get("Location"))
	require.NoError(t, err)
	cookies := start.Result().Cookies()
	require.NotEmpty(t, cookies)
	callback := httptest.NewRequest(http.MethodGet,
		opts.BaseURL+"/auth/github/callback?code=fixture-code&state="+url.QueryEscape(location.Query().Get("state")), nil,
	)
	callback.AddCookie(cookies[0])
	complete := httptest.NewRecorder()
	manager.CompleteLogin(complete, callback)
	require.Equal(t, http.StatusSeeOther, complete.Code, complete.Body.String())
}

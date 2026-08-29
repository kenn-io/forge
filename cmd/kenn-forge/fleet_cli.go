package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/fleetsetup"
	"go.kenn.io/forge/internal/server"
	"golang.org/x/term"
)

const (
	maxFleetEnrollmentTokenBytes = 4096
	maxFleetResponseBytes        = 1 << 20
)

type fleetEnrollmentTokenOptions struct {
	ConfigPath string
	BaseURL    string
	Name       string
	TTL        time.Duration
	Timeout    time.Duration
}

type fleetJoinOptions struct {
	ConfigPath   string
	HubURL       string
	SpokeBaseURL string
	Name         string
	Token        string
	Timeout      time.Duration
}

type fleetPrepareOptions struct {
	ConfigPath string
	Timeout    time.Duration
}

type fleetAbortPreparationOptions struct {
	ConfigPath string
	Timeout    time.Duration
	Force      bool
}

type fleetRevokeOptions struct {
	ConfigPath   string
	Timeout      time.Duration
	EnrollmentID string
}

type fleetCommandRunner interface {
	CreateEnrollmentToken(
		context.Context, fleetEnrollmentTokenOptions,
	) (federation.EnrollmentToken, error)
	Join(context.Context, fleetJoinOptions) (federation.LocalEnrollment, error)
	PrepareSpoke(context.Context, fleetPrepareOptions) (server.SpokePreparationReport, error)
	AbortPreparation(context.Context, fleetAbortPreparationOptions) (server.SpokePreparationAbortReport, error)
	Revoke(context.Context, fleetRevokeOptions) error
}

type fleetCLIOptions struct {
	Stdin                 io.Reader
	Stdout                io.Writer
	Stderr                io.Writer
	Runner                fleetCommandRunner
	SetupRunner           fleetSetupCommandRunner
	ReadInteractiveSecret func(string) (string, error)
	StdinIsTerminal       bool
}

func newFleetCommand(options fleetCLIOptions) *cobra.Command {
	if options.Stdin == nil {
		options.Stdin = os.Stdin
	}
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	if options.Runner == nil {
		options.Runner = daemonFleetCommandRunner{}
	}
	if options.SetupRunner == nil {
		options.SetupRunner = fleetsetup.NewRunner()
	}
	if options.ReadInteractiveSecret == nil {
		options.ReadInteractiveSecret = interactiveSecretReader(options.Stdin, options.Stderr)
	}
	cmd := &cobra.Command{
		Use: "fleet", Short: "Enroll and manage federated Forge daemons",
		Args: cobra.NoArgs, SilenceErrors: true, SilenceUsage: true,
	}
	cmd.AddCommand(
		newFleetSetupCommand(options),
		newFleetEnrollmentTokenCommand(options),
		newFleetJoinCommand(options),
		newFleetPrepareSpokeCommand(options),
		newFleetAbortPreparationCommand(options),
		newFleetRevokeCommand(options),
	)
	return cmd
}

func newFleetEnrollmentTokenCommand(options fleetCLIOptions) *cobra.Command {
	flags := fleetEnrollmentTokenOptions{}
	cmd := &cobra.Command{
		Use: "enrollment-token", Short: "Create a one-time spoke enrollment token",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(flags.BaseURL) == "" {
				return errors.New("--base-url is required")
			}
			result, err := options.Runner.CreateEnrollmentToken(cmd.Context(), flags)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(options.Stdout, result.Token)
			return err
		},
	}
	cmd.Flags().StringVar(&flags.ConfigPath, "config", config.DefaultConfigPath(), "path to config file")
	cmd.Flags().StringVar(&flags.BaseURL, "base-url", "", "public HTTPS hub origin")
	cmd.Flags().StringVar(&flags.Name, "name", "", "hub display name")
	cmd.Flags().DurationVar(&flags.TTL, "ttl", 10*time.Minute, "one-time token lifetime")
	cmd.Flags().DurationVar(&flags.Timeout, "timeout", 30*time.Second, "local daemon request timeout")
	return cmd
}

func newFleetJoinCommand(options fleetCLIOptions) *cobra.Command {
	flags := fleetJoinOptions{}
	var tokenFile string
	cmd := &cobra.Command{
		Use: "join HUB_URL", Short: "Enroll this daemon with a hub",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(flags.SpokeBaseURL) == "" {
				return errors.New("--base-url is required")
			}
			token, err := readFleetEnrollmentToken(options, tokenFile)
			if err != nil {
				return err
			}
			flags.HubURL = args[0]
			flags.Token = token
			result, err := options.Runner.Join(cmd.Context(), flags)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(
				options.Stdout,
				"Enrollment %s is %s; preparation required before switching roles.\n",
				result.EnrollmentID, result.State,
			)
			return err
		},
	}
	cmd.Flags().StringVar(&flags.ConfigPath, "config", config.DefaultConfigPath(), "path to config file")
	cmd.Flags().StringVar(&flags.SpokeBaseURL, "base-url", "", "public HTTPS origin for this daemon")
	cmd.Flags().StringVar(&flags.Name, "name", "", "spoke display name")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "read the one-time enrollment token from a file")
	cmd.Flags().DurationVar(&flags.Timeout, "timeout", 60*time.Second, "enrollment request timeout")
	return cmd
}

func readFleetEnrollmentToken(options fleetCLIOptions, tokenFile string) (string, error) {
	var raw []byte
	var err error
	switch {
	case tokenFile != "":
		raw, err = os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("read enrollment token file: %w", err)
		}
	case options.StdinIsTerminal:
		secret, readErr := options.ReadInteractiveSecret("Enrollment token: ")
		if readErr != nil {
			return "", fmt.Errorf("read enrollment token: %w", readErr)
		}
		raw = []byte(secret)
	default:
		raw, err = io.ReadAll(io.LimitReader(
			options.Stdin, maxFleetEnrollmentTokenBytes+1,
		))
		if err != nil {
			return "", fmt.Errorf("read enrollment token from stdin: %w", err)
		}
	}
	if len(raw) > maxFleetEnrollmentTokenBytes {
		return "", errors.New("enrollment token input is too large")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("enrollment token must be one non-empty value")
	}
	return token, nil
}

func interactiveSecretReader(stdin io.Reader, stderr io.Writer) func(string) (string, error) {
	return func(prompt string) (string, error) {
		file, ok := stdin.(*os.File)
		if !ok {
			return "", errors.New("interactive token input requires a terminal")
		}
		if _, err := fmt.Fprint(stderr, prompt); err != nil {
			return "", err
		}
		raw, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(stderr)
		return string(raw), err
	}
}

func stdinIsTerminal(stdin io.Reader) bool {
	file, ok := stdin.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

type daemonFleetCommandRunner struct{}

func (daemonFleetCommandRunner) CreateEnrollmentToken(
	ctx context.Context, options fleetEnrollmentTokenOptions,
) (federation.EnrollmentToken, error) {
	var result federation.EnrollmentToken
	err := postLocalFleetJSON(ctx, options.ConfigPath, options.Timeout,
		"/api/v1/fleet/enrollment-tokens", map[string]any{
			"base_url": options.BaseURL, "name": options.Name,
			"expires_in_seconds": int(options.TTL / time.Second),
		}, &result)
	return result, err
}

func (daemonFleetCommandRunner) Join(
	ctx context.Context, options fleetJoinOptions,
) (federation.LocalEnrollment, error) {
	var result federation.LocalEnrollment
	err := postLocalFleetJSON(ctx, options.ConfigPath, options.Timeout,
		"/api/v1/fleet/join", map[string]any{
			"hub_base_url":   options.HubURL,
			"spoke_base_url": options.SpokeBaseURL, "name": options.Name,
			"enrollment_token": options.Token,
		}, &result)
	return result, err
}

func (daemonFleetCommandRunner) PrepareSpoke(
	ctx context.Context, options fleetPrepareOptions,
) (server.SpokePreparationReport, error) {
	var result server.SpokePreparationReport
	err := postLocalFleetJSON(
		ctx, options.ConfigPath, options.Timeout,
		"/api/v1/fleet/prepare-spoke", struct{}{}, &result,
	)
	return result, err
}

func (daemonFleetCommandRunner) AbortPreparation(
	ctx context.Context, options fleetAbortPreparationOptions,
) (server.SpokePreparationAbortReport, error) {
	var result server.SpokePreparationAbortReport
	err := localFleetJSON(
		ctx, options.ConfigPath, options.Timeout, http.MethodPost,
		"/api/v1/fleet/prepare-spoke/abort", map[string]any{"force": options.Force},
		&result,
	)
	return result, err
}

func (daemonFleetCommandRunner) Revoke(
	ctx context.Context, options fleetRevokeOptions,
) error {
	return localFleetJSON(
		ctx, options.ConfigPath, options.Timeout, http.MethodDelete,
		"/api/v1/fleet/enrollments/"+options.EnrollmentID, nil, nil,
	)
}

func postLocalFleetJSON(
	ctx context.Context, configPath string, timeout time.Duration,
	path string, body, result any,
) error {
	return localFleetJSON(
		ctx, configPath, timeout, http.MethodPost, path, body, result,
	)
}

func localFleetJSON(
	ctx context.Context, configPath string, timeout time.Duration,
	method, path string, body, result any,
) error {
	daemon, err := discoverDaemonHTTP(configPath, timeout)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		raw, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return fmt.Errorf("encode fleet request: %w", marshalErr)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(
		ctx, method, daemon.BaseURL+path, reader,
	)
	if err != nil {
		return fmt.Errorf("build fleet request: %w", err)
	}
	if body != nil || (method != http.MethodGet && method != http.MethodHead) {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := daemon.Client.Do(request)
	if err != nil {
		return fmt.Errorf("fleet request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("fleet request returned %s", response.Status)
	}
	if result == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(
		response.Body, maxFleetResponseBytes,
	)).Decode(result); err != nil {
		return fmt.Errorf("decode fleet response: %w", err)
	}
	return nil
}

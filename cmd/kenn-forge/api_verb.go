package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
)

// The api verb is the thin-HTTP-client primitive: it discovers the
// running daemon through the runtime metadata under data_dir,
// authenticates with the minted token, and relays one request. Fleet
// transports execute it on remote hosts (over SSH) so the remote
// listener never has to be exposed; scripts and supervisors use it
// locally instead of hand-rolling discovery + auth.
//
// Contract (pinned by tests):
//   - response body bytes go to stdout verbatim, success or failure
//     (an API error body is an RFC 9457 problem document the caller
//     wants);
//   - exit 0 on 2xx, exit 1 on any other HTTP status, exit 2 when no
//     request was made (daemon not running, transport failure, bad
//     usage) with the reason on stderr.
const (
	apiVerbExitHTTPError = 1
	apiVerbExitNoRequest = 2
)

// apiVerbError carries the exit code for failures that never reached
// a response.
type apiVerbError struct {
	code int
	err  error
}

func (e *apiVerbError) Error() string { return e.err.Error() }

type apiVerbOptions struct {
	configPath    string
	data          string
	timeout       time.Duration
	includeStatus bool
}

func newAPIVerbCommand(
	stdin io.Reader,
	stdout io.Writer,
	controlRequest func(context.Context, string, string, url.Values, []string) error,
) *cobra.Command {
	opts := apiVerbOptions{}
	cmd := &cobra.Command{
		Use:   "api METHOD PATH [body...]",
		Short: "Relay one request to the running daemon",
		Long: "Relay one request to the running daemon. Use `kenn-forge api list` " +
			"to discover available operations.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return &apiVerbError{apiVerbExitNoRequest, fmt.Errorf(
					"usage: kenn-forge api [flags] METHOD PATH",
				)}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if useControlAPIMode(cmd, args) {
				if err := rejectRelayFlagsInControlMode(cmd); err != nil {
					return err
				}
				return controlRequest(cmd.Context(), args[0], args[1], nil, args[2:])
			}
			return runAPIVerb(args[0], args[1], opts, stdout, stdin)
		},
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &apiVerbError{apiVerbExitNoRequest, err}
	})
	cmd.Flags().StringVar(&opts.configPath, "config", config.DefaultConfigPath(), "path to config file")
	cmd.Flags().StringVarP(&opts.data, "data", "d", "", "request body; use @- to read the body from stdin")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 60*time.Second, "request timeout")
	cmd.Flags().BoolVarP(
		&opts.includeStatus,
		"include",
		"i",
		false,
		"prefix output with an HTTP status line and a blank line",
	)
	return cmd
}

func rejectRelayFlagsInControlMode(cmd *cobra.Command) error {
	for _, name := range []string{"config", "data", "include", "timeout"} {
		if cmd.Flags().Changed(name) {
			return &apiVerbError{
				code: apiVerbExitNoRequest,
				err:  fmt.Errorf("--%s cannot be used with API control mode", name),
			}
		}
	}
	return nil
}

func useControlAPIMode(cmd *cobra.Command, args []string) bool {
	if len(args) > 2 {
		return true
	}
	flags := cmd.Root().PersistentFlags()
	return flags.Changed("server") || flags.Changed("output") || flags.Changed("timeout")
}

func runAPIVerb(method, requestPath string, opts apiVerbOptions, stdout io.Writer, stdin io.Reader) error {
	method = strings.ToUpper(method)
	path := requestPath
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	daemon, err := discoverDaemonHTTP(opts.configPath, opts.timeout)
	if err != nil {
		return &apiVerbError{apiVerbExitNoRequest, err}
	}

	var body io.Reader
	switch {
	case opts.data == "@-":
		body = stdin
	case opts.data != "":
		body = strings.NewReader(opts.data)
	}

	url := daemon.BaseURL + path
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return &apiVerbError{apiVerbExitNoRequest,
			fmt.Errorf("build request: %w", err)}
	}
	// The server's CSRF guard requires application/json on every
	// mutation, including zero-body endpoints (e.g. POST /sync), so
	// the content type is keyed off the method, not body presence.
	if body != nil || (method != http.MethodGet && method != http.MethodHead) {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := daemon.Client.Do(req)
	if err != nil {
		return &apiVerbError{apiVerbExitNoRequest,
			fmt.Errorf("request failed: %w", err)}
	}
	defer resp.Body.Close()
	if opts.includeStatus {
		if _, err := fmt.Fprintf(
			stdout, "%s %s\r\n\r\n", resp.Proto, resp.Status,
		); err != nil {
			return &apiVerbError{apiVerbExitNoRequest,
				fmt.Errorf("write status line: %w", err)}
		}
	}
	if _, err := io.Copy(stdout, resp.Body); err != nil {
		return &apiVerbError{apiVerbExitNoRequest,
			fmt.Errorf("read response: %w", err)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiVerbError{apiVerbExitHTTPError, fmt.Errorf(
			"%s %s returned %s", method, path, resp.Status,
		)}
	}
	return nil
}

// exitCodeForAPIVerb maps a runAPICLI error to its process exit code.
func exitCodeForAPIVerb(err error) int {
	if verr, ok := errors.AsType[*apiVerbError](err); ok {
		return verr.code
	}
	return apiVerbExitNoRequest
}

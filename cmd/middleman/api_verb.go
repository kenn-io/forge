package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/config"
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

func runAPICLI(args []string, stdout io.Writer, stdin io.Reader) error {
	fs := flag.NewFlagSet("middleman api", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String(
		"config", config.DefaultConfigPath(),
		"path to config file",
	)
	data := fs.String(
		"d", "",
		"request body; use @- to read the body from stdin",
	)
	timeout := fs.Duration(
		"timeout", 60*time.Second,
		"request timeout",
	)
	includeStatus := fs.Bool(
		"i", false,
		"prefix the output with an HTTP status line and a blank line,"+
			" so relays can recover the exact status code",
	)
	if err := fs.Parse(args); err != nil {
		return &apiVerbError{apiVerbExitNoRequest, err}
	}
	if fs.NArg() != 2 {
		return &apiVerbError{apiVerbExitNoRequest, fmt.Errorf(
			"usage: middleman api [flags] METHOD PATH",
		)}
	}
	method := strings.ToUpper(fs.Arg(0))
	path := fs.Arg(1)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	daemon, err := discoverDaemonHTTP(*configPath, *timeout)
	if err != nil {
		return &apiVerbError{apiVerbExitNoRequest, err}
	}

	var body io.Reader
	switch {
	case *data == "@-":
		body = stdin
	case *data != "":
		body = strings.NewReader(*data)
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
	if *includeStatus {
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
	var verr *apiVerbError
	if errors.As(err, &verr) {
		return verr.code
	}
	return apiVerbExitNoRequest
}

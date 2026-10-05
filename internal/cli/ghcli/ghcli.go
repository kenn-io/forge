// Package ghcli runs kenn-forge as a stand-in for the GitHub CLI: a `gh`
// symlink to kenn-forge, or `kenn-forge gh`, serves supported queries from
// Forge and passes everything else to the real gh.
package ghcli

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonclient"
	"go.kenn.io/forge/internal/ghshim"
	"go.kenn.io/forge/internal/procutil"
	"golang.org/x/term"
)

// CommandName is the subcommand name and the executable name that routes to it.
const CommandName = "gh"

// ExitError carries the exit status of a served or passed-through gh call.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("gh exited with status %d", e.Code) }

// NewCommand returns the `gh` subcommand. gh owns every argument, so Cobra
// must not parse or normalize them.
func NewCommand() *cobra.Command {
	return &cobra.Command{
		Use:                "gh [gh arguments]",
		Short:              "Answer supported gh pull request queries from Forge and pass the rest to gh",
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if code := Run(args); code != 0 {
				return &ExitError{Code: code}
			}
			return nil
		},
	}
}

// Run serves one gh invocation and returns its exit status. Pass-through
// replaces the process on Unix.
func Run(args []string) int {
	// Legitimate nesting, such as a gh extension calling gh, stays shallow.
	// A deep chain means gh keeps resolving back to this shim.
	depth, _ := strconv.Atoi(os.Getenv(depthEnv))
	if depth >= maxDepth {
		fmt.Fprintln(os.Stderr, "kenn-forge gh: gh keeps calling back into kenn-forge gh; set FORGE_GH_REAL to the real gh executable")
		return 1
	}
	skip := filepath.SplitList(os.Getenv(skipEnv))
	// A gh wrapper script that runs `kenn-forge gh` sends the call it was
	// handed straight back here. Skip that wrapper from now on, including in
	// nested gh calls, and do not query or log the same call twice.
	var handoff struct {
		Path string   `json:"path"`
		Argv []string `json:"argv"`
	}
	bounced := json.Unmarshal([]byte(os.Getenv(handoffEnv)), &handoff) == nil && slices.Equal(handoff.Argv, args)
	if bounced {
		skip = append(skip, handoff.Path)
	}
	realPath, err := realGH(skip)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !bounced {
		q, repo, supported := ghshim.Parse(args)
		reason := "unsupported"
		if supported && !term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("GH_FORCE_TTY") == "" && os.Getenv("CLICOLOR_FORCE") == "" {
			if resolveRepo(&q, repo) {
				output, handled, why := queryDaemon(q)
				reason = why
				if handled {
					logCall(args, reason)
					if _, err := os.Stdout.WriteString(output); err != nil {
						return 1
					}
					return 0
				}
			} else {
				reason = "repository_unresolved"
			}
		}
		logCall(args, reason)
	}
	handoff.Path, handoff.Argv = realPath, args
	encoded, err := json.Marshal(handoff)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := errors.Join(
		os.Setenv(skipEnv, strings.Join(skip, string(os.PathListSeparator))),
		os.Setenv(handoffEnv, string(encoded)),
		os.Setenv(depthEnv, strconv.Itoa(depth+1)),
	); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return passthrough(realPath, args)
}

// Environment passed to the real gh. The handoff names the executable and
// arguments of the call being passed on; the skip list holds executables that
// turned out to be wrappers around this shim; the depth counts shim hops.
const (
	handoffEnv = "KENN_FORGE_GH_HANDOFF"
	skipEnv    = "KENN_FORGE_GH_SKIP"
	depthEnv   = "KENN_FORGE_GH_DEPTH"
	maxDepth   = 16
)

func realGH(skip []string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	info, err := os.Stat(self)
	if err != nil {
		return "", err
	}
	skipped := []os.FileInfo{info}
	for _, path := range skip {
		if other, err := os.Stat(path); err == nil {
			skipped = append(skipped, other)
		}
	}
	candidates := []string{}
	if explicit := os.Getenv("FORGE_GH_REAL"); explicit != "" {
		candidates = append(candidates, explicit)
	} else {
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			candidates = append(candidates, filepath.Join(dir, ghExecutable))
		}
	}
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate); err != nil {
			continue
		}
		other, err := os.Stat(candidate)
		if err != nil || other.IsDir() || slices.ContainsFunc(skipped, func(info os.FileInfo) bool { return os.SameFile(info, other) }) {
			continue
		}
		if abs, err := filepath.Abs(candidate); err == nil {
			return abs, nil
		}
	}
	return "", fmt.Errorf("kenn-forge gh: real gh not found; set FORGE_GH_REAL to its executable path")
}

func resolveRepo(q *ghshim.Query, repo string) bool {
	fromRemote := false
	if repo == "" {
		repo = os.Getenv("GH_REPO")
	}
	if repo == "" {
		fromRemote = true
		// Multiple remotes, branch inference, and gh-resolved defaults belong to gh.
		// A single remote is unambiguous and can be resolved without provider I/O.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		names, err := procutil.CommandContext(ctx, "git", "remote").Output()
		if err != nil || len(strings.Fields(string(names))) != 1 {
			return false
		}
		remote := strings.TrimSpace(string(names))
		raw, err := procutil.CommandContext(ctx, "git", "remote", "get-url", remote).Output()
		if err != nil {
			return false
		}
		repo = strings.TrimSpace(string(raw))
		resolved, _ := procutil.CommandContext(ctx, "git", "config", "--get", "remote."+remote+".gh-resolved").Output()
		if value := strings.TrimSpace(string(resolved)); value != "" && value != "base" {
			return false
		}
	}
	host := defaultGHHost()
	if strings.Contains(repo, "://") {
		u, err := url.Parse(repo)
		if err != nil || u.Host == "" {
			return false
		}
		host = u.Host
		repo = strings.TrimPrefix(u.Path, "/")
	} else if sshRepo, found := strings.CutPrefix(repo, "git@"); found {
		authority, path, ok := strings.Cut(sshRepo, ":")
		if !ok {
			return false
		}
		host = authority
		repo = path
	}
	parts := strings.Split(strings.TrimSuffix(repo, ".git"), "/")
	if len(parts) == 3 {
		host = parts[0]
		parts = parts[1:]
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	if fromRemote && os.Getenv("GH_HOST") != "" && !strings.EqualFold(os.Getenv("GH_HOST"), host) {
		return false
	}
	q.Host = strings.ToLower(host)
	q.Owner = parts[0]
	q.Repo = parts[1]
	return true
}

func queryDaemon(q ghshim.Query) (string, bool, string) {
	configPath := os.Getenv("FORGE_GH_CONFIG")
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}
	daemon, err := daemonclient.Discover(configPath, 10*time.Second)
	if err != nil {
		return "", false, "daemon_unavailable"
	}
	httpClient := *daemon.Client
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client, err := apiclient.NewWithHTTPClient(daemon.BaseURL, &httpClient)
	if err != nil {
		return "", false, "daemon_unavailable"
	}
	body := generated.QueryGhBody{
		Command: q.Command, Host: q.Host, Owner: q.Owner, Repo: q.Repo,
		Number: int64(q.Number), State: q.State, Head: q.Head, Base: q.Base,
		Limit: int64(q.Limit), Fields: q.Fields,
	}
	resp, err := client.HTTP.QueryGhWithResponse(context.Background(), &generated.QueryGhRequestOptions{Body: &body})
	if err != nil || resp.JSON200 == nil {
		return "", false, "daemon_unavailable"
	}
	return resp.JSON200.Output, resp.JSON200.Handled, resp.JSON200.Reason
}

// Log each call at debug level so normal gh output stays clean. The argv is
// a string so the log handler can redact known secret shapes in it.
func logCall(args []string, reason string) {
	slog.Debug("gh shim call", "reason", reason, "argv", fmt.Sprintf("%q", args))
}

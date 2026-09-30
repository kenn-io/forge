// forge-gh can be installed as gh ahead of the real GitHub CLI on PATH.
package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonclient"
	"go.kenn.io/forge/internal/ghshim"
	"go.kenn.io/forge/internal/procutil"
	"golang.org/x/term"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	realPath, err := realGH()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	q, repo, supported := ghshim.Parse(args)
	reason := "unsupported"
	if supported && !term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("GH_FORCE_TTY") == "" && os.Getenv("CLICOLOR_FORCE") == "" {
		if resolveRepo(&q, repo) {
			output, handled, why := queryDaemon(q)
			reason = why
			if handled {
				recordUsage(args, reason)
				if _, err := os.Stdout.WriteString(output); err != nil {
					return 1
				}
				return 0
			}
		} else {
			reason = "repository_unresolved"
		}
	}
	recordUsage(args, reason)
	return passthrough(realPath, args)
}

func realGH() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	info, err := os.Stat(self)
	if err != nil {
		return "", err
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
		if err == nil && !other.IsDir() && !os.SameFile(info, other) {
			abs, err := filepath.Abs(candidate)
			if err == nil {
				return abs, nil
			}
		}
	}
	return "", fmt.Errorf("forge-gh: real gh not found; set FORGE_GH_REAL to its executable path")
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

// Record the full invocation and outcome so coverage gaps can be reproduced.
func recordUsage(args []string, reason string) {
	path := filepath.Join(filepath.Dir(config.DefaultConfigPath()), "forge-gh-usage.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	data, err := json.Marshal(struct {
		Time    string   `json:"time"`
		Command string   `json:"command"`
		Reason  string   `json:"reason"`
		Argv    []string `json:"argv"`
	}{time.Now().UTC().Format(time.RFC3339), commandName(args), reason, args})
	if err == nil {
		_, _ = file.Write(append(data, '\n'))
	}
}

func commandName(args []string) string {
	if len(args) < 2 {
		return "other"
	}
	switch args[0] {
	case "pr", "issue", "repo", "run", "workflow", "auth", "api", "release", "search", "label", "project":
		switch args[1] {
		case "list", "ls", "view", "checks", "status", "token", "create", "edit", "close", "merge", "diff", "checkout", "comment", "review", "download", "watch", "delete":
			return args[0] + " " + args[1]
		}
		return args[0] + " other"
	}
	return "other"
}

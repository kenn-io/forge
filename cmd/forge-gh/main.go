// forge-gh can be installed as gh ahead of the real GitHub CLI on PATH.
package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/ghshim"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/runtimelock"
	"golang.org/x/term"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	real, err := realGH()
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
	return passthrough(real, args)
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
	cfg, err := config.Load(os.Getenv("FORGE_GH_CONFIG"))
	if err != nil {
		return "", false, "daemon_unavailable"
	}
	status, err := runtimelock.Read(cfg.DataDir)
	if err != nil || !status.Running || status.Metadata == nil {
		return "", false, "daemon_unavailable"
	}
	token, err := runtimelock.ReadAuthToken(cfg.DataDir)
	if err != nil {
		return "", false, "daemon_unavailable"
	}
	origin := "http://" + status.Metadata.ListenAddr
	endpoint := origin + strings.TrimSuffix(status.Metadata.BasePath, "/") + "/api/v1/gh/query"
	body, err := json.Marshal(q)
	if err != nil {
		return "", false, "unsupported"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", false, "daemon_unavailable"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", false, "daemon_unavailable"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, "daemon_unavailable"
	}
	var result struct {
		Handled bool   `json:"handled"`
		Output  string `json:"output"`
		Reason  string `json:"reason"`
	}
	if json.UnmarshalRead(io.LimitReader(resp.Body, 16<<20), &result) != nil {
		return "", false, "daemon_unavailable"
	}
	return result.Output, result.Handled, result.Reason
}

// Record the full invocation and outcome so coverage gaps can be reproduced.
func recordUsage(args []string, reason string) {
	path := filepath.Join(filepath.Dir(config.DefaultConfigPath()), "forge-gh-usage.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
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

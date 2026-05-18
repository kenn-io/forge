package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rest-sh/restish/cli"
	"github.com/rest-sh/restish/openapi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
)

const (
	defaultServer = "http://127.0.0.1:8091"
	apiPrefix     = "/api/v1"
)

type restishRequester func(context.Context, cliConfig, string, string, []string) ([]byte, error)

type commandDeps struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Restish restishRequester
}

type cliConfig struct {
	server  string
	output  string
	timeout time.Duration
}

func main() {
	cmd := newRootCommand(commandDeps{})
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand(deps commandDeps) *cobra.Command {
	if deps.Stdout == nil {
		deps.Stdout = os.Stdout
	}
	if deps.Stderr == nil {
		deps.Stderr = os.Stderr
	}
	if deps.Restish == nil {
		deps.Restish = makeRestishRequest
	}

	cfg := viper.New()
	cfg.SetEnvPrefix("MIDDLEMANCTL")
	cfg.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	cfg.AutomaticEnv()
	cfg.SetDefault("server", defaultServer)
	cfg.SetDefault("output", "json")
	cfg.SetDefault("timeout", 30*time.Second)

	root := &cobra.Command{
		Use:   "middlemanctl",
		Short: "Agent-oriented CLI for the middleman API",
		Long: strings.TrimSpace(`middlemanctl serves middleman API content for agents.

Start with "middlemanctl quickstart" for the API shape, then use typed shortcuts
like "middlemanctl pulls" or the Restish-backed escape hatch:

  middlemanctl api METHOD PATH [body...]

Responses are formatted as JSON by default, YAML with --output yaml, and
newline-delimited JSON with --output jsonl.`),
		SilenceUsage: true,
	}
	root.SetOut(deps.Stdout)
	root.SetErr(deps.Stderr)
	root.PersistentFlags().String("server", defaultServer, "middleman server URL")
	root.PersistentFlags().StringP("output", "o", "json", "response format: json, yaml, or jsonl")
	root.PersistentFlags().Duration("timeout", 30*time.Second, "HTTP request timeout")
	mustBind(cfg, root.PersistentFlags().Lookup("server"), "server")
	mustBind(cfg, root.PersistentFlags().Lookup("output"), "output")
	mustBind(cfg, root.PersistentFlags().Lookup("timeout"), "timeout")

	request := func(ctx context.Context, method, path string, query url.Values, bodyArgs []string) error {
		current, err := readConfig(cfg)
		if err != nil {
			return err
		}
		body, err := deps.Restish(ctx, current, method, apiURL(current.server, path, query), bodyArgs)
		if err != nil {
			return err
		}
		if len(body) > 0 {
			if err := writeResponse(deps.Stdout, current.output, body); err != nil {
				return err
			}
		}
		return nil
	}

	root.AddCommand(newQuickstartCommand(cfg, deps.Stdout))
	root.AddCommand(newAPICommand(request))
	root.AddCommand(newSimpleGetCommand("version", "Show server version", "/version", nil, request))
	root.AddCommand(newSimpleGetCommand("repos", "List configured repositories", "/repos", nil, request))
	root.AddCommand(newSimpleGetCommand("repo-summaries", "List repository summaries", "/repos/summary", nil, request))
	root.AddCommand(newPullsCommand(request))
	root.AddCommand(newIssuesCommand(request))
	root.AddCommand(newSyncCommand(request))
	root.AddCommand(newSimpleGetCommand("stacks", "List detected pull request stacks", "/stacks", nil, request))
	root.AddCommand(newWorkspacesCommand(request))
	root.AddCommand(newSimpleGetCommand("rate-limits", "Show provider rate limit status", "/rate-limits", nil, request))
	root.AddCommand(newActivityCommand(request))

	return root
}

func mustBind(cfg *viper.Viper, flag *pflag.Flag, key string) {
	if flag == nil {
		panic("missing flag " + key)
	}
	if err := cfg.BindPFlag(key, flag); err != nil {
		panic(err)
	}
}

func readConfig(cfg *viper.Viper) (cliConfig, error) {
	out := strings.ToLower(strings.TrimSpace(cfg.GetString("output")))
	if out != "json" && out != "yaml" && out != "jsonl" {
		return cliConfig{}, fmt.Errorf("unsupported output format %q", out)
	}
	server := strings.TrimRight(strings.TrimSpace(cfg.GetString("server")), "/")
	if server == "" {
		return cliConfig{}, errors.New("server is required")
	}
	return cliConfig{
		server:  server,
		output:  out,
		timeout: cfg.GetDuration("timeout"),
	}, nil
}

func apiURL(server, path string, query url.Values) string {
	if parsed, err := url.Parse(path); err == nil && parsed.IsAbs() {
		if len(query) > 0 {
			existing := parsed.Query()
			for key, values := range query {
				for _, value := range values {
					existing.Add(key, value)
				}
			}
			parsed.RawQuery = existing.Encode()
		}
		return parsed.String()
	}

	cleanPath := "/" + strings.TrimLeft(path, "/")
	if !strings.HasPrefix(cleanPath, apiPrefix+"/") && cleanPath != apiPrefix {
		cleanPath = apiPrefix + cleanPath
	}
	u := strings.TrimRight(server, "/") + cleanPath
	if len(query) == 0 {
		return u
	}
	return u + "?" + query.Encode()
}

func newQuickstartCommand(cfg *viper.Viper, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "quickstart",
		Short: "Explain how middlemanctl talks to the API",
		RunE: func(cmd *cobra.Command, args []string) error {
			current, err := readConfig(cfg)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"api_base_url": apiURL(current.server, apiPrefix, nil),
				"formats":      []string{"json", "yaml", "jsonl"},
				"commands": []map[string]string{
					{"command": "middlemanctl version", "does": "GET /api/v1/version"},
					{"command": "middlemanctl pulls --state open --limit 20", "does": "GET /api/v1/pulls with query parameters"},
					{"command": "middlemanctl issues --output jsonl", "does": "Emit one issue JSON object per line"},
					{"command": "middlemanctl api GET /pulls", "does": "Raw Restish-backed request to /api/v1/pulls"},
					{"command": "middlemanctl api GET /sync/status", "does": "Inspect sync state"},
					{"command": "middlemanctl api POST /sync", "does": "Trigger a sync"},
				},
				"notes": []string{
					"PATH values without /api/v1 are automatically scoped to /api/v1.",
					"Use --server to target a non-default daemon.",
					"Use --output yaml when an agent or shell pipeline prefers YAML.",
					"Use --output jsonl for streaming-friendly array responses.",
				},
			}
			return encodeStructured(stdout, current.output, payload)
		},
	}
}

func newAPICommand(request func(context.Context, string, string, url.Values, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "api METHOD PATH [body...]",
		Short: "Call any middleman API path through Restish",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return request(cmd.Context(), args[0], args[1], nil, args[2:])
		},
	}
}

func newSimpleGetCommand(name, short, path string, addFlags func(*cobra.Command), request func(context.Context, string, string, url.Values, []string) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return request(cmd.Context(), http.MethodGet, path, collectChangedQuery(cmd, nil), nil)
		},
	}
	if addFlags != nil {
		addFlags(cmd)
	}
	return cmd
}

func newPullsCommand(request func(context.Context, string, string, url.Values, []string) error) *cobra.Command {
	cmd := newSimpleGetCommand("pulls", "List pull and merge requests", "/pulls", addListFlags, request)
	get := &cobra.Command{
		Use:   "get PROVIDER OWNER NAME NUMBER",
		Short: "Get a pull or merge request detail record",
		Args:  cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := repoNumberPath(cmd.Flag("host").Value.String(), "pulls", args)
			return request(cmd.Context(), http.MethodGet, path, nil, nil)
		},
	}
	get.Flags().String("host", "", "platform host for self-hosted providers")
	cmd.AddCommand(get)
	return cmd
}

func newIssuesCommand(request func(context.Context, string, string, url.Values, []string) error) *cobra.Command {
	cmd := newSimpleGetCommand("issues", "List issues", "/issues", addIssueListFlags, request)
	get := &cobra.Command{
		Use:   "get PROVIDER OWNER NAME NUMBER",
		Short: "Get an issue detail record",
		Args:  cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := repoNumberPath(cmd.Flag("host").Value.String(), "issues", args)
			return request(cmd.Context(), http.MethodGet, path, nil, nil)
		},
	}
	get.Flags().String("host", "", "platform host for self-hosted providers")
	cmd.AddCommand(get)
	return cmd
}

func newSyncCommand(request func(context.Context, string, string, url.Values, []string) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Trigger a full sync",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return request(cmd.Context(), http.MethodPost, "/sync", nil, nil)
		},
	}
	cmd.AddCommand(newSimpleGetCommand("status", "Show sync status", "/sync/status", nil, request))
	return cmd
}

func newWorkspacesCommand(request func(context.Context, string, string, url.Values, []string) error) *cobra.Command {
	cmd := newSimpleGetCommand("workspaces", "List middleman workspaces", "/workspaces", nil, request)
	get := &cobra.Command{
		Use:   "get ID",
		Short: "Get one workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return request(cmd.Context(), http.MethodGet, "/workspaces/"+url.PathEscape(args[0]), nil, nil)
		},
	}
	cmd.AddCommand(get)
	return cmd
}

func newActivityCommand(request func(context.Context, string, string, url.Values, []string) error) *cobra.Command {
	return newSimpleGetCommand("activity", "List recent activity", "/activity", func(cmd *cobra.Command) {
		cmd.Flags().String("since", "", "RFC3339 timestamp to fetch activity after")
		cmd.Flags().Int("limit", 0, "maximum activity records to return")
	}, request)
}

func addListFlags(cmd *cobra.Command) {
	addIssueListFlags(cmd)
	cmd.Flags().String("kanban", "", "filter by kanban state")
}

func addIssueListFlags(cmd *cobra.Command) {
	cmd.Flags().String("repo", "", "filter by owner/name or provider-aware repo key")
	cmd.Flags().String("state", "", "filter by state")
	cmd.Flags().String("q", "", "search query")
	cmd.Flags().Bool("starred", false, "only starred items")
	cmd.Flags().Int("limit", 0, "maximum items to return")
	cmd.Flags().Int("offset", 0, "offset for pagination")
}

func collectChangedQuery(cmd *cobra.Command, _ []string) url.Values {
	query := url.Values{}
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		switch flag.Name {
		case "server", "output", "timeout":
			return
		}
		query.Set(flag.Name, flag.Value.String())
	})
	return query
}

func repoNumberPath(host, resource string, args []string) string {
	prefix := ""
	if host != "" {
		prefix = "/host/" + url.PathEscape(host)
	}
	return fmt.Sprintf(
		"%s/%s/%s/%s/%s/%s",
		prefix,
		resource,
		url.PathEscape(args[0]),
		url.PathEscape(args[1]),
		url.PathEscape(args[2]),
		url.PathEscape(args[3]),
	)
}

func encodeStructured(w io.Writer, format string, payload any) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	case "yaml":
		enc := yaml.NewEncoder(w)
		defer enc.Close()
		return enc.Encode(payload)
	case "jsonl":
		return encodeJSONLines(w, payload)
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}

func writeResponse(w io.Writer, format string, body []byte) error {
	if format == "json" {
		_, err := w.Write(body)
		return err
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		_, writeErr := w.Write(body)
		return writeErr
	}
	if format == "jsonl" {
		return encodeJSONLines(w, payload)
	}
	return encodeStructured(w, format, payload)
}

func encodeJSONLines(w io.Writer, payload any) error {
	enc := json.NewEncoder(w)
	if rows, ok := payload.([]any); ok {
		for _, row := range rows {
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		return nil
	}
	return enc.Encode(payload)
}

var restishMu sync.Mutex

func makeRestishRequest(ctx context.Context, cfg cliConfig, method, requestURL string, bodyArgs []string) ([]byte, error) {
	restishMu.Lock()
	defer restishMu.Unlock()

	viper.Reset()
	cli.Init("middlemanctl_restish", "dev")
	cli.Defaults()
	cli.AddLoader(openapi.New())
	viper.Set("rsh-no-cache", true)
	viper.Set("rsh-profile", "default")

	var body io.Reader
	if len(bodyArgs) > 0 {
		bodyString, err := cli.GetBody("application/json", bodyArgs)
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(bodyString)
	}

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), requestURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := cli.MakeRequest(req, cli.WithClient(&http.Client{Timeout: cfg.timeout}))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.DecodeResponse(resp); err != nil {
		return nil, err
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("middleman API returned %s", resp.Status)
	}
	return responseBody, nil
}

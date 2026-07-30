package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/shorthand/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	configpkg "go.kenn.io/forge/internal/config"
	"go.yaml.in/yaml/v3"
)

const (
	apiPrefix            = "/api/v1"
	controlCommandMarker = "forge.io/control-command"
)

type apiRequester func(context.Context, cliConfig, string, string, []string) ([]byte, error)

type APIRequest func(context.Context, string, string, url.Values, []string) error

type Options struct {
	Stdout            io.Writer
	Stderr            io.Writer
	APICommandFactory func(APIRequest) *cobra.Command
}

type commandDeps struct {
	Stdout            io.Writer
	Stderr            io.Writer
	Request           apiRequester
	APICommandFactory func(APIRequest) *cobra.Command
}

type cliConfig struct {
	server  string
	output  string
	timeout time.Duration
}

func NewCommand(opts Options) *cobra.Command {
	return newCommand(commandDeps{
		Stdout:            opts.Stdout,
		Stderr:            opts.Stderr,
		APICommandFactory: opts.APICommandFactory,
	})
}

func RegisterCommands(root *cobra.Command, opts Options) {
	registerCommands(root, commandDeps{
		Stdout:            opts.Stdout,
		Stderr:            opts.Stderr,
		APICommandFactory: opts.APICommandFactory,
	})
}

func Execute(args []string, opts Options) error {
	cmd := NewCommand(opts)
	cmd.SetArgs(args)
	return cmd.Execute()
}

func newCommand(deps commandDeps) *cobra.Command {
	root := &cobra.Command{
		Use:   "kenn-forge",
		Short: "Agent-oriented CLI for the kenn-forge API",
		Long: strings.TrimSpace(`kenn-forge serves kenn-forge API content for agents.

Start with "kenn-forge quickstart" for the API shape, then use typed shortcuts
like "kenn-forge pulls" or the raw HTTP escape hatch:

  kenn-forge api METHOD PATH [body...]

Responses are formatted as JSON by default, YAML with --output yaml, and
newline-delimited JSON with --output jsonl.`),
		SilenceUsage: true,
	}
	registerCommands(root, deps)
	return root
}

func registerCommands(root *cobra.Command, deps commandDeps) {
	if deps.Stdout == nil {
		deps.Stdout = os.Stdout
	}
	if deps.Stderr == nil {
		deps.Stderr = os.Stderr
	}
	if deps.Request == nil {
		deps.Request = makeAPIRequest
	}

	cfg := viper.New()
	cfg.SetEnvPrefix("KENN_FORGE")
	cfg.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	cfg.AutomaticEnv()
	cfg.SetDefault("output", "json")
	cfg.SetDefault("timeout", 30*time.Second)

	root.SetOut(deps.Stdout)
	root.SetErr(deps.Stderr)
	root.PersistentFlags().String("server", "", "kenn-forge server URL (defaults to kenn-forge config host/port)")
	root.PersistentFlags().StringP("output", "o", "json", "response format: json, yaml, or jsonl")
	root.PersistentFlags().Duration("timeout", 30*time.Second, "HTTP request timeout")
	mustBind(cfg, root.PersistentFlags().Lookup("server"), "server")
	mustBind(cfg, root.PersistentFlags().Lookup("output"), "output")
	mustBind(cfg, root.PersistentFlags().Lookup("timeout"), "timeout")
	installControlFlagValidation(root)

	fetch := func(ctx context.Context, method, path string, query url.Values, bodyArgs []string) (cliConfig, []byte, error) {
		current, err := readConfig(cfg)
		if err != nil {
			return cliConfig{}, nil, err
		}
		requestURL, err := apiURL(current.server, path, query)
		if err != nil {
			return cliConfig{}, nil, err
		}
		body, err := deps.Request(ctx, current, method, requestURL, bodyArgs)
		if err != nil {
			return current, body, err
		}
		return current, body, nil
	}

	request := func(ctx context.Context, method, path string, query url.Values, bodyArgs []string) error {
		current, body, err := fetch(ctx, method, path, query, bodyArgs)
		if len(body) > 0 {
			if writeErr := writeResponse(deps.Stdout, current.output, body); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			return err
		}
		return nil
	}
	listOperations := func(ctx context.Context) error {
		current, body, err := fetch(ctx, http.MethodGet, "/openapi.json", nil, nil)
		if err != nil {
			return err
		}
		operations, err := decodeOpenAPIOperations(body)
		if err != nil {
			return err
		}
		return encodeStructured(deps.Stdout, current.output, operations)
	}

	apiCommand := newAPICommand(request)
	if deps.APICommandFactory != nil {
		apiCommand = deps.APICommandFactory(APIRequest(request))
	}
	apiCommand.AddCommand(newAPIListCommand(listOperations))
	controlCommands := []*cobra.Command{
		newQuickstartCommand(cfg, deps.Stdout),
		apiCommand,
		newSimpleGetCommand("repos", "List configured repositories", "/repos", nil, request),
		newSimpleGetCommand("repo-summaries", "List repository summaries", "/repos/summary", nil, request),
		newPullsCommand(request),
		newIssuesCommand(request),
		newSyncCommand(request),
		newSimpleGetCommand("stacks", "List detected pull request stacks", "/stacks", nil, request),
		newWorkspacesCommand(request),
		newSimpleGetCommand("rate-limits", "Show provider rate limit status", "/rate-limits", nil, request),
		newActivityCommand(request),
	}
	for _, command := range controlCommands {
		if command.Annotations == nil {
			command.Annotations = make(map[string]string)
		}
		command.Annotations[controlCommandMarker] = "true"
		root.AddCommand(command)
	}
}

func installControlFlagValidation(root *cobra.Command) {
	previousE := root.PersistentPreRunE
	previous := root.PersistentPreRun
	root.PersistentPreRun = nil
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if !isControlCommand(cmd) {
			for _, name := range []string{"server", "output", "timeout"} {
				if root.PersistentFlags().Changed(name) {
					return fmt.Errorf("--%s can only be used with API control commands", name)
				}
			}
		}
		if previousE != nil {
			return previousE(cmd, args)
		}
		if previous != nil {
			previous(cmd, args)
		}
		return nil
	}
}

func isControlCommand(cmd *cobra.Command) bool {
	for current := cmd; current != nil; current = current.Parent() {
		if current.Annotations[controlCommandMarker] == "true" {
			return true
		}
	}
	return false
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
	server, err := resolveServer(cfg)
	if err != nil {
		return cliConfig{}, err
	}
	return cliConfig{
		server:  server,
		output:  out,
		timeout: cfg.GetDuration("timeout"),
	}, nil
}

func resolveServer(cfg *viper.Viper) (string, error) {
	if raw := strings.TrimSpace(cfg.GetString("server")); raw != "" {
		return strings.TrimRight(raw, "/"), nil
	}
	forgeCfg, err := configpkg.LoadOrCreate(configpkg.DefaultConfigPath())
	if err != nil {
		return "", fmt.Errorf("load kenn-forge config: %w", err)
	}
	return "http://" + forgeCfg.ListenAddr(), nil
}

func apiURL(server, rawPath string, query url.Values) (string, error) {
	parsed, err := url.Parse(rawPath)
	if err != nil {
		return "", fmt.Errorf("invalid API path %q: %w", rawPath, err)
	}
	if parsed.IsAbs() || parsed.Host != "" {
		return "", fmt.Errorf("absolute API URLs are not allowed: %s", parsed.Redacted())
	}

	cleanPath, err := scopedAPIPath(parsed.EscapedPath())
	if err != nil {
		return "", err
	}
	u := strings.TrimRight(server, "/") + cleanPath
	values := parsed.Query()
	for key, queryValues := range query {
		for _, value := range queryValues {
			values.Add(key, value)
		}
	}
	if len(values) == 0 {
		return u, nil
	}
	return u + "?" + values.Encode(), nil
}

func scopedAPIPath(rawPath string) (string, error) {
	if rawPath == "" {
		rawPath = "/"
	}
	candidate := "/" + strings.TrimLeft(rawPath, "/")
	if !strings.HasPrefix(candidate, apiPrefix+"/") && candidate != apiPrefix {
		candidate = apiPrefix + candidate
	}
	for segment := range strings.SplitSeq(candidate, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return "", fmt.Errorf("invalid API path segment %q: %w", segment, err)
		}
		if pathSegmentHasTraversal(decoded) {
			return "", fmt.Errorf("API path dot segments are not allowed: %s", rawPath)
		}
	}
	cleanPath := pathpkg.Clean(candidate)
	if cleanPath != apiPrefix && !strings.HasPrefix(cleanPath, apiPrefix+"/") {
		return "", fmt.Errorf("API path must stay under %s: %s", apiPrefix, rawPath)
	}
	return cleanPath, nil
}

func pathSegmentHasTraversal(decoded string) bool {
	if decoded == "." || decoded == ".." {
		return true
	}
	if !strings.Contains(decoded, "/") {
		return false
	}
	for subsegment := range strings.SplitSeq(decoded, "/") {
		if subsegment == "" || subsegment == "." || subsegment == ".." {
			return true
		}
	}
	return false
}

func mustAPIURL(server, path string, query url.Values) string {
	u, err := apiURL(server, path, query)
	if err != nil {
		panic(err)
	}
	return u
}

func newQuickstartCommand(cfg *viper.Viper, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "quickstart",
		Short: "Explain how kenn-forge talks to the API",
		RunE: func(cmd *cobra.Command, args []string) error {
			current, err := readConfig(cfg)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"api_base_url": mustAPIURL(current.server, apiPrefix, nil),
				"formats":      []string{"json", "yaml", "jsonl"},
				"commands": []map[string]string{
					{"command": "kenn-forge pulls --state open --limit 20", "does": "GET /api/v1/pulls with query parameters"},
					{"command": "kenn-forge issues --output jsonl", "does": "Emit one issue JSON object per line"},
					{"command": "kenn-forge api list", "does": "List API methods, paths, summaries, and parameters"},
					{"command": "kenn-forge api GET /pulls", "does": "Raw HTTP request to /api/v1/pulls"},
					{"command": "kenn-forge api GET /version", "does": "Show server version"},
					{"command": "kenn-forge api GET /sync/status", "does": "Inspect sync state"},
					{"command": "kenn-forge api POST /sync", "does": "Trigger a sync"},
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
		Short: "Call any kenn-forge API path",
		Long: strings.TrimSpace(`Call any kenn-forge API path.

Use "kenn-forge api list" to discover available methods and paths.`),
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return request(cmd.Context(), args[0], args[1], nil, args[2:])
		},
	}
}

func newAPIListCommand(listOperations func(context.Context) error) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available kenn-forge API operations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listOperations(cmd.Context())
		},
	}
}

type apiOperationRecord struct {
	Method      string   `json:"method" yaml:"method"`
	Path        string   `json:"path" yaml:"path"`
	OperationID string   `json:"operation_id,omitempty" yaml:"operation_id,omitempty"`
	Summary     string   `json:"summary,omitempty" yaml:"summary,omitempty"`
	QueryParams []string `json:"query_params,omitempty" yaml:"query_params,omitempty"`
}

type openAPIDocument struct {
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

type openAPIOperation struct {
	OperationID string             `json:"operationId"`
	Summary     string             `json:"summary"`
	Parameters  []openAPIParameter `json:"parameters"`
}

type openAPIParameter struct {
	Name string `json:"name"`
	In   string `json:"in"`
}

func decodeOpenAPIOperations(body []byte) ([]apiOperationRecord, error) {
	var doc openAPIDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(doc.Paths))
	for path := range doc.Paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var operations []apiOperationRecord
	for _, path := range paths {
		pathItem := doc.Paths[path]
		methods := sortedOpenAPIMethods(pathItem)
		for _, method := range methods {
			var operation openAPIOperation
			if err := json.Unmarshal(pathItem[method], &operation); err != nil {
				return nil, fmt.Errorf("decode %s %s operation: %w", strings.ToUpper(method), path, err)
			}
			record := apiOperationRecord{
				Method:      strings.ToUpper(method),
				Path:        path,
				OperationID: operation.OperationID,
				Summary:     operation.Summary,
			}
			for _, param := range operation.Parameters {
				if param.In == "query" {
					record.QueryParams = append(record.QueryParams, param.Name)
				}
			}
			operations = append(operations, record)
		}
	}
	return operations, nil
}

var openAPIMethodOrder = []string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
	http.MethodHead,
	http.MethodOptions,
	http.MethodTrace,
}

func sortedOpenAPIMethods(pathItem map[string]json.RawMessage) []string {
	present := make(map[string]bool, len(pathItem))
	for method := range pathItem {
		present[strings.ToUpper(method)] = true
	}
	methods := make([]string, 0, len(present))
	for _, method := range openAPIMethodOrder {
		if present[method] {
			methods = append(methods, strings.ToLower(method))
		}
	}
	return methods
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
	cmd := newSimpleGetCommand("workspaces", "List kenn-forge workspaces", "/workspaces", nil, request)
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
	value := reflect.ValueOf(payload)
	if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) && value.Type().Elem().Kind() != reflect.Uint8 {
		for i := 0; i < value.Len(); i++ {
			if err := enc.Encode(value.Index(i).Interface()); err != nil {
				return err
			}
		}
		return nil
	}
	return enc.Encode(payload)
}

func makeAPIRequest(ctx context.Context, cfg cliConfig, method, requestURL string, bodyArgs []string) ([]byte, error) {
	body, err := requestBody(bodyArgs)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), requestURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if methodRequiresJSONContentType(req.Method) {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := (&http.Client{Timeout: cfg.timeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return responseBody, apiStatusError{Status: resp.Status, Body: responseBody}
	}
	return responseBody, nil
}

func requestBody(bodyArgs []string) (io.Reader, error) {
	if len(bodyArgs) == 0 {
		return nil, nil
	}
	input, _, err := shorthand.GetInput(bodyArgs, shorthand.ParseOptions{
		EnableFileInput:       true,
		EnableObjectDetection: true,
	})
	if err != nil {
		return nil, err
	}
	if input == nil {
		return nil, nil
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(body), nil
}

func methodRequiresJSONContentType(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead:
		return false
	default:
		return true
	}
}

type apiStatusError struct {
	Status string
	Body   []byte
}

func (e apiStatusError) Error() string {
	body := strings.TrimSpace(string(e.Body))
	if body == "" {
		return fmt.Sprintf("kenn-forge API returned %s", e.Status)
	}
	return fmt.Sprintf("kenn-forge API returned %s: %s", e.Status, body)
}

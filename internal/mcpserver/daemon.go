package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/runtimelock"
)

type daemonError struct {
	Kind    string
	Code    string
	Message string
	Details map[string]any
}

func (e *daemonError) Error() string {
	return e.Kind + ": " + e.Message
}

type daemonClient struct {
	configPath string
	timeout    time.Duration

	mu                sync.Mutex
	baseURL           string
	token             string
	identity          daemonIdentity
	workflowProbeDone bool
	workflowProbeErr  *daemonError
}

type daemonIdentity struct {
	pid       int
	startedAt string
}

func newDaemonClient(configPath string, timeout time.Duration) *daemonClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}
	return &daemonClient{configPath: configPath, timeout: timeout}
}

func (c *daemonClient) discover() error {
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return &daemonError{Kind: "daemon_unavailable", Message: "load config: " + err.Error()}
	}
	st, err := runtimelock.Read(cfg.DataDir)
	if err != nil {
		return &daemonError{Kind: "daemon_unavailable", Message: "read runtime status: " + err.Error()}
	}
	if !st.Running || st.Metadata == nil {
		return &daemonError{
			Kind:    "daemon_unavailable",
			Message: "no Kenn Forge daemon is running on " + cfg.DataDir,
		}
	}
	prefix := st.Metadata.BasePath
	if prefix == "" {
		prefix = cfg.BasePath
	}
	prefix = strings.TrimSuffix(prefix, "/")
	token, err := runtimelock.ReadAuthToken(cfg.DataDir)
	if err != nil {
		return &daemonError{Kind: "daemon_unavailable", Message: "read auth token failed"}
	}

	identity := daemonIdentity{pid: st.Metadata.PID, startedAt: st.Metadata.StartedAt}
	c.mu.Lock()
	if c.identity != identity {
		c.workflowProbeDone = false
		c.workflowProbeErr = nil
	}
	c.baseURL = "http://" + st.Metadata.ListenAddr + prefix
	c.token = token
	c.identity = identity
	c.mu.Unlock()
	return nil
}

func (c *daemonClient) ensureWorkflowStateSupported(ctx context.Context) error {
	if err := c.discover(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.workflowProbeDone {
		err := c.workflowProbeErr
		c.mu.Unlock()
		if err != nil {
			return err
		}
		return nil
	}
	c.mu.Unlock()

	var out struct {
		Items []any `json:"items"`
	}
	query := url.Values{}
	query.Set("limit", "1")
	err := c.getJSON(ctx, "/api/v1/workflow-state", query, &out)
	if err != nil {
		var derr *daemonError
		if errors.As(err, &derr) && derr.Kind == "not_found" {
			probeErr := &daemonError{
				Kind:    "version_mismatch",
				Message: "daemon does not support /workflow-state; upgrade kenn-forge",
			}
			c.cacheWorkflowProbe(probeErr)
			return probeErr
		}
		return err
	}
	c.cacheWorkflowProbe(nil)
	return nil
}

func (c *daemonClient) cacheWorkflowProbe(err *daemonError) {
	c.mu.Lock()
	c.workflowProbeDone = true
	c.workflowProbeErr = err
	c.mu.Unlock()
}

func (c *daemonClient) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	err := c.do(ctx, http.MethodGet, path, query, nil, out)
	var derr *daemonError
	if errors.As(err, &derr) && derr.Kind == "daemon_unavailable" {
		if rediscoverErr := c.discover(); rediscoverErr != nil {
			return rediscoverErr
		}
		return c.do(ctx, http.MethodGet, path, query, nil, out)
	}
	return err
}

func (c *daemonClient) putJSON(ctx context.Context, path string, body, out any) error {
	err := c.do(ctx, http.MethodPut, path, nil, body, out)
	var derr *daemonError
	if errors.As(err, &derr) && derr.Kind == "daemon_unavailable" {
		_ = c.discover()
	}
	return err
}

func (c *daemonClient) do(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
	out any,
) error {
	c.mu.Lock()
	base := c.baseURL
	c.mu.Unlock()
	if base == "" {
		if err := c.discover(); err != nil {
			return err
		}
		c.mu.Lock()
		base = c.baseURL
		c.mu.Unlock()
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return &daemonError{Kind: "daemon_error", Message: "encode request: " + err.Error()}
		}
		reader = bytes.NewReader(data)
	}

	u := base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, u, reader)
	if err != nil {
		return &daemonError{Kind: "daemon_error", Message: "build request: " + err.Error()}
	}
	if body != nil || method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return daemonRequestError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil {
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return &daemonError{Kind: "daemon_error", Message: "decode response: " + err.Error()}
		}
		return nil
	}
	return problemToDaemonError(resp)
}

func daemonRequestError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &daemonError{Kind: "daemon_timeout", Message: "daemon request timed out"}
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, io.EOF) {
		return &daemonError{Kind: "daemon_unavailable", Message: "daemon connection failed"}
	}
	return &daemonError{Kind: "daemon_unavailable", Message: "daemon request failed"}
}

func problemToDaemonError(resp *http.Response) error {
	var prob struct {
		Status  int            `json:"status"`
		Code    string         `json:"code"`
		Detail  string         `json:"detail"`
		Details map[string]any `json:"details"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&prob)
	message := prob.Detail
	if message == "" {
		message = resp.Status
	}
	if prob.Code != "" {
		message = fmt.Sprintf("%s (%s)", message, prob.Code)
	}

	kind := "daemon_error"
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		kind = "daemon_auth"
	case resp.StatusCode == http.StatusNotFound:
		kind = "not_found"
	case resp.StatusCode == http.StatusConflict:
		kind = "conflict"
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		kind = "invalid_request"
	}
	return &daemonError{Kind: kind, Code: prob.Code, Message: message, Details: prob.Details}
}

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/runtimelock"
)

func TestServeStartsProfilerListenerE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	bin := buildMiddleman(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	cfgPath := filepath.Join(root, "config.toml")

	appPort := reserveFreePort(t)
	profilerPort := reserveFreePort(t)
	writeMinimalConfig(t, cfgPath, dataDir, appPort)

	cmd := procutil.Command(
		bin,
		"serve",
		"--config", cfgPath,
		"--pprof-addr", "127.0.0.1:"+strconv.Itoa(profilerPort),
	)
	var stderr bytes.Buffer
	cmd.Stdout = os.Stderr
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(),
		"MIDDLEMAN_LOG_LEVEL=warn",
		"MIDDLEMAN_GITHUB_TOKEN_UNSET_FOR_LOCK_E2E=",
	)
	require.NoError(cmd.Start())
	stopped := false
	t.Cleanup(func() {
		if !stopped && cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGKILL)
			_ = cmd.Wait()
		}
	})

	waitForFile(t, runtimelock.MetadataPath(dataDir), 10*time.Second)
	healthBody := waitForHTTPBody(
		t,
		fmt.Sprintf("http://127.0.0.1:%d/healthz", appPort),
		10*time.Second,
	)
	profilerBody := waitForHTTPBody(
		t,
		fmt.Sprintf(
			"http://127.0.0.1:%d/debug/pprof/",
			profilerPort,
		),
		10*time.Second,
	)

	assert.Contains(healthBody, `"status":"ok"`)
	assert.Contains(profilerBody, "Types of profiles available")
	profilerURL := fmt.Sprintf("http://127.0.0.1:%d", profilerPort)
	assert.Equal(
		http.StatusForbidden,
		profilerStatus(
			t,
			profilerURL+"/debug/pprof/",
			"localhost:"+strconv.Itoa(profilerPort),
			nil,
		),
	)
	assert.Equal(
		http.StatusForbidden,
		profilerStatus(
			t,
			profilerURL+"/debug/pprof/",
			"",
			map[string]string{"Sec-Fetch-Site": "cross-site"},
		),
	)
	assert.Equal(
		http.StatusForbidden,
		profilerStatus(
			t,
			profilerURL+"/debug/pprof/",
			"",
			map[string]string{"Origin": "http://example.com"},
		),
	)
	assert.Equal(
		http.StatusForbidden,
		profilerStatus(
			t,
			profilerURL+"/debug/pprof/",
			"",
			map[string]string{
				"User-Agent": "Mozilla/5.0 AppleWebKit/537.36 Chrome/125.0.0.0 Safari/537.36",
			},
		),
	)
	assert.Equal(
		http.StatusBadRequest,
		profilerStatus(
			t,
			profilerURL+"/debug/pprof/profile?seconds=31",
			"",
			nil,
		),
	)
	assert.Equal(
		http.StatusBadRequest,
		profilerStatus(
			t,
			profilerURL+"/debug/pprof/heap?seconds=31",
			"",
			nil,
		),
	)

	require.NoError(cmd.Process.Signal(syscall.SIGTERM))
	require.NoError(cmd.Wait(), stderr.String())
	stopped = true
}

func profilerStatus(
	t *testing.T,
	url string,
	host string,
	headers map[string]string,
) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	if host != "" {
		req.Host = host
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	require.NoError(t, err)
	return resp.StatusCode
}

func waitForHTTPBody(
	t *testing.T,
	url string,
	timeout time.Duration,
) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
		} else if closeErr != nil {
			lastErr = closeErr
		} else if resp.StatusCode == http.StatusOK {
			return string(body)
		} else {
			lastErr = fmt.Errorf(
				"GET %s returned %d: %s",
				url, resp.StatusCode, body,
			)
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NoError(t, lastErr, "timed out waiting for %s", url)
	require.FailNowf(t, "timed out waiting for HTTP", "url=%s", url)
	return ""
}

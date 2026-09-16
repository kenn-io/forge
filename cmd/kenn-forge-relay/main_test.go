package main

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/activityrelay"
)

func TestShutdownEndsOpenSubscriptionsPromptly(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	assert := assert.New(t)
	dir := t.TempDir()
	secret := filepath.Join(dir, "team")
	require.NoError(os.WriteFile(secret, []byte("fixture-secret"), 0o600))
	config := filepath.Join(dir, "relay.toml")
	require.NoError(os.WriteFile(config, fmt.Appendf(nil,
		"webhook_listen = %q\nfeed_listen = %q\n[sources.team]\nsecret_file = %q\nrepository_ids = [1]\n",
		"127.0.0.1:0", "127.0.0.1:0", secret), 0o600))
	ctx, cancel := context.WithCancel(t.Context())
	readyReader, readyWriter := io.Pipe()
	finished := make(chan error, 1)
	go func() { finished <- run(ctx, config, readyWriter) }()
	var addresses struct {
		Feed string `json:"feed"`
	}
	scanner := bufio.NewScanner(readyReader)
	require.True(scanner.Scan())
	require.NoError(json.Unmarshal(scanner.Bytes(), &addresses))
	stream, err := activityrelay.Open(ctx, &http.Client{}, "http://"+addresses.Feed)
	require.NoError(err)
	readErr := make(chan error, 1)
	go func() { readErr <- stream.Read(func(activityrelay.Hint) {}) }()
	started := time.Now()
	cancel()
	select {
	case err := <-finished:
		require.NoError(err, "a routine stop must not report a shutdown timeout")
	case <-time.After(5 * time.Second):
		require.FailNow("relay did not stop while a subscription was open")
	}
	assert.Less(time.Since(started), 5*time.Second)
	select {
	case err := <-readErr:
		require.Error(err, "the subscriber must observe the closed stream")
	case <-time.After(5 * time.Second):
		require.FailNow("subscriber read did not end after shutdown")
	}
	require.NoError(stream.Close())
}

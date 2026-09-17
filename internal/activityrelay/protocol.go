// Package activityrelay implements a stateless GitHub refresh-hint feed.
//
// The relay reduces signed webhooks to routing hints and writes each hint to
// every open subscriber connection, batching check hints in memory. Nothing is
// persisted: subscribers rely on ordinary syncing to recover missed hints.
package activityrelay

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const (
	PullRequest       = "pull_request"
	PullRequestChecks = "pull_request_checks"
	Issue             = "issue"
	RepositoryRefs    = "repository_refs"
	Repository        = "repository"
)

type Hint struct {
	Provider     string `json:"provider"`
	Host         string `json:"host"`
	RepositoryID string `json:"repository_id"`
	Target       string `json:"target"`
	Number       int    `json:"number,omitempty"`
}

func (h Hint) Validate() error {
	if h.Provider != "github" || h.Host != "github.com" || len(h.RepositoryID) == 0 || len(h.RepositoryID) > 256 {
		return errors.New("invalid relay repository identity")
	}
	switch h.Target {
	case PullRequest, PullRequestChecks, Issue:
		if h.Number > 0 {
			return nil
		}
	case RepositoryRefs, Repository:
		if h.Number == 0 {
			return nil
		}
	}
	return errors.New("invalid relay refresh target")
}

// Stream is one open subscription to a relay feed.
type Stream struct {
	body io.ReadCloser
	// idleTimeout closes a stream that stops delivering bytes. The relay
	// writes a keepalive every keepaliveInterval, so silence beyond this is a
	// stalled proxy or connection, not a quiet feed.
	idleTimeout time.Duration
}

const (
	hintEvent         = "hint"
	maxEventLineBytes = 4096
	streamIdleTimeout = 3 * keepaliveInterval
)

// Open connects to the feed and returns once the relay has accepted the
// subscription. Callers own the returned stream and must close it.
func Open(ctx context.Context, client *http.Client, baseURL string) (*Stream, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse relay URL: %w", err)
	}
	endpoint.Path = "/activity"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create relay request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subscribe to relay: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("relay returned HTTP %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		_ = response.Body.Close()
		return nil, errors.New("relay did not return an event stream")
	}
	return &Stream{body: response.Body, idleTimeout: streamIdleTimeout}, nil
}

// Read delivers hints until the connection ends. It always returns a non-nil
// error describing why delivery stopped.
func (s *Stream) Read(handle func(Hint)) error {
	var stalled atomic.Bool
	idle := time.AfterFunc(s.idleTimeout, func() {
		stalled.Store(true)
		_ = s.body.Close()
	})
	defer idle.Stop()
	scanner := bufio.NewScanner(s.body)
	scanner.Buffer(make([]byte, maxEventLineBytes), maxEventLineBytes)
	var event string
	var data []string
	for scanner.Scan() {
		idle.Reset(s.idleTimeout)
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if event == hintEvent {
				var hint Hint
				if json.Unmarshal([]byte(strings.Join(data, "\n")), &hint) != nil || hint.Validate() != nil {
					return errors.New("relay sent an invalid hint")
				}
				handle(hint)
			}
			event, data = "", nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			data = append(data, value)
		}
	}
	if stalled.Load() {
		return errors.New("relay stream stalled")
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read relay stream: %w", err)
	}
	return io.ErrUnexpectedEOF
}

func (s *Stream) Close() error { return s.body.Close() }

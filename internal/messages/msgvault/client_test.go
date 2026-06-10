package msgvault

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMsgvault stands up a synthetic msgvault server with configurable
// responses. Tests assert on req.Header and req.URL.RawQuery to verify the
// client wires bearer auth and query params correctly.
func fakeMsgvault(t *testing.T, handler http.Handler) (*httptest.Server, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	return srv, func() { srv.Close() }
}

func TestClientCapabilitiesProbesBothEndpoints(t *testing.T) {
	assert := Assert.New(t)
	var sawHealth, sawStats bool
	var statsAuth string
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			sawHealth = true
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/stats":
			sawStats = true
			statsAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"total_messages":1234}`))
		default:
			assert.Failf("unexpected upstream call", "path=%s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer cleanup()

	c := NewClient(srv.URL, "secret-key")
	require.NoError(t, c.Capabilities(context.Background()))
	assert.True(sawHealth)
	assert.True(sawStats)
	assert.Equal("Bearer secret-key", statsAuth)
}

func TestClientCapabilitiesUnauthorizedFromStats(t *testing.T) {
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/stats":
			http.Error(w, `{"error":"unauthorized","message":"bad key"}`, http.StatusUnauthorized)
		}
	}))
	defer cleanup()

	c := NewClient(srv.URL, "wrong-key")
	err := c.Capabilities(context.Background())
	Assert.Equal(t, "unauthorized", Classify(err))
}

func TestClientCapabilitiesDownOnRefused(t *testing.T) {
	// Bind a listener so we know the port was free, then close it so
	// dial gets ECONNREFUSED deterministically. Reserved port 1 is
	// flaky on CI hosts that have a privileged process binding it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	c := NewClient("http://"+addr, "k")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = c.Capabilities(ctx)
	Assert.Equal(t, "down", Classify(err))
}

func TestClientCapabilitiesTimeoutOnSlowUpstream(t *testing.T) {
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the test's context deadline.
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer cleanup()

	c := NewClient(srv.URL, "k")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.Capabilities(ctx)
	Assert.Equal(t, "timeout", Classify(err))
}

func TestClientSearchPassesQueryParams(t *testing.T) {
	assert := Assert.New(t)
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal("/api/v1/search", r.URL.Path)
		assert.Equal("project sync", query.Get("q"))
		assert.Equal("fts", query.Get("mode"))
		assert.Equal("2", query.Get("page"))
		_ = json.NewEncoder(w).Encode(SearchResult{
			Query: "project sync", Total: 1, Page: 2, PageSize: 20,
			Messages: []MessageSummary{{ID: 42, Subject: "Project sync"}},
		})
	}))
	defer cleanup()

	c := NewClient(srv.URL, "k")
	out, err := c.Search(context.Background(), SearchParams{Query: "project sync", Mode: "fts", Page: 2, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, out.Messages, 1)
	assert.Equal(int64(42), out.Messages[0].ID)
}

func TestClientNormalizesOmittedArrays(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/search":
			_, _ = w.Write([]byte(`{"messages":[{"id":1}]}`))
		case "/api/v1/messages/1":
			_, _ = w.Write([]byte(`{"id":1,"body":"plain"}`))
		case "/api/v1/messages/filter":
			_, _ = w.Write([]byte(`{"messages":[{"id":2}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cleanup()
	c := NewClient(srv.URL, "k")

	search, err := c.Search(context.Background(), SearchParams{Query: "anything"})
	require.NoError(err)
	require.Len(search.Messages, 1)
	assert.NotNil(search.Messages)
	assert.Empty(search.Messages[0].To)
	assert.Empty(search.Messages[0].CC)
	assert.Empty(search.Messages[0].BCC)
	assert.Empty(search.Messages[0].Labels)

	message, err := c.Message(context.Background(), 1)
	require.NoError(err)
	assert.Empty(message.To)
	assert.Empty(message.CC)
	assert.Empty(message.BCC)
	assert.Empty(message.Labels)
	assert.NotNil(message.Attachments)

	thread, err := c.Thread(context.Background(), 99, "date", "asc")
	require.NoError(err)
	require.Len(thread, 1)
	assert.Empty(thread[0].To)
	assert.Empty(thread[0].CC)
	assert.Empty(thread[0].BCC)
	assert.Empty(thread[0].Labels)
}

func TestClientAggregatesTranslatesSearchQuery(t *testing.T) {
	var seenSearchQuery string
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Assert.Equal(t, "/api/v1/aggregates", r.URL.Path)
		seenSearchQuery = r.URL.Query().Get("search_query")
		_ = json.NewEncoder(w).Encode(AggregateResult{ViewType: "senders", Rows: nil})
	}))
	defer cleanup()

	c := NewClient(srv.URL, "k")
	_, err := c.Aggregates(context.Background(), AggregateParams{ViewType: "senders", SearchQuery: "label:Inbox"})
	require.NoError(t, err)
	Assert.Equal(t, "label:Inbox", seenSearchQuery)
}

func TestClientThreadPinsSort(t *testing.T) {
	assert := Assert.New(t)
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal("1001", q.Get("conversation_id"))
		assert.Equal("date", q.Get("sort"))
		assert.Equal("asc", q.Get("direction"))
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer cleanup()

	c := NewClient(srv.URL, "k")
	_, err := c.Thread(context.Background(), 1001, "date", "asc")
	require.NoError(t, err)
}

func TestClientThreadRejectsNullPayload(t *testing.T) {
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Assert.Equal(t, "/api/v1/messages/filter", r.URL.Path)
		_, _ = w.Write([]byte(`null`))
	}))
	defer cleanup()

	c := NewClient(srv.URL, "k")
	_, err := c.Thread(context.Background(), 1001, "date", "asc")
	require.ErrorIs(t, err, ErrMalformedUpstream)
	Assert.Equal(t, "malformed", Classify(err))
}

func TestClientMessageDecodesBodyHTML(t *testing.T) {
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Assert.Equal(t, "/api/v1/messages/42", r.URL.Path)
		_, _ = w.Write([]byte(`{
			"id": 42, "subject": "Hi",
			"body": "plain text body",
			"body_html": "<p>hi</p>",
			"attachments": []
		}`))
	}))
	defer cleanup()

	c := NewClient(srv.URL, "k")
	out, err := c.Message(context.Background(), 42)
	require.NoError(t, err)
	Assert.Equal(t, "plain text body", out.Body)
	Assert.Equal(t, "<p>hi</p>", out.BodyHTML)
}

func TestClientInlineStreamsAndPassesContentType(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer cleanup()

	c := NewClient(srv.URL, "k")
	ct, body, err := c.InlineImage(context.Background(), 42, "logo")
	require.NoError(err)
	defer func() { _ = body.Close() }()

	assert.Equal("image/png", ct)
	raw, err := io.ReadAll(body)
	require.NoError(err)
	assert.Len(raw, 4)
}

func TestClientUpstreamErrorDecodesEnvelope(t *testing.T) {
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"message_not_found","message":"no such id"}`))
	}))
	defer cleanup()

	c := NewClient(srv.URL, "k")
	_, err := c.Message(context.Background(), 99)
	var apiErr *Error
	require.ErrorAs(t, err, &apiErr)
	Assert.Equal(t, http.StatusNotFound, apiErr.Status)
	Assert.Equal(t, "message_not_found", apiErr.Code)
}

func TestClientSearchWrapsDecodeError(t *testing.T) {
	assert := Assert.New(t)
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{ this is not json`))
	}))
	defer cleanup()

	c := NewClient(srv.URL, "k")
	_, err := c.Search(context.Background(), SearchParams{Query: "anything"})
	require.Error(t, err)
	assert.Equal("unknown", Classify(err))
	assert.Contains(err.Error(), "/api/v1/search")
}

func TestClientUpstreamErrorWithNonJSONBody(t *testing.T) {
	srv, cleanup := fakeMsgvault(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<html>upstream crashed</html>`))
	}))
	defer cleanup()

	c := NewClient(srv.URL, "k")
	_, err := c.Message(context.Background(), 7)
	var apiErr *Error
	require.ErrorAs(t, err, &apiErr)
	Assert.Equal(t, http.StatusBadGateway, apiErr.Status)
	Assert.Contains(t, apiErr.Message, "upstream crashed")
}

func TestErrorStringIncludesMsgvaultPrefix(t *testing.T) {
	err := &Error{Status: http.StatusBadGateway, Code: "bad_gateway", Message: "upstream crashed"}
	Assert.Equal(t, "msgvault: upstream 502 bad_gateway: upstream crashed", err.Error())
}

func TestErrorsAsStillFindsClientError(t *testing.T) {
	err := errors.Join(&Error{Status: http.StatusNotFound, Code: "missing", Message: "gone"})
	var apiErr *Error
	require.ErrorAs(t, err, &apiErr)
	Assert.Equal(t, "missing", apiErr.Code)
}

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/messages"
)

type savedSearchesWire struct {
	Searches []messages.SavedSearch `json:"searches"`
	ETag     string                 `json:"etag"`
}

func setupSavedSearchesRouteServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv(messages.SavedSearchesEnv, filepath.Join(t.TempDir(), "saved.toml"))
	return setupMsgvaultRouteServer(t, &config.Config{})
}

func decodeSavedSearches(t *testing.T, rr *httptest.ResponseRecorder) savedSearchesWire {
	t.Helper()
	var out savedSearchesWire
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&out))
	return out
}

func doSavedSearchesReplace(t *testing.T, srv *Server, searches []messages.SavedSearch, ifMatch string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"searches": searches}
	reqBody, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/messages/saved-searches", bytes.NewReader(reqBody))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(middlemanCSRFHeaderName, "1")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func TestMessagesSavedSearchesEmpty(t *testing.T) {
	srv := setupSavedSearchesRouteServer(t)

	rr := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/messages/saved-searches", nil)

	require.Equal(t, http.StatusOK, rr.Code)
	etagHeader := rr.Header().Get("ETag")
	require.True(t, strings.HasPrefix(etagHeader, `"sha256:`), "ETag header = %q", etagHeader)
	body := decodeSavedSearches(t, rr)
	assert := Assert.New(t)
	assert.Empty(body.Searches)
	assert.NotNil(body.Searches)
	assert.Equal(etagHeader, body.ETag)
}

func TestMessagesSavedSearchesPutThenGetRoundTrip(t *testing.T) {
	srv := setupSavedSearchesRouteServer(t)
	in := []messages.SavedSearch{
		{Name: "Recent", Query: "newer_than:7d"},
		{Name: "From alice", Query: "from:alice@example.com"},
	}

	putRR := doSavedSearchesReplace(t, srv, in, "")
	require.Equal(t, http.StatusOK, putRR.Code)
	putBody := decodeSavedSearches(t, putRR)

	getRR := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/messages/saved-searches", nil)
	require.Equal(t, http.StatusOK, getRR.Code)
	getBody := decodeSavedSearches(t, getRR)

	assert := Assert.New(t)
	assert.Equal(putBody.ETag, getBody.ETag)
	assert.Equal(in, getBody.Searches)
}

func TestMessagesSavedSearchesPutIfMatchStaleReturns412(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv := setupSavedSearchesRouteServer(t)
	firstRR := doSavedSearchesReplace(t, srv, []messages.SavedSearch{{Name: "x", Query: "y"}}, "")
	require.Equal(http.StatusOK, firstRR.Code)

	rr := doSavedSearchesReplace(t, srv, []messages.SavedSearch{{Name: "x", Query: "z"}}, `"sha256:bogus"`)

	require.Equal(http.StatusPreconditionFailed, rr.Code)
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeConflict, problem.Code)
	assert.Equal("stale_etag", problem.Details["reason"])
}

func TestMessagesSavedSearchesPutIfMatchOmittedAccepted(t *testing.T) {
	srv := setupSavedSearchesRouteServer(t)

	rr := doSavedSearchesReplace(t, srv, []messages.SavedSearch{{Name: "bootstrap", Query: "q"}}, "")

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestMessagesSavedSearchesPutRequiresMiddlemanCSRFHeader(t *testing.T) {
	srv := setupSavedSearchesRouteServer(t)

	rr := doMsgvaultRawWithoutCSRF(t, srv, http.MethodPut, "/api/v1/messages/saved-searches",
		"127.0.0.1:12345", "application/json", `{"searches":[]}`)

	require.Equal(t, http.StatusForbidden, rr.Code)
	assert := Assert.New(t)
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeForbidden, problem.Code)
	assert.Equal("missingCsrfHeader", problem.Details["reason"])
}

func TestMessagesSavedSearchesPutCanonicalizes(t *testing.T) {
	srv := setupSavedSearchesRouteServer(t)
	in := []messages.SavedSearch{
		{Name: "", Query: "  "},
		{Name: "A", Query: "q1"},
		{Name: "a", Query: "q2"},
	}

	rr := doSavedSearchesReplace(t, srv, in, "")

	require.Equal(t, http.StatusOK, rr.Code)
	body := decodeSavedSearches(t, rr)
	Assert.Equal(t, []messages.SavedSearch{{Name: "a", Query: "q2"}}, body.Searches)
}

func TestMessagesSavedSearchesPutLoopbackOnly(t *testing.T) {
	srv := setupSavedSearchesRouteServer(t)

	rr := doMsgvaultRaw(t, srv, http.MethodPut, "/api/v1/messages/saved-searches", "203.0.113.7:54321", "application/json", `{"searches":[]}`)

	require.Equal(t, http.StatusForbidden, rr.Code)
	assert := Assert.New(t)
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeForbidden, problem.Code)
	assert.Equal("loopbackOnly", problem.Details["reason"])
}

func TestMessagesSavedSearchesGetLoopbackOnly(t *testing.T) {
	srv := setupSavedSearchesRouteServer(t)

	rr := doMsgvaultRaw(t, srv, http.MethodGet, "/api/v1/messages/saved-searches", "203.0.113.7:54321", "", "")

	require.Equal(t, http.StatusForbidden, rr.Code)
	assert := Assert.New(t)
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeForbidden, problem.Code)
	assert.Equal("loopbackOnly", problem.Details["reason"])
}

func TestMessagesSavedSearchesPutInvalidJSON(t *testing.T) {
	srv := setupSavedSearchesRouteServer(t)

	rr := doMsgvaultRaw(t, srv, http.MethodPut, "/api/v1/messages/saved-searches", "127.0.0.1:12345", "application/json", "{")

	require.Equal(t, http.StatusBadRequest, rr.Code)
	Assert.Equal(t, CodeBadRequest, decodeMsgvaultProblem(t, rr).Code)
}

func TestMessagesSavedSearchesPutMissingSearchesField(t *testing.T) {
	srv := setupSavedSearchesRouteServer(t)

	rr := doMsgvaultRaw(t, srv, http.MethodPut, "/api/v1/messages/saved-searches", "127.0.0.1:12345", "application/json", "{}")

	require.Equal(t, http.StatusUnprocessableEntity, rr.Code)
	assert := Assert.New(t)
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeValidationError, problem.Code)
}

func TestMessagesSavedSearchesPutNullSearchesField(t *testing.T) {
	srv := setupSavedSearchesRouteServer(t)

	rr := doMsgvaultRaw(t, srv, http.MethodPut, "/api/v1/messages/saved-searches", "127.0.0.1:12345", "application/json", `{"searches":null}`)

	require.Equal(t, http.StatusUnprocessableEntity, rr.Code)
	assert := Assert.New(t)
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeValidationError, problem.Code)
}

func TestMessagesSavedSearchesPutTolerantPerEntry(t *testing.T) {
	srv := setupSavedSearchesRouteServer(t)

	body := `{"searches":[
		"not-an-object",
		{"name":"bad","query":123},
		{"name":"missing-query"},
		{"name":42,"query":"keep-name-fallback"},
		{"name":"good","query":"q"}
	]}`
	rr := doMsgvaultRaw(t, srv, http.MethodPut, "/api/v1/messages/saved-searches", "127.0.0.1:12345", "application/json", body)

	require.Equal(t, http.StatusOK, rr.Code)
	got := decodeSavedSearches(t, rr)
	want := []messages.SavedSearch{
		{Name: "keep-name-fallback", Query: "keep-name-fallback"},
		{Name: "good", Query: "q"},
	}
	Assert.Equal(t, want, got.Searches)
}

func TestMessagesSavedSearchesConcurrentSameETagPutsSerialize(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv := setupSavedSearchesRouteServer(t)
	seedRR := doSavedSearchesReplace(t, srv, []messages.SavedSearch{{Name: "seed", Query: "q"}}, "")
	require.Equal(http.StatusOK, seedRR.Code)
	etag := decodeSavedSearches(t, seedRR).ETag

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]int, 2)
	responseBodies := make([]string, 2)
	wg.Add(2)
	for i := range 2 {
		go func() {
			defer wg.Done()
			<-start
			reqBody, _ := json.Marshal(map[string]any{"searches": []messages.SavedSearch{
				{Name: "racer", Query: "q" + string(rune('A'+i))},
			}})
			req := httptest.NewRequest(
				http.MethodPut,
				"/api/v1/messages/saved-searches",
				bytes.NewReader(reqBody),
			)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(middlemanCSRFHeaderName, "1")
			req.Header.Set("If-Match", etag)
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			results[i] = rr.Code
			responseBodies[i] = rr.Body.String()
		}()
	}
	close(start)
	wg.Wait()

	okCount := 0
	staleCount := 0
	for i := range 2 {
		switch results[i] {
		case http.StatusOK:
			okCount++
		case http.StatusPreconditionFailed:
			var problem ProblemError
			require.NoError(json.NewDecoder(strings.NewReader(responseBodies[i])).Decode(&problem))
			assert.Equal("stale_etag", problem.Details["reason"])
			staleCount++
		default:
			require.Failf("unexpected status", "status=%d body=%q", results[i], responseBodies[i])
		}
	}
	require.Equal(1, okCount, "want exactly one 200 (results=%v bodies=%v)", results, responseBodies)
	require.Equal(1, staleCount, "want exactly one 412 (results=%v bodies=%v)", results, responseBodies)
}

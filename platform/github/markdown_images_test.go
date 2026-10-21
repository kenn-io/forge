package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

type contentsRequest struct {
	Path   string
	Ref    string
	Accept string
}

// contentsServer serves the contents API for one repository file that lives on
// a branch whose name contains a slash, the case a web URL cannot delimit.
func contentsServer(t *testing.T, body []byte) (*httptest.Server, func() []contentsRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []contentsRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, contentsRequest{
			Path: r.URL.Path, Ref: r.URL.Query().Get("ref"), Accept: r.Header.Get("Accept"),
		})
		mu.Unlock()
		if r.URL.Path != "/api/v3/repos/acme/widgets/contents/docs/images/search.png" ||
			r.URL.Query().Get("ref") != "feat/search-controls" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.github.raw; charset=utf-8")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, func() []contentsRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]contentsRequest(nil), requests...)
	}
}

func newMarkdownImageTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{Host: "github.com", Read: server.Client(), Write: server.Client(), Notifications: server.Client(), Clock: time.Now, APIBase: server.URL + "/api/v3/", ViewerCacheTTL: time.Hour})
	require.NoError(t, err)
	return client
}

func TestGetMarkdownImageResolvesRepositoryFileAcrossSlashedBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, requests := contentsServer(t, pngBytes)
	client := newMarkdownImageTestClient(t, server)

	image, err := client.GetMarkdownImage(t.Context(), "acme", "widgets",
		"https://github.com/acme/widgets/blob/feat/search-controls/docs/images/search.png?raw=true")
	require.NoError(err)
	assert.Equal("image/png", image.ContentType)
	assert.Equal(pngBytes, image.Content)
	assert.True(image.Mutable, "branch-addressed files change under the same URL")

	got := requests()
	require.Len(got, 2, "the shortest ref candidate misses before the slashed branch resolves")
	assert.Equal(contentsRequest{
		Path:   "/api/v3/repos/acme/widgets/contents/search-controls/docs/images/search.png",
		Ref:    "feat",
		Accept: "application/vnd.github.raw+json",
	}, got[0])
	assert.Equal(contentsRequest{
		Path:   "/api/v3/repos/acme/widgets/contents/docs/images/search.png",
		Ref:    "feat/search-controls",
		Accept: "application/vnd.github.raw+json",
	}, got[1])
}

func TestGetMarkdownImageAcceptsRawAndRawHostRepositoryURLs(t *testing.T) {
	server, _ := contentsServer(t, pngBytes)
	client := newMarkdownImageTestClient(t, server)

	for _, source := range []string{
		"https://github.com/acme/widgets/raw/feat/search-controls/docs/images/search.png",
		"https://raw.githubusercontent.com/acme/widgets/feat/search-controls/docs/images/search.png",
		"https://raw.githubusercontent.com/Acme/Widgets/feat/search-controls/docs/images/search.png",
	} {
		image, err := client.GetMarkdownImage(t.Context(), "acme", "widgets", source)
		require.NoError(t, err, source)
		assert.Equal(t, "image/png", image.ContentType, source)
	}
}

func TestGetMarkdownImageRejectsFilesOutsideRouteRepository(t *testing.T) {
	server, requests := contentsServer(t, pngBytes)
	client := newMarkdownImageTestClient(t, server)

	for _, source := range []string{
		"https://github.com/acme/other/blob/main/docs/images/search.png?raw=true",
		"https://raw.githubusercontent.com/acme/other/main/docs/images/search.png",
		"https://github.com/acme/widgets/tree/main/docs",
		"https://github.com/acme/widgets/blob/main",
		"https://github.com/acme/widgets/blob/main/../secret.png",
		"https://gist.githubusercontent.com/acme/widgets/main/docs/images/search.png",
	} {
		_, err := client.GetMarkdownImage(t.Context(), "acme", "widgets", source)
		var providerErr *platform.Error
		require.ErrorAs(t, err, &providerErr, source)
		assert.Equal(t, platform.ErrCodeInvalidArgument, providerErr.Code, source)
	}
	assert.Empty(t, requests(), "rejected sources must not reach the provider")
}

func TestGetMarkdownImageReportsMissingRepositoryFile(t *testing.T) {
	server, requests := contentsServer(t, pngBytes)
	client := newMarkdownImageTestClient(t, server)

	_, err := client.GetMarkdownImage(t.Context(), "acme", "widgets",
		"https://github.com/acme/widgets/blob/main/docs/images/missing.png?raw=true")
	var providerErr *platform.Error
	require.ErrorAs(t, err, &providerErr)
	assert.Equal(t, platform.ErrCodeNotFound, providerErr.Code)
	assert.Len(t, requests(), 3, "every ref split is tried before reporting not found")
}

func TestGetMarkdownImageRejectsNonImageRepositoryFile(t *testing.T) {
	server, _ := contentsServer(t, []byte("<html><body>Not an image</body></html>"))
	client := newMarkdownImageTestClient(t, server)

	_, err := client.GetMarkdownImage(t.Context(), "acme", "widgets",
		"https://github.com/acme/widgets/blob/feat/search-controls/docs/images/search.png?raw=true")
	var providerErr *platform.Error
	require.ErrorAs(t, err, &providerErr)
	assert.Equal(t, platform.ErrCodeInvalidArgument, providerErr.Code)
}

func TestGetMarkdownImageReadsRepositorySVG(t *testing.T) {
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 80"><rect width="120" height="80" fill="green"/></svg>`
	for _, tc := range []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "svg", content: svg},
		{name: "xml declaration", content: `<?xml version="1.0"?>` + svg},
		{name: "html with svg extension", content: `<html><body>Not an image</body></html>`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal("/api/v3/repos/acme/widgets/contents/diagram.svg", r.URL.Path)
				assert.Equal("main", r.URL.Query().Get("ref"))
				w.Header().Set("Content-Type", "application/vnd.github.raw; charset=utf-8")
				_, _ = w.Write([]byte(tc.content))
			}))
			t.Cleanup(server.Close)
			client := newMarkdownImageTestClient(t, server)

			image, err := client.GetMarkdownImage(t.Context(), "acme", "widgets",
				"https://github.com/acme/widgets/raw/main/diagram.svg")
			if tc.wantErr {
				var providerErr *platform.Error
				require.ErrorAs(t, err, &providerErr)
				assert.Equal(platform.ErrCodeInvalidArgument, providerErr.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal("image/svg+xml", image.ContentType)
			assert.Equal([]byte(tc.content), image.Content)
		})
	}
}

func TestGetMarkdownImageRejectsOversizedRepositoryFile(t *testing.T) {
	oversized := make([]byte, maxMarkdownImageBytes+1)
	copy(oversized, pngBytes)
	server, _ := contentsServer(t, oversized)
	client := newMarkdownImageTestClient(t, server)

	_, err := client.GetMarkdownImage(t.Context(), "acme", "widgets",
		"https://github.com/acme/widgets/blob/feat/search-controls/docs/images/search.png?raw=true")
	require.ErrorIs(t, err, errMarkdownImageTooLarge)
}

// githubHostServer serves requests addressed to any github.com host from one
// TLS test server, routing on the original Host so tests exercise the real
// attachment, API, and signed-storage hosts.
func githubHostServer(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	base := server.Client().Transport
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		routed := r.Clone(r.Context())
		routed.URL.Scheme, routed.URL.Host = target.Scheme, target.Host
		return base.RoundTrip(routed)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newGitHubHostImageClient(t *testing.T, hc *http.Client) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		Host: "github.com", Read: hc, Write: hc, Notifications: hc, MarkdownImages: hc,
		Clock: time.Now, ViewerCacheTTL: time.Hour,
		Authentication: Authentication{Source: staticMediaToken("user-token")},
	})
	require.NoError(t, err)
	return client
}

const unreachableAttachment = "https://github.com/user-attachments/assets/aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb"

func TestGetMarkdownImageFallsBackToRenderedSignedURLWhenAttachmentIsMissing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const signedPath = "/1001/2002-aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb.png"
	var mu sync.Mutex
	var signedAuth []string
	hc := githubHostServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Host + r.URL.Path {
		case "github.com/user-attachments/assets/aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb":
			w.WriteHeader(http.StatusNotFound)
		case "api.github.com/markdown":
			assert.Equal("Bearer user-token", r.Header.Get("Authorization"))
			var body map[string]string
			assert.NoError(json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(map[string]string{
				"text": "![](" + unreachableAttachment + ")", "mode": "gfm", "context": "acme/widgets",
			}, body)
			_, _ = fmt.Fprintf(w, `<p dir="auto"><a href="x"><img src="https://private-user-images.githubusercontent.com%s?jwt=a.b.c&amp;v=1" alt="" style="max-width: 100%%;"></a></p>`, signedPath)
		case "private-user-images.githubusercontent.com" + signedPath:
			assert.Equal("a.b.c", r.URL.Query().Get("jwt"))
			assert.Equal("1", r.URL.Query().Get("v"), "HTML entities in the rendered src are decoded")
			mu.Lock()
			signedAuth = append(signedAuth, r.Header.Get("Authorization"))
			mu.Unlock()
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		default:
			assert.Failf("unexpected request", "%s%s", r.Host, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	client := newGitHubHostImageClient(t, hc)

	image, err := client.GetMarkdownImage(t.Context(), "acme", "widgets", unreachableAttachment)
	require.NoError(err)
	assert.Equal("image/png", image.ContentType)
	assert.Equal(pngBytes, image.Content)
	assert.False(image.Mutable, "the signed URL resolves the same immutable upload")
	assert.Equal([]string{""}, signedAuth, "the signed URL carries its own authorization")
}

func TestGetMarkdownImageDoesNotFollowRenderedSourceOffGitHubStorage(t *testing.T) {
	hc := githubHostServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Host + r.URL.Path {
		case "github.com/user-attachments/assets/aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb":
			w.WriteHeader(http.StatusNotFound)
		case "api.github.com/markdown":
			_, _ = w.Write([]byte(`<p><img src="https://attacker.example/steal.png"></p>`))
		default:
			assert.Failf(t, "unexpected request", "%s%s", r.Host, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	client := newGitHubHostImageClient(t, hc)

	_, err := client.GetMarkdownImage(t.Context(), "acme", "widgets", unreachableAttachment)
	var providerErr *platform.Error
	require.ErrorAs(t, err, &providerErr)
	assert.Equal(t, platform.ErrCodeNotFound, providerErr.Code)
}

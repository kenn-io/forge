package github

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

type staticMediaToken string

func (s staticMediaToken) Token(context.Context) (string, error) { return string(s), nil }

func (staticMediaToken) Invalidate(string) {}

func newMarkdownMediaTestClient(t *testing.T, upstream *httptest.Server, media *http.Client) *Client {
	t.Helper()
	host := upstream.Listener.Addr().String()
	client, err := NewClient(ClientConfig{
		Host: host, Read: upstream.Client(), Write: upstream.Client(), Notifications: upstream.Client(),
		MarkdownImages: media, Clock: time.Now, ViewerCacheTTL: time.Hour,
		Authentication: Authentication{Source: staticMediaToken("user-token")},
	})
	require.NoError(t, err)
	return client
}

func mediaPlatformError(t *testing.T, err error) *platform.Error {
	t.Helper()
	platformErr, ok := errors.AsType[*platform.Error](err)
	require.True(t, ok, "expected *platform.Error, got %v", err)
	return platformErr
}

func TestOpenMarkdownMediaForwardsRangeAndReturnsPartial(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/user-attachments/assets/a1", r.URL.Path)
		assert.Equal(t, "bytes=0-0", r.Header.Get("Range"))
		assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-0/1765992")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
	}))
	defer upstream.Close()
	client := newMarkdownMediaTestClient(t, upstream, upstream.Client())

	media, err := client.OpenMarkdownMedia(t.Context(), "acme", "widgets",
		"https://"+upstream.Listener.Addr().String()+"/user-attachments/assets/a1", "bytes=0-0")
	require.NoError(t, err)
	defer media.Body.Close()

	assert.True(t, media.Partial)
	assert.Equal(t, "bytes 0-0/1765992", media.ContentRange)
	assert.Equal(t, "video/mp4", media.ContentType)
	assert.Equal(t, int64(1), media.ContentLength)
	body, err := io.ReadAll(media.Body)
	require.NoError(t, err)
	assert.Equal(t, []byte{0}, body)
}

func TestOpenMarkdownMediaAcceptsRepositoryAssetForRouteRepository(t *testing.T) {
	const assetPath = "/Acme/Widgets/assets/12345/0f8e1a52-3c55-4d39-8a43-7c1f0b5d9e21"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, assetPath, r.URL.Path)
		assert.Empty(t, r.Header.Get("Range"))
		w.Header().Set("Content-Type", "video/webm")
		_, _ = w.Write([]byte("webm"))
	}))
	defer upstream.Close()
	client := newMarkdownMediaTestClient(t, upstream, upstream.Client())

	media, err := client.OpenMarkdownMedia(t.Context(), "acme", "widgets",
		"https://"+upstream.Listener.Addr().String()+assetPath, "")
	require.NoError(t, err)
	defer media.Body.Close()

	assert.False(t, media.Partial)
	assert.Equal(t, "video/webm", media.ContentType)
}

func TestOpenMarkdownMediaFollowsRedirectWithoutCredential(t *testing.T) {
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"), "signed storage must not receive the user credential")
		assert.Equal(t, "bytes=0-0", r.Header.Get("Range"))
		w.Header().Set("Content-Type", "video/quicktime")
		w.Header().Set("Content-Range", "bytes 0-0/20")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{1})
	}))
	defer storage.Close()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
		http.Redirect(w, r, "https://example.com/asset.mov?signature=x", http.StatusFound)
	}))
	defer upstream.Close()

	// example.com resolves to the storage server so the redirect leaves the
	// attachment hostname, which is what makes Go drop Authorization.
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	dialer := &net.Dialer{}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "example.com:443" {
			addr = storage.Listener.Addr().String()
		}
		return dialer.DialContext(ctx, network, addr)
	}
	client := newMarkdownMediaTestClient(t, upstream, &http.Client{Transport: transport})

	media, err := client.OpenMarkdownMedia(t.Context(), "acme", "widgets",
		"https://"+upstream.Listener.Addr().String()+"/user-attachments/assets/a1", "bytes=0-0")
	require.NoError(t, err)
	defer media.Body.Close()

	assert.Equal(t, "video/quicktime", media.ContentType)
	assert.True(t, media.Partial)
}

func TestOpenMarkdownMediaRejectsImageAsset(t *testing.T) {
	released := make(chan struct{})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial png"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(released)
	}))
	defer upstream.Close()
	client := newMarkdownMediaTestClient(t, upstream, upstream.Client())

	_, err := client.OpenMarkdownMedia(t.Context(), "acme", "widgets",
		"https://"+upstream.Listener.Addr().String()+"/user-attachments/assets/a1", "bytes=0-0")

	assert.Equal(t, platform.ErrCodeUnsupportedMediaType, mediaPlatformError(t, err).Code)
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		require.Fail(t, "upstream response was left open after rejecting its content type")
	}
}

func TestOpenMarkdownMediaRejectsForeignSources(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		assert.Fail(t, "a rejected source must not reach the network")
	}))
	defer upstream.Close()
	client := newMarkdownMediaTestClient(t, upstream, upstream.Client())
	host := upstream.Listener.Addr().String()

	for _, source := range []string{
		"https://example.com/user-attachments/assets/a1",
		"https://" + host + "/other/repo/assets/1/0f8e1a52-3c55-4d39-8a43-7c1f0b5d9e21",
		"http://" + host + "/user-attachments/assets/a1",
		"https://" + host + "/user-attachments/assets/",
		"https://" + host + "/user-attachments/assets/a1/extra",
		"https://" + host + "/acme/widgets/assets/latest/0f8e1a52-3c55-4d39-8a43-7c1f0b5d9e21",
		"https://" + host + "/acme/widgets/blob/main/demo.mp4",
	} {
		t.Run(source, func(t *testing.T) {
			_, err := client.OpenMarkdownMedia(t.Context(), "acme", "widgets", source, "")
			platformErr := mediaPlatformError(t, err)
			assert.Equal(t, platform.ErrCodeInvalidArgument, platformErr.Code)
			assert.Equal(t, "source", platformErr.Field)
		})
	}
}

func TestOpenMarkdownMediaMapsUpstreamStatus(t *testing.T) {
	tests := []struct {
		status int
		code   platform.PlatformErrorCode
	}{
		{http.StatusRequestedRangeNotSatisfiable, platform.ErrCodeRangeNotSatisfiable},
		{http.StatusUnauthorized, platform.ErrCodePermissionDenied},
		{http.StatusForbidden, platform.ErrCodePermissionDenied},
		{http.StatusNotFound, platform.ErrCodeNotFound},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer upstream.Close()
			client := newMarkdownMediaTestClient(t, upstream, upstream.Client())

			_, err := client.OpenMarkdownMedia(t.Context(), "acme", "widgets",
				"https://"+upstream.Listener.Addr().String()+"/user-attachments/assets/a1", "bytes=5-")

			assert.Equal(t, tc.code, mediaPlatformError(t, err).Code)
		})
	}
}

func TestOpenMarkdownMediaReportsUnexpectedStatusWithoutTypedCode(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()
	client := newMarkdownMediaTestClient(t, upstream, upstream.Client())

	_, err := client.OpenMarkdownMedia(t.Context(), "acme", "widgets",
		"https://"+upstream.Listener.Addr().String()+"/user-attachments/assets/a1", "")

	require.Error(t, err)
	_, typed := errors.AsType[*platform.Error](err)
	assert.False(t, typed, "an upstream outage says nothing about the asset type")
}

func TestOpenMarkdownMediaStreamsPastHeaderBound(t *testing.T) {
	previous := markdownMediaHeaderTimeout
	markdownMediaHeaderTimeout = 50 * time.Millisecond
	t.Cleanup(func() { markdownMediaHeaderTimeout = previous })
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("late body"))
	}))
	defer upstream.Close()
	client := newMarkdownMediaTestClient(t, upstream, upstream.Client())

	media, err := client.OpenMarkdownMedia(t.Context(), "acme", "widgets",
		"https://"+upstream.Listener.Addr().String()+"/user-attachments/assets/a1", "")
	require.NoError(t, err)
	defer media.Body.Close()

	body, err := io.ReadAll(media.Body)
	require.NoError(t, err)
	assert.Equal(t, "late body", string(body))
}

func TestOpenMarkdownMediaBoundsHeaderWait(t *testing.T) {
	previous := markdownMediaHeaderTimeout
	markdownMediaHeaderTimeout = 50 * time.Millisecond
	t.Cleanup(func() { markdownMediaHeaderTimeout = previous })
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()
	client := newMarkdownMediaTestClient(t, upstream, upstream.Client())

	started := time.Now()
	_, err := client.OpenMarkdownMedia(t.Context(), "acme", "widgets",
		"https://"+upstream.Listener.Addr().String()+"/user-attachments/assets/a1", "")

	require.Error(t, err)
	assert.Less(t, time.Since(started), 5*time.Second)
}

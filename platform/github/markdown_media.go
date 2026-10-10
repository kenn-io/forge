package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
)

// GitHub accepts these video uploads in issue and pull request bodies.
var allowedMarkdownMediaTypes = map[string]struct{}{
	"video/mp4":       {},
	"video/quicktime": {},
	"video/webm":      {},
}

// markdownMediaHeaderTimeout bounds only the wait for response headers. The
// body streams for as long as the browser keeps playing, so a whole-request
// timeout would cut long videos off mid-playback.
var markdownMediaHeaderTimeout = 30 * time.Second

// OpenMarkdownMedia streams a video attachment with the repository's user
// credential. Only GitHub's two attachment shapes on the platform host are
// accepted, and the repository-scoped shape only for the route's own
// repository, so the proxy cannot fetch arbitrary URLs. The attachment URL
// redirects to signed storage on another host; Go drops Authorization when the
// redirect leaves the attachment host.
func (c *Client) OpenMarkdownMedia(
	ctx context.Context,
	owner, repo, sourceURL, byteRange string,
) (platform.MarkdownMedia, error) {
	parsed, err := url.Parse(sourceURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" ||
		!strings.EqualFold(parsed.Host, c.platformHost) ||
		!markdownMediaAttachmentPath(parsed.EscapedPath(), owner, repo) {
		return platform.MarkdownMedia{}, c.invalidMarkdownMediaSource()
	}
	if c.source == nil {
		return platform.MarkdownMedia{}, errors.New("GitHub markdown media token source is unavailable")
	}
	// user-attachments returns 404 for installation tokens, as for images.
	authCtx := c.authContext(ctx, owner, true)
	token, err := c.source.Token(authCtx)
	if err != nil {
		return platform.MarkdownMedia{}, err
	}

	requestCtx, cancel := context.WithCancel(authCtx)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		cancel()
		return platform.MarkdownMedia{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	client := c.markdownImageHTTPClient
	if client == nil {
		client = &http.Client{}
	}
	headerWait := time.AfterFunc(markdownMediaHeaderTimeout, cancel)
	resp, err := client.Do(req)
	if !headerWait.Stop() {
		if err == nil {
			_ = resp.Body.Close()
		}
		cancel()
		return platform.MarkdownMedia{}, fmt.Errorf(
			"fetch GitHub markdown media: no response headers within %s", markdownMediaHeaderTimeout,
		)
	}
	if err != nil {
		cancel()
		return platform.MarkdownMedia{}, fmt.Errorf("fetch GitHub markdown media: %w", err)
	}
	media, err := c.markdownMediaResponse(resp)
	if err != nil {
		_ = resp.Body.Close()
		cancel()
		return platform.MarkdownMedia{}, err
	}
	media.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return media, nil
}

func (c *Client) markdownMediaResponse(resp *http.Response) (platform.MarkdownMedia, error) {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
	case http.StatusRequestedRangeNotSatisfiable:
		return platform.MarkdownMedia{}, &platform.Error{
			Code: platform.ErrCodeRangeNotSatisfiable, Provider: platform.KindGitHub, PlatformHost: c.platformHost,
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		return platform.MarkdownMedia{}, platform.PermissionDenied(platform.KindGitHub, c.platformHost, errors.New(resp.Status))
	case http.StatusNotFound:
		return platform.MarkdownMedia{}, &platform.Error{
			Code: platform.ErrCodeNotFound, Provider: platform.KindGitHub, PlatformHost: c.platformHost,
		}
	default:
		return platform.MarkdownMedia{}, fmt.Errorf("fetch GitHub markdown media: %s", resp.Status)
	}
	contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if _, ok := allowedMarkdownMediaTypes[contentType]; err != nil || !ok {
		return platform.MarkdownMedia{}, &platform.Error{
			Code: platform.ErrCodeUnsupportedMediaType, Provider: platform.KindGitHub, PlatformHost: c.platformHost,
			Err: fmt.Errorf("unsupported media content type %q", resp.Header.Get("Content-Type")),
		}
	}
	return platform.MarkdownMedia{
		ContentType:   contentType,
		ContentLength: resp.ContentLength,
		ContentRange:  resp.Header.Get("Content-Range"),
		Partial:       resp.StatusCode == http.StatusPartialContent,
	}, nil
}

func (c *Client) invalidMarkdownMediaSource() error {
	return &platform.Error{
		Code: platform.ErrCodeInvalidArgument, Provider: platform.KindGitHub,
		PlatformHost: c.platformHost, Field: "source", Err: errors.New("unsupported markdown media URL"),
	}
}

// markdownMediaAttachmentPath accepts /user-attachments/assets/<id> and the
// older /<owner>/<repo>/assets/<number>/<uuid> form for the route repository.
func markdownMediaAttachmentPath(escapedPath, owner, repo string) bool {
	segments := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	if len(segments) == 3 && segments[0] == "user-attachments" && segments[1] == "assets" {
		return segments[2] != ""
	}
	if len(segments) != 5 || segments[2] != "assets" || segments[4] == "" ||
		!strings.EqualFold(segments[0], owner) || !strings.EqualFold(segments[1], repo) {
		return false
	}
	_, err := strconv.ParseUint(segments[3], 10, 64)
	return err == nil
}

// cancelOnClose releases the request context when the caller closes the
// stream, which ends the upstream fetch for an abandoned player.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

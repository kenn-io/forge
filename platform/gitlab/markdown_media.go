package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
)

// GitLab plays an upload as video when image syntax names one of these
// extensions. Its uploads API answers application/octet-stream, so the
// extension is the only type signal.
var markdownMediaTypesByExtension = map[string]string{
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".mov":  "video/quicktime",
	".webm": "video/webm",
	".ogv":  "video/ogg",
}

// markdownMediaHeaderTimeout bounds only the wait for upload response headers.
var markdownMediaHeaderTimeout = 30 * time.Second

// OpenMarkdownMedia streams a project upload named in markdown image syntax.
// It accepts the same upload URLs as GetMarkdownImage and keeps the auth
// transport's origin check, so a redirect elsewhere fails instead of carrying
// the token.
func (c *Client) OpenMarkdownMedia(
	ctx context.Context,
	ref platform.RepoRef,
	sourceURL, byteRange string,
) (platform.MarkdownMedia, error) {
	lookupCtx, cancel := c.withForegroundTimeout(ctx)
	_, ref, err := c.projectScopedArg(lookupCtx, ref)
	cancel()
	if err != nil {
		return platform.MarkdownMedia{}, err
	}
	secret, filename, err := c.markdownUploadParts(ref, sourceURL)
	if err != nil {
		return platform.MarkdownMedia{}, err
	}
	contentType, ok := markdownMediaTypesByExtension[strings.ToLower(path.Ext(filename))]
	if !ok {
		return platform.MarkdownMedia{}, &platform.Error{
			Code: platform.ErrCodeUnsupportedMediaType, Provider: platform.KindGitLab, PlatformHost: c.host,
			Err: fmt.Errorf("upload %q is not a video", filename),
		}
	}
	projectID, ok := ref.Key.ID()
	if !ok {
		return platform.MarkdownMedia{}, &platform.Error{
			Code: platform.ErrCodeInvalidRepoRef, Provider: platform.KindGitLab,
			PlatformHost: c.host, Field: "platform_id", Err: errors.New("missing GitLab project ID"),
		}
	}

	endpoint := strings.TrimRight(c.baseURL, "/") + "/projects/" +
		strconv.FormatInt(projectID, 10) + "/uploads/" +
		url.PathEscape(secret) + "/" + url.PathEscape(filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return platform.MarkdownMedia{}, err
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	resp, err := platform.DoMediaRequest(c.httpClient, req, markdownMediaHeaderTimeout)
	if err != nil {
		return platform.MarkdownMedia{}, fmt.Errorf("fetch GitLab markdown media: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		return platform.MarkdownMedia{}, c.markdownMediaStatusError(resp)
	}
	return platform.MarkdownMedia{
		Body:          resp.Body,
		ContentType:   contentType,
		ContentLength: resp.ContentLength,
		ContentRange:  resp.Header.Get("Content-Range"),
		Partial:       resp.StatusCode == http.StatusPartialContent,
	}, nil
}

func (c *Client) markdownMediaStatusError(resp *http.Response) error {
	var code platform.PlatformErrorCode
	switch resp.StatusCode {
	case http.StatusBadRequest:
		code = platform.ErrCodeInvalidRepoRef
	case http.StatusUnauthorized, http.StatusForbidden:
		code = platform.ErrCodePermissionDenied
	case http.StatusNotFound:
		code = platform.ErrCodeNotFound
	case http.StatusTooManyRequests:
		code = platform.ErrCodeRateLimited
	case http.StatusRequestedRangeNotSatisfiable:
		code = platform.ErrCodeRangeNotSatisfiable
	default:
		return fmt.Errorf("GitLab markdown media request failed: %s", resp.Status)
	}
	return &platform.Error{
		Code: code, Provider: platform.KindGitLab, PlatformHost: c.host,
		Capability: "read_markdown_media", Err: errors.New(resp.Status),
	}
}

package providerapi

import (
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
)

type markdownMediaInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Source       string `query:"source"`
	Range        string `header:"Range" doc:"Single byte range forwarded to the provider"`
}

type markdownMediaHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Source       string `query:"source"`
	Range        string `header:"Range" doc:"Single byte range forwarded to the provider"`
}

func (s *Handlers) getMarkdownMedia(ctx context.Context, input *markdownMediaInput) (*huma.StreamResponse, error) {
	return s.getMarkdownMediaFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Source, input.Range)
}

func (s *Handlers) getMarkdownMediaOnHost(
	ctx context.Context,
	input *markdownMediaHostInput,
) (*huma.StreamResponse, error) {
	return s.getMarkdownMediaFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Source, input.Range)
}

// getMarkdownMediaFor opens the upstream stream before answering so provider
// failures become normal problem responses, then copies the body as it
// arrives. Video can be far larger than an image, so nothing is buffered or
// cached on disk; the browser's own cache keeps what it played.
func (s *Handlers) getMarkdownMediaFor(
	ctx context.Context,
	provider, platformHost, owner, name, source, byteRange string,
) (*huma.StreamResponse, error) {
	repo, err := s.RepoResolver.RequireRouteCapability(
		ctx, provider, platformHost, owner, name, itemapi.CapabilityReadMarkdownMedia,
	)
	if err != nil {
		return nil, err
	}
	kind := httpapi.ProviderKind(repo.Repo)
	host := httpapi.ProviderHost(repo.Repo)
	reader, err := (*s.Syncer).Registry().MarkdownMediaReader(kind, host)
	if err != nil {
		return nil, markdownImageError(ctx, err, kind, host)
	}
	media, err := reader.OpenMarkdownMedia(ctx, httpapi.PlatformRepoRef(repo.Repo), source, byteRange)
	if err != nil {
		return nil, markdownImageError(ctx, err, kind, host)
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer media.Body.Close()
		hctx.SetHeader("Content-Type", media.ContentType)
		if media.ContentLength >= 0 {
			hctx.SetHeader("Content-Length", strconv.FormatInt(media.ContentLength, 10))
		}
		hctx.SetHeader("Accept-Ranges", "bytes")
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		hctx.SetHeader("Cache-Control", "private, max-age=31536000, immutable")
		status := http.StatusOK
		if media.Partial {
			hctx.SetHeader("Content-Range", media.ContentRange)
			status = http.StatusPartialContent
		}
		hctx.SetStatus(status)
		// A copy error means the browser went away or the upstream broke
		// mid-stream; the status line is already sent, so there is no
		// response left to change.
		_, _ = io.Copy(hctx.BodyWriter(), media.Body)
	}}, nil
}

func markdownMediaResponses() map[string]*huma.Response {
	video := map[string]*huma.MediaType{
		"video/mp4":       {Schema: &huma.Schema{Type: "string", Format: "binary"}},
		"video/ogg":       {Schema: &huma.Schema{Type: "string", Format: "binary"}},
		"video/quicktime": {Schema: &huma.Schema{Type: "string", Format: "binary"}},
		"video/webm":      {Schema: &huma.Schema{Type: "string", Format: "binary"}},
	}
	return map[string]*huma.Response{
		"200": {Description: "Whole media", Content: video},
		"206": {Description: "Requested byte range", Content: video},
	}
}

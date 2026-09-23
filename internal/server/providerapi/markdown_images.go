package providerapi

import (
	"context"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/platform"
)

type markdownImageOutput struct {
	ContentType           string `header:"Content-Type"`
	CacheControl          string `header:"Cache-Control"`
	ContentLength         string `header:"Content-Length"`
	ContentTypeOptions    string `header:"X-Content-Type-Options"`
	ContentSecurityPolicy string `header:"Content-Security-Policy"`
	Body                  []byte
}

type markdownImageInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Source       string `query:"source"`
}

type markdownImageHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Source       string `query:"source"`
}

func (s *Handlers) getMarkdownImage(ctx context.Context, input *markdownImageInput) (*markdownImageOutput, error) {
	return s.getMarkdownImageFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Source)
}

func (s *Handlers) getMarkdownImageOnHost(ctx context.Context, input *markdownImageHostInput) (*markdownImageOutput, error) {
	return s.getMarkdownImageFor(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Source)
}

func (s *Handlers) getMarkdownImageFor(
	ctx context.Context,
	provider, platformHost, owner, name, source string,
) (*markdownImageOutput, error) {
	repo, err := s.RepoResolver.RequireRouteCapability(
		ctx, provider, platformHost, owner, name, itemapi.CapabilityReadMarkdownImages,
	)
	if err != nil {
		return nil, err
	}
	kind := httpapi.ProviderKind(*repo)
	host := httpapi.ProviderHost(*repo)
	reader, err := (*s.Syncer).Registry().MarkdownImageReader(kind, host)
	if err != nil {
		return nil, markdownImageError(ctx, err, kind, host)
	}
	ref := httpapi.PlatformRepoRef(*repo)
	image, err := (*s.MarkdownImages).load(ctx, markdownImageCacheKey(ref, source), func(fetchCtx context.Context) (platform.MarkdownImage, error) {
		return reader.GetMarkdownImage(fetchCtx, ref, source)
	})
	if err != nil {
		return nil, markdownImageError(ctx, err, kind, host)
	}
	output := &markdownImageOutput{
		ContentType:        image.ContentType,
		CacheControl:       markdownImageCacheControl(image),
		ContentLength:      strconv.Itoa(len(image.Content)),
		ContentTypeOptions: "nosniff",
		Body:               image.Content,
	}
	if image.ContentType == "image/svg+xml" {
		// An SVG can also be opened as a document. Keep repository content
		// outside Forge's origin and disable scripts and external resources.
		output.ContentSecurityPolicy = "sandbox; default-src 'none'; style-src 'unsafe-inline'"
	}
	return output, nil
}

// markdownImageCacheKey uses the stable provider identity, not the owner/name
// route: a replacement repository at a reused route must never be served the
// previous occupant's private bytes.
func markdownImageCacheKey(ref platform.RepoRef, source string) string {
	return string(ref.Platform) + "\x00" + ref.Host + "\x00" + ref.PlatformExternalID + "\x00" + source
}

func markdownImageCacheControl(image platform.MarkdownImage) string {
	if image.Mutable {
		return "private, max-age=" + strconv.Itoa(int(MarkdownImageMutableTTL.Seconds()))
	}
	return "private, max-age=31536000, immutable"
}

func markdownImageError(ctx context.Context, err error, kind platform.Kind, host string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return httpapi.ProviderCallProblem(err, string(kind), host)
}

func markdownImageResponses() map[string]*huma.Response {
	return map[string]*huma.Response{
		"200": {
			Description: "Image response",
			Content: map[string]*huma.MediaType{
				"image/avif":    {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/bmp":     {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/gif":     {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/jpeg":    {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/png":     {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/svg+xml": {Schema: &huma.Schema{Type: "string", Format: "binary"}},
				"image/webp":    {Schema: &huma.Schema{Type: "string", Format: "binary"}},
			},
		},
	}
}

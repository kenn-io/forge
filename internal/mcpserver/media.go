package mcpserver

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MediaDescriptorKind marks a local file the agent asked the chat to show.
// Agent adapters forward tool results only as text or base64 images, so the
// tool returns a path reference that the chat resolves on the agent host.
const MediaDescriptorKind = "kenn-forge://media/local"

type MediaDescriptor struct {
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Title    string `json:"title,omitempty"`
	MimeType string `json:"mimeType"`
}

type showMediaInput struct {
	Path  string `json:"path" jsonschema:"absolute path to an image, video or audio file on this machine"`
	Title string `json:"title,omitempty" jsonschema:"optional caption shown with the media"`
}

// IsChatMediaType reports whether the chat renders the media type inline.
func IsChatMediaType(mediaType string) bool {
	family, _, _ := strings.Cut(mediaType, "/")
	return family == "image" || family == "video" || family == "audio"
}

func (s *Server) registerMediaTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_show_media",
		Description: "Show a local image, video or audio file inline in the maintainer's Forge ACP chat, for example a screen recording or screenshot you captured. " +
			"Pass the file's absolute path; Forge reads it on this machine when the chat displays it, so keep the file in place. " +
			"Use this instead of Markdown image links to local paths or embedding media in kenn_forge_render_app, which cannot load local files.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in showMediaInput) (*mcp.CallToolResult, MediaDescriptor, error) {
		out := MediaDescriptor{Kind: MediaDescriptorKind, Path: in.Path, Title: strings.TrimSpace(in.Title)}
		if !filepath.IsAbs(in.Path) {
			return nil, out, errors.New("path must be absolute")
		}
		mediaType, err := sniffLocalMediaType(in.Path)
		if err != nil {
			return nil, out, err
		}
		out.MimeType = mediaType
		data, err := json.Marshal(out)
		if err != nil {
			return nil, out, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, out, nil
	})
}

func sniffLocalMediaType(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	// A pipe or device could block or never end.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	mediaType := mime.TypeByExtension(filepath.Ext(path))
	if mediaType == "" {
		file, err := os.Open(resolved)
		if err != nil {
			return "", err
		}
		defer file.Close()
		head := make([]byte, 512)
		n, err := io.ReadFull(file, head)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return "", err
		}
		mediaType = http.DetectContentType(head[:n])
	}
	if !IsChatMediaType(mediaType) {
		return "", fmt.Errorf("%s is %s, not an image, video or audio file", path, mediaType)
	}
	return mediaType, nil
}

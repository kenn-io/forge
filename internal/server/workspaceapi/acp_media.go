package workspaceapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/url"
	"path/filepath"
	"strings"

	"go.kenn.io/kit/fslink"

	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// acpMediaPreviews delivers agent-side media references as browser-readable
// content. Resolve them on the execution host, including saved transcripts,
// without changing the references stored by the ACP owner.
func acpMediaPreviews(data []byte) []byte {
	var frame map[string]jsontext.Value
	if json.Unmarshal(data, &frame) != nil {
		return data
	}
	changed := false
	if raw, ok := frame["messages"]; ok {
		var messages []localruntime.ACPMessage
		if json.Unmarshal(raw, &messages) == nil && acpMessageMediaPreviews(messages) {
			encoded, err := json.Marshal(messages)
			if err == nil {
				frame["messages"] = encoded
				changed = true
			}
		}
	}
	if raw, ok := frame["updates"]; ok {
		updated := acpMediaPreviews(raw)
		if !bytes.Equal(raw, updated) {
			frame["updates"] = updated
			changed = true
		}
	}
	if raw, ok := frame["history"]; ok {
		var history localruntime.ACPHistory
		if json.Unmarshal(raw, &history) == nil && acpMessageMediaPreviews(history.Messages) {
			encoded, err := json.Marshal(history)
			if err == nil {
				frame["history"] = encoded
				changed = true
			}
		}
	}
	if changed {
		if encoded, err := json.Marshal(frame); err == nil {
			return encoded
		}
	}
	return data
}

func acpMessageMediaPreviews(messages []localruntime.ACPMessage) bool {
	changed := false
	for i := range messages {
		if acpContentMediaPreview(messages[i].Content) {
			changed = true
		}
		for j := range messages[i].ToolContent {
			if acpContentMediaPreview(messages[i].ToolContent[j].Content) {
				changed = true
			}
		}
	}
	return changed
}

// acpContentMediaPreview resolves a local file reference into inline media:
// an ACP resource link or the kenn_forge_show_media descriptor, which Claude
// agents can only return as text. ACP has no video block, so video becomes an
// embedded blob resource.
func acpContentMediaPreview(content *localruntime.ACPContent) bool {
	if content == nil {
		return false
	}
	switch content.Type {
	case "resource_link":
		path := content.URI
		if !filepath.IsAbs(path) {
			uri, err := url.Parse(path)
			if err != nil || uri.Scheme != "file" {
				return false
			}
			path = uri.Path
		}
		return acpLocalMediaPreview(content, path)
	case "text":
		var descriptor mcpserver.MediaDescriptor
		if json.Unmarshal([]byte(content.Text), &descriptor) != nil || descriptor.Kind != mcpserver.MediaDescriptorKind || !filepath.IsAbs(descriptor.Path) {
			return false
		}
		// A missing or replaced file still shows where the agent left it.
		*content = localruntime.ACPContent{Type: "resource_link", URI: descriptor.Path, Name: filepath.Base(descriptor.Path), Title: descriptor.Title}
		acpLocalMediaPreview(content, descriptor.Path)
		return true
	default:
		return false
	}
}

func acpLocalMediaPreview(content *localruntime.ACPContent, path string) bool {
	// Accept symlinks, then read only a regular file: a pipe or device could
	// block or never end.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	data, err := fslink.ReadFile(resolved)
	if err != nil {
		return false
	}
	mediaType := previewMediaType(path, data)
	family, _, _ := strings.Cut(mediaType, "/")
	switch family {
	case "image", "audio":
		content.Type = family
	case "video":
		content.Type = "resource"
	default:
		return false
	}
	content.MimeType = mediaType
	content.Data = base64.StdEncoding.EncodeToString(data)
	content.Size = new(len(data))
	return true
}

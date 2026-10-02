package workspaceapi

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/url"
	"path/filepath"
	"strings"

	"go.kenn.io/kit/fslink"

	"go.kenn.io/forge/internal/workspace/localruntime"
)

// acpImagePreviews delivers agent-side image references as browser-readable
// content. Resolve them on the execution host, including saved transcripts,
// without changing the references stored by the ACP owner.
func acpImagePreviews(data []byte) []byte {
	var frame map[string]jsontext.Value
	if json.Unmarshal(data, &frame) != nil {
		return data
	}
	changed := false
	if raw, ok := frame["messages"]; ok {
		var messages []localruntime.ACPMessage
		if json.Unmarshal(raw, &messages) == nil && acpMessageImagePreviews(messages) {
			encoded, err := json.Marshal(messages)
			if err == nil {
				frame["messages"] = encoded
				changed = true
			}
		}
	}
	if raw, ok := frame["history"]; ok {
		var history localruntime.ACPHistory
		if json.Unmarshal(raw, &history) == nil && acpMessageImagePreviews(history.Messages) {
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

func acpMessageImagePreviews(messages []localruntime.ACPMessage) bool {
	changed := false
	for i := range messages {
		if acpContentImagePreview(messages[i].Content) {
			changed = true
		}
		for j := range messages[i].ToolContent {
			if acpContentImagePreview(messages[i].ToolContent[j].Content) {
				changed = true
			}
		}
	}
	return changed
}

func acpContentImagePreview(content *localruntime.ACPContent) bool {
	if content == nil || content.Type != "resource_link" {
		return false
	}
	path := content.URI
	if !filepath.IsAbs(path) {
		uri, err := url.Parse(path)
		if err != nil || uri.Scheme != "file" {
			return false
		}
		path = uri.Path
	}
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
	if !strings.HasPrefix(mediaType, "image/") {
		return false
	}
	content.Type = "image"
	content.MimeType = mediaType
	content.Data = base64.StdEncoding.EncodeToString(data)
	content.Size = new(len(data))
	return true
}

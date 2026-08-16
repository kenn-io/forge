package workspaceapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace"
)

const MaxPastedImageRequestBytes = int64(
	((workspace.MaxPastedImageBytes+2)/3)*4 + 1024,
)

type pastedImageInput struct {
	ID   string `path:"id"`
	Body struct {
		Data string `json:"data"`
	}
}

type pastedImageOutput struct {
	Body struct {
		Path string `json:"path"`
	}
}

func (h *Handler) registerPastedImages(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:  "write-workspace-pasted-image",
		Method:       http.MethodPost,
		Path:         "/workspaces/{id}/pasted-images",
		Hidden:       true,
		MaxBodyBytes: MaxPastedImageRequestBytes,
	}, h.writeWorkspacePastedImage)
}

func (h *Handler) writeWorkspacePastedImage(
	ctx context.Context,
	input *pastedImageInput,
) (*pastedImageOutput, error) {
	decoded, err := base64.StdEncoding.DecodeString(input.Body.Data)
	if err != nil {
		return nil, httpapi.Validation("body.data", "data must be valid base64")
	}
	if len(decoded) > workspace.MaxPastedImageBytes {
		return nil, httpapi.PayloadTooLarge(
			"pasted image exceeds the size limit", workspace.MaxPastedImageBytes,
		)
	}
	path, err := h.workspaces.StorePastedImage(ctx, input.ID, decoded)
	if err != nil {
		switch {
		case errors.Is(err, workspace.ErrPastedImageTooLarge):
			return nil, httpapi.PayloadTooLarge(
				"pasted image exceeds the size limit", workspace.MaxPastedImageBytes,
			)
		case errors.Is(err, workspace.ErrUnsupportedPastedImage):
			return nil, httpapi.Validation(
				"body.data", "data must contain a supported PNG, JPEG, GIF, or WebP image",
			)
		case errors.Is(err, workspace.ErrWorkspaceNotFound):
			return nil, httpapi.NotFound(
				httpapi.CodeWorkspaceNotFound, "workspace not found", nil,
			)
		case errors.Is(err, workspace.ErrWorkspaceInvalidState),
			errors.Is(err, workspace.ErrPastedImagePathConflict):
			return nil, httpapi.Conflict(httpapi.CodeConflict, err.Error(), nil)
		default:
			return nil, err
		}
	}
	output := &pastedImageOutput{}
	output.Body.Path = path
	return output, nil
}

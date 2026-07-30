package workspaceapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/server/httpapi"
)

const (
	maxTerminalClipboardBytes = 1024 * 1024
	terminalClipboardTimeout  = 3 * time.Second
)

type terminalClipboardInput struct {
	Body struct {
		Text string `json:"text"`
	}
}

type terminalClipboardOutput struct {
	Status int `status:"204"`
}

func (h *Handler) registerTerminalClipboard(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "write-terminal-clipboard",
		Method:        http.MethodPost,
		Path:          "/terminal/clipboard",
		DefaultStatus: http.StatusNoContent,
		Hidden:        true,
		// JSON can expand each control byte into a six-byte \u00xx escape.
		MaxBodyBytes: maxTerminalClipboardBytes*6 + 1024,
	}, h.writeTerminalClipboard)
}

func (h *Handler) writeTerminalClipboard(
	ctx context.Context,
	input *terminalClipboardInput,
) (*terminalClipboardOutput, error) {
	if len([]byte(input.Body.Text)) > maxTerminalClipboardBytes {
		return nil, httpapi.PayloadTooLarge(
			"terminal clipboard text exceeds the size limit",
			maxTerminalClipboardBytes,
		)
	}
	if h.clipboard == nil {
		return nil, httpapi.ServiceUnavailable(
			"local clipboard integration is unavailable",
		)
	}

	writeCtx, cancel := context.WithTimeout(
		ctx,
		terminalClipboardTimeout,
	)
	defer cancel()
	if err := h.clipboard.WriteText(writeCtx, input.Body.Text); err != nil {
		return nil, httpapi.ServiceUnavailable(
			"local clipboard integration failed",
		)
	}
	return &terminalClipboardOutput{Status: http.StatusNoContent}, nil
}

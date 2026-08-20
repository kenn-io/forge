package ptyowner

import "go.kenn.io/forge/internal/ptysize"

const (
	RequestAttach = "attach"
	RequestInput  = "input"
	RequestResize = "resize"
	RequestStatus = "status"
	RequestStop   = "stop"

	ResponseOK     = "ok"
	ResponseOutput = "output"
	ResponseExit   = "exit"
	ResponseError  = "error"
)

type Request struct {
	Type        string `json:"type"`
	Token       string `json:"token,omitempty"`
	Cols        int    `json:"cols,omitempty"`
	Rows        int    `json:"rows,omitempty"`
	PixelWidth  int    `json:"pixel_width,omitempty"`
	PixelHeight int    `json:"pixel_height,omitempty"`
	Data        []byte `json:"data,omitempty"`
}

func (r Request) geometry() ptysize.Geometry {
	return ptysize.Geometry{
		Cols:        r.Cols,
		Rows:        r.Rows,
		PixelWidth:  r.PixelWidth,
		PixelHeight: r.PixelHeight,
	}
}

type Response struct {
	Type     string `json:"type"`
	OK       bool   `json:"ok,omitempty"`
	Error    string `json:"error,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Output   []byte `json:"output,omitempty"`
	Title    string `json:"title,omitempty"`
}

type Status struct {
	Output []byte
	Title  string
}

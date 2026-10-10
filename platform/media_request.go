package platform

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DoMediaRequest sends a markdown media request and bounds only the wait for
// response headers. Media bodies stream for as long as a player keeps
// reading, so a whole-request timeout would cut long videos off. Closing the
// returned body cancels the request.
func DoMediaRequest(client *http.Client, req *http.Request, headerTimeout time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	headerWait := time.AfterFunc(headerTimeout, cancel)
	resp, err := client.Do(req.WithContext(ctx))
	if !headerWait.Stop() {
		if err == nil {
			_ = resp.Body.Close()
		}
		cancel()
		return nil, fmt.Errorf("no media response headers within %s: %w", headerTimeout, context.DeadlineExceeded)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

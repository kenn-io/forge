package apitest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// TestBridgeRuntimeAttachmentOutputClosedEmitsExitFrameBeforeDone pins the
// bridge branch for wrappers where PTY EOF reaches the subscriber before
// cmd.Wait marks the session done. That is the race the real ShellDrawer cares
// about, but it is cleaner to drive it with controlled channels than to depend
// on backend-specific PTY behavior in an e2e helper.
func TestBridgeRuntimeAttachmentOutputClosedEmitsExitFrameBeforeDone(t *testing.T) {
	require := require.New(t)
	closedOutput := make(chan []byte)
	close(closedOutput)
	stillWaiting := make(chan struct{})
	exitCode := 7
	attach := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output: closedOutput,
			Done:   stillWaiting,
			Info: func() localruntime.SessionInfo {
				return localruntime.SessionInfo{ExitCode: &exitCode}
			},
			SessionOutputClosed: func() bool { return true },
		},
	)

	bridgeReturn := make(chan bool, 1)
	acceptErr := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				InsecureSkipVerify: true,
			})
			if err != nil {
				acceptErr <- err
				return
			}
			exited := workspaceapi.BridgeRuntimeAttachment(r.Context(), conn, attach)
			bridgeReturn <- exited
			conn.Close(websocket.StatusNormalClosure, "test done")
		},
	))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	typ, data, err := conn.Read(ctx)
	require.NoError(err)
	require.Equal(websocket.MessageText, typ)
	var msg struct {
		Type string `json:"type"`
		Code int    `json:"code"`
	}
	require.NoError(json.Unmarshal(data, &msg))
	require.Equal("exited", msg.Type)
	require.Equal(7, msg.Code)

	select {
	case exited := <-bridgeReturn:
		require.True(exited,
			"bridge must report exited when session output closed")
	case err := <-acceptErr:
		require.NoError(err, "websocket accept failed")
	case <-time.After(2 * time.Second):
		require.Fail("bridge did not return")
	}
}

// TestBridgeRuntimeAttachmentSubscriberDropDoesNotEmitExitFrame
// pins the bridge's branch that distinguishes a subscriber drop from
// a real session exit. broadcast closes a subscriber's Output channel
// when its 64-slot buffer fills (slow client); without this branch
// the bridge would emit "exited" on a healthy shell and auto-close
// the drawer in front of a still-running session.
//
// We exercise the bridge directly with an Attachment whose Output is
// pre-closed and whose SessionOutputClosed reports false — exactly
// the post-broadcast-drop state. Constructing that state via real
// PTY traffic would be timing-fragile (it requires saturating the
// TCP send buffer faster than the bridge can drain it), so this is
// a focused unit test on the bridge's branching logic.
func TestBridgeRuntimeAttachmentSubscriberDropDoesNotEmitExitFrame(t *testing.T) {
	require := require.New(t)
	closedOutput := make(chan []byte)
	close(closedOutput)
	stillRunning := make(chan struct{})
	attach := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output:              closedOutput,
			Done:                stillRunning,
			SessionOutputClosed: func() bool { return false },
		},
	)

	bridgeReturn := make(chan bool, 1)
	acceptErr := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				InsecureSkipVerify: true,
			})
			if err != nil {
				acceptErr <- err
				return
			}
			exited := workspaceapi.BridgeRuntimeAttachment(r.Context(), conn, attach)
			bridgeReturn <- exited
			conn.Close(websocket.StatusNormalClosure, "test done")
		},
	))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(
		context.Background(), 4*time.Second,
	)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	for {
		typ, data, readErr := conn.Read(ctx)
		if readErr != nil {
			break
		}
		if typ == websocket.MessageText {
			require.Failf(
				"unexpected exit frame on subscriber drop",
				"frame: %s", data,
			)
		}
	}

	select {
	case exited := <-bridgeReturn:
		require.False(exited,
			"bridge must report not-exited when only the "+
				"subscriber's Output closed")
	case err := <-acceptErr:
		require.NoError(err, "websocket accept failed")
	case <-time.After(2 * time.Second):
		require.Fail("bridge did not return")
	}
}

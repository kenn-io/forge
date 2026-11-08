package localruntime

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"go.kenn.io/kit/atomicfile"
)

// acpMirrorPollInterval is how often a viewed mirror reads the transcript.
const acpMirrorPollInterval = 250 * time.Millisecond

// ACPMirrorRefusalError is a command a mirrored chat will not carry out while its
// agent runs outside Forge. Sending it again changes nothing.
type ACPMirrorRefusalError string

func (r ACPMirrorRefusalError) Error() string { return string(r) }

// ErrACPMirrorReadOnly refuses input to a mirrored agent Forge cannot type to.
const ErrACPMirrorReadOnly = ACPMirrorRefusalError("this agent runs in a terminal Forge can't type into; use that terminal until it exits")

// ACPMirrorStatus is what the daemon knows about a mirrored agent right now.
type ACPMirrorStatus struct {
	// Running means the agent may still hold its session, so no second process
	// may start on it.
	Running bool
	// Writable means Forge can type into the agent's terminal.
	Writable bool
	Busy     bool
	// TranscriptPath is the transcript the agent last reported, which a hook
	// may supply only after the chat opened.
	TranscriptPath string
}

// ACPMirror names an agent session that runs outside Forge. Its chat shows the
// agent's transcript as the agent writes it and sends input to its terminal.
// Once the agent exits, the chat's first command starts a Forge-run agent on
// the same session, like any dormant chat.
type ACPMirror struct {
	Key            string
	Agent          string
	SessionID      string
	TranscriptPath string
	Status         func() ACPMirrorStatus
	// Deliver sends a person's prompt or stop to the running agent's terminal.
	Deliver func(ACPCommand) error
	// Start starts a Forge-run agent once the session is saved for it to load.
	Start func() (ACPChat, error)
	// Live returns the Forge-run agent another viewer's chat started on the
	// session, or nil while none runs.
	Live func() ACPChat
}

type acpMirrorFollower struct {
	source ACPMirror
	reader acpTranscriptReader
	// readMu serializes reads, which Follow and a save before a start both make.
	readMu sync.Mutex
	// path is the transcript being read; the first reported one is kept, since
	// entry IDs come from offsets in it.
	path   string
	offset int64
	// partial holds a line the agent has not finished writing.
	partial []byte
}

// MirrorACP returns a chat of mirror and reads the transcript written so far.
// Follow keeps it current while someone watches.
func (m *Manager) MirrorACP(mirror ACPMirror) *DormantACP {
	view := &ACP{done: make(chan struct{}), subscribers: make(map[chan struct{}]struct{}), exitCode: -1}
	view.state.Dormant = true
	dormant := &DormantACP{saved: view, started: make(chan struct{})}
	dormant.mirror = &acpMirrorFollower{source: mirror, reader: newACPTranscriptReader(mirror.Agent), path: mirror.TranscriptPath}
	dormant.start = func() (ACPChat, error) {
		if err := m.saveMirroredSession(dormant, mirror); err != nil {
			return nil, err
		}
		return mirror.Start()
	}
	dormant.pollMirror()
	return dormant
}

// saveMirroredSession writes the session file a Forge-run agent loads, keeping
// the transcript the chat already shows and its message IDs.
func (m *Manager) saveMirroredSession(dormant *DormantACP, mirror ACPMirror) error {
	path := m.acpSessionPath(mirror.Key)
	if path == "" {
		return fmt.Errorf("no session path for chat %q", mirror.Key)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create chat session directory: %w", err)
	}
	dormant.pollMirror()
	dormant.saved.mu.Lock()
	// A chat that read no transcript, for want of a reader or a transcript,
	// shows the history the agent replays on load instead.
	saved := acpSavedSession{
		SessionID: mirror.SessionID, LoadSession: true, ImportHistory: len(dormant.saved.state.Messages) == 0,
		State: ACPState{Messages: slices.Clone(dormant.saved.state.Messages)},
	}
	dormant.saved.mu.Unlock()
	data, err := json.Marshal(saved, json.Deterministic(true))
	if err != nil {
		return fmt.Errorf("encode mirrored session: %w", err)
	}
	if err := atomicfile.WriteFile(path, data, atomicfile.WithPerm(0o600)); err != nil {
		return fmt.Errorf("save mirrored session: %w", err)
	}
	return nil
}

// PrepareMirroredACP names the agent a mirrored chat loads its session in. The
// chat never had a Forge-run agent, so no earlier launch recorded one.
func (m *Manager) PrepareMirroredACP(ctx context.Context, targetKey, workspaceID, key, cwd string) error {
	target, err := m.target(targetKey)
	if err != nil {
		return err
	}
	_, err = m.writeACPOwnerConfig(ctx, target, workspaceID, key, cwd, nil)
	return err
}

// Follow keeps a mirror's chat current until ctx ends or its agent starts,
// here or in another viewer's chat.
func (d *DormantACP) Follow(ctx context.Context) {
	ticker := time.NewTicker(acpMirrorPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.started:
			return
		case <-ticker.C:
			if d.attachRunning() != nil {
				return
			}
			d.pollMirror()
		}
	}
}

// attachRunning makes the Forge-run agent another viewer started this chat's
// agent, and returns it, or nil while none runs.
func (d *DormantACP) attachRunning() ACPChat {
	if d.mirror.source.Live == nil {
		return nil
	}
	running := d.mirror.source.Live()
	if running == nil {
		return nil
	}
	d.startMu.Lock()
	defer d.startMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.live == nil {
		d.live = running
		close(d.started)
	}
	return d.live
}

// pollMirror applies what the agent wrote since the last read and its status.
func (d *DormantACP) pollMirror() {
	status := d.mirror.source.Status()
	d.pollMirrorTranscript(status.TranscriptPath)
	readOnly := status.Running && !status.Writable
	busy := status.Running && status.Busy
	d.saved.mu.Lock()
	defer d.saved.mu.Unlock()
	if d.saved.state.Busy != busy || d.saved.state.ExternalReadOnly != readOnly {
		d.saved.state.Busy = busy
		d.saved.state.ExternalReadOnly = readOnly
		d.saved.changedLocked()
	}
}

func (d *DormantACP) pollMirrorTranscript(reported string) {
	d.mirror.readMu.Lock()
	defer d.mirror.readMu.Unlock()
	if d.mirror.path == "" {
		d.mirror.path = reported
	}
	lines := d.mirror.read()
	if len(lines) == 0 {
		return
	}
	d.saved.mu.Lock()
	defer d.saved.mu.Unlock()
	transcript := acpMirrorTranscript{view: d.saved}
	for _, line := range lines {
		d.mirror.reader.apply(line.offset, line.data, &transcript)
	}
	d.saved.changedLocked()
}

type acpTranscriptLine struct {
	offset int64
	data   []byte
}

// read returns the complete lines the agent appended since the last read. A
// transcript that is missing, or not a regular file, reads as nothing yet.
func (f *acpMirrorFollower) read() []acpTranscriptLine {
	if f.reader == nil || f.path == "" {
		return nil
	}
	info, err := os.Stat(f.path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == f.offset {
		return nil
	}
	file, err := os.Open(f.path)
	if err != nil {
		return nil
	}
	defer file.Close()
	if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil
	}
	start := f.offset - int64(len(f.partial))
	f.offset += int64(len(data))
	data = append(f.partial, data...)
	var lines []acpTranscriptLine
	for {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			break
		}
		if end > 0 {
			lines = append(lines, acpTranscriptLine{offset: start, data: data[:end]})
		}
		start += int64(end) + 1
		data = data[end+1:]
	}
	f.partial = slices.Clone(data)
	return lines
}

// command handles a command while the agent runs outside Forge.
func (f *acpMirrorFollower) command(command ACPCommand, status ACPMirrorStatus) error {
	switch command.Type {
	case "prompt", "cancel":
	case "dismiss_error":
		return nil
	default:
		return ACPMirrorRefusalError(fmt.Sprintf("%q is not available while this agent runs outside Forge", command.Type))
	}
	if !status.Writable {
		return ErrACPMirrorReadOnly
	}
	if command.SenderKind() != ACPSenderUser {
		return ACPMirrorRefusalError("a coordinator cannot send input to an agent running outside Forge")
	}
	if len(command.Images) > 0 {
		return ACPMirrorRefusalError("images can't be sent to an agent running outside Forge")
	}
	return f.source.Deliver(command)
}

// acpMirrorTranscript is the view a transcript reader adds entries to. The
// caller holds the view's lock.
type acpMirrorTranscript struct {
	view *ACP
	// createdAt is the timestamp of the record being applied.
	createdAt string
}

func (t *acpMirrorTranscript) add(id string, message ACPMessage) {
	message.ID = id
	message.CreatedAt = t.createdAt
	if message.Role == "user" {
		message.Sender = &ACPSender{Kind: ACPSenderUser}
		message.Turn = &ACPTurn{}
	}
	t.view.insertMessageLocked(len(t.view.state.Messages), message)
}

func (t *acpMirrorTranscript) finishTool(callID string, failed bool, output string) {
	for i, item := range slices.Backward(t.view.state.Messages) {
		if item.Role != "tool" || item.ToolCallID != callID {
			continue
		}
		message := &t.view.state.Messages[i]
		message.Status = "completed"
		if failed {
			message.Status = "failed"
		}
		if output != "" {
			message.ToolContent = []ACPToolContent{{Type: "content", Content: &ACPContent{Type: "text", Text: output}}}
		}
		t.view.publishMessageLocked(i)
		return
	}
}

// acpTranscriptReader turns one agent family's transcript records into chat
// entries. offset names the record, so its entries keep their IDs across
// rereads and into a later Forge-run agent's saved session.
type acpTranscriptReader interface {
	apply(offset int64, record []byte, transcript *acpMirrorTranscript)
}

// newACPTranscriptReader returns the reader for an agent family, or nil for one
// whose transcript Forge cannot read: its chat stays empty until the agent
// exits and a Forge-run agent loads the session.
func newACPTranscriptReader(agent string) acpTranscriptReader {
	switch agent {
	case "claude":
		return claudeTranscriptReader{}
	case "codex":
		return codexTranscriptReader{}
	}
	return nil
}

func acpTranscriptEntryID(offset int64, block int) string {
	return fmt.Sprintf("transcript-%d-%d", offset, block)
}

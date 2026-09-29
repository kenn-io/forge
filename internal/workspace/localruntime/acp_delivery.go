package localruntime

import (
	"regexp"
	"strings"
	"time"
)

// Streamed assistant text is published in finished markdown blocks rather
// than token by token. A block whose shape can still change as more text
// arrives (an open paragraph, list item, or code fence) stays held.

// maxHeldTextBytes delivers held text as is to cap memory during a turn that
// never reaches a block boundary.
const maxHeldTextBytes = 24_000

// minTextDeliveryInterval keeps blocks that finish shortly after a delivery
// held so they land together, and a fast agent does not repaint the message
// several times a second.
const minTextDeliveryInterval = 400 * time.Millisecond

var (
	acpFencePattern     = regexp.MustCompile("^( {0,3})(`{3,}|~{3,})")
	acpBlankLinePattern = regexp.MustCompile(`^[ \t]*$`)
	acpListItemPattern  = regexp.MustCompile(`^ {0,3}(?:[-+*]|\d{1,9}[.)])[ \t]`)
)

// deliverableTextPrefix returns how much of text ends at a block boundary: a
// blank line, the line after a closing code fence, or the start of a list
// item, none of them inside an open fence.
func deliverableTextPrefix(text string) int {
	var fenceMarker string
	fenceIndent, fenceOpen := 0, false
	boundary, lineStart := -1, 0
	for {
		newline := strings.IndexByte(text[lineStart:], '\n')
		end := len(text)
		if newline >= 0 {
			newline += lineStart
			end = newline
		}
		line := strings.TrimRight(text[lineStart:end], " \t\r")
		if !fenceOpen && lineStart > 0 && acpListItemPattern.MatchString(line) {
			boundary = lineStart
		}
		if newline < 0 {
			break
		}
		if fence := acpFencePattern.FindStringSubmatch(line); fence != nil {
			indent, marker := len(fence[1]), fence[2]
			switch {
			case !fenceOpen:
				fenceOpen, fenceMarker, fenceIndent = true, marker, indent
			case marker[0] == fenceMarker[0] && len(marker) >= len(fenceMarker) &&
				indent <= fenceIndent+3 && len(line) == indent+len(marker):
				fenceOpen = false
				boundary = newline + 1
			}
		} else if !fenceOpen && lineStart > 0 && acpBlankLinePattern.MatchString(line) {
			boundary = newline + 1
		}
		lineStart = newline + 1
	}
	return max(boundary, 0)
}

// holdTextLocked records streamed text appended to the last assistant
// message. It is published only when deliverTextLocked releases it.
func (a *ACP) holdTextLocked(startedMessage bool, text string) {
	if startedMessage {
		a.releaseHeldTextLocked()
		a.heldSeq++
	}
	a.heldBytes += len(text)
	a.deliverTextLocked()
}

// deliverTextLocked publishes held text up to its last block boundary once the
// pacing window has passed, or everything when the hold exceeds the cap.
func (a *ACP) deliverTextLocked() {
	pending := a.heldTextLocked()
	if pending == "" {
		return
	}
	if len(pending) > maxHeldTextBytes {
		a.releaseHeldTextLocked()
		a.publishProgressLocked()
		return
	}
	ready := pending[:deliverableTextPrefix(pending)]
	wait := time.Until(a.lastTextDelivery.Add(minTextDeliveryInterval))
	if strings.TrimSpace(ready) == "" || wait > 0 {
		if wait <= 0 {
			wait = minTextDeliveryInterval
		}
		a.scheduleTextDeliveryLocked(wait)
		return
	}
	a.heldBytes -= len(ready)
	a.lastTextDelivery = time.Now()
	a.publishProgressLocked()
	if a.heldBytes > 0 {
		a.scheduleTextDeliveryLocked(minTextDeliveryInterval)
	}
}

// scheduleTextDeliveryLocked looks again once the pacing window closes. An
// agent that went quiet in the middle of a block gets that text shown instead
// of held indefinitely.
func (a *ACP) scheduleTextDeliveryLocked(wait time.Duration) {
	if a.textDelivery != nil || a.heldBytes == 0 {
		return
	}
	seq, received := a.heldSeq, a.lastMessageBytesLocked()
	a.textDelivery = time.AfterFunc(wait, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.textDelivery = nil
		if a.heldBytes == 0 {
			return
		}
		if a.heldSeq == seq && a.lastMessageBytesLocked() == received {
			a.releaseHeldTextLocked()
			a.publishProgressLocked()
			return
		}
		a.deliverTextLocked()
	})
}

// releaseHeldTextLocked makes every received character visible. Callers
// publish afterwards.
func (a *ACP) releaseHeldTextLocked() {
	if a.textDelivery != nil {
		a.textDelivery.Stop()
		a.textDelivery = nil
	}
	if a.heldBytes == 0 {
		return
	}
	a.heldBytes = 0
	a.lastTextDelivery = time.Now()
}

func (a *ACP) publishProgressLocked() {
	a.trimStateLocked()
	_ = a.persistLocked()
	a.changedLocked()
}

func (a *ACP) lastMessageBytesLocked() int {
	if len(a.state.Messages) == 0 {
		return 0
	}
	return len(a.state.Messages[len(a.state.Messages)-1].Text)
}

// heldTextLocked is the unpublished suffix of the last assistant message.
func (a *ACP) heldTextLocked() string {
	if a.heldBytes == 0 || len(a.state.Messages) == 0 {
		a.heldBytes = 0
		return ""
	}
	text := a.state.Messages[len(a.state.Messages)-1].Text
	// History trimming can shorten the held message itself.
	a.heldBytes = min(a.heldBytes, len(text))
	return text[len(text)-a.heldBytes:]
}

// publishedStateLocked is the state clients may see: held text is cut from
// the last message, and a message that is entirely held is left out.
func (a *ACP) publishedStateLocked() ACPState {
	state := a.state
	held := len(a.heldTextLocked())
	if held == 0 {
		return state
	}
	last := len(state.Messages) - 1
	state.Messages = append([]ACPMessage(nil), state.Messages...)
	message := state.Messages[last]
	message.Text = message.Text[:len(message.Text)-held]
	if message.Text == "" {
		state.Messages = state.Messages[:last]
	} else {
		state.Messages[last] = message
	}
	return state
}

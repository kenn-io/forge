package localruntime

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"go.kenn.io/kit/atomicfile"
)

// A saved chat is a snapshot (session.json) and a journal of the changes made
// since it (session.journal). Each save appends one journal record with the
// transcript entries whose version changed and, when it changed, the rest of
// the chat state, so a save costs what changed rather than the whole
// transcript. A chat that read large tool output used to rewrite tens of
// megabytes on every tool update and saturate the disk for the whole host.
//
// Once the journal outgrows the snapshot, the next save writes a new snapshot
// with a new Base and removes the journal, which keeps the bytes written to
// about twice the bytes that changed. Records carry the Base of the snapshot
// they extend, so a crash between writing a snapshot and removing the journal
// leaves records the loader skips.

// acpJournalMinCompactBytes keeps small chats from writing a new snapshot on
// nearly every save.
const acpJournalMinCompactBytes = 1 << 20

type acpJournalRecord struct {
	Base        string
	SessionID   string
	LoadSession bool
	// State is the chat state without messages, present when it changed.
	State    *ACPState `json:",omitzero"`
	Messages []ACPMessage
}

func acpJournalPath(recordPath string) string {
	return strings.TrimSuffix(recordPath, ".json") + ".journal"
}

// removeACPSaved removes a chat's saved snapshot and journal.
func removeACPSaved(recordPath string) error {
	return errors.Join(removeIfExists(recordPath), removeIfExists(acpJournalPath(recordPath)))
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// readACPSaved reads a saved snapshot and applies its journal.
func readACPSaved(recordPath string) (acpSavedSession, error) {
	var saved acpSavedSession
	data, err := os.ReadFile(recordPath)
	if err != nil {
		return saved, err
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return saved, err
	}
	journal, err := os.ReadFile(acpJournalPath(recordPath))
	if errors.Is(err, os.ErrNotExist) {
		return saved, nil
	}
	if err != nil {
		return saved, err
	}
	applyACPJournal(&saved, journal)
	return saved, nil
}

// applyACPJournal applies the records that extend saved's snapshot. It stops
// at the first record it cannot read, which a crash during an append leaves
// at the end.
func applyACPJournal(saved *acpSavedSession, journal []byte) {
	if saved.Base == "" {
		return
	}
	messages := saved.State.Messages
	index := make(map[string]int, len(messages))
	for i, message := range messages {
		index[message.ID] = i
	}
	changed := false
	for line := range bytes.Lines(journal) {
		var record acpJournalRecord
		if !bytes.HasSuffix(line, []byte("\n")) || json.Unmarshal(line, &record) != nil {
			break
		}
		if record.Base != saved.Base {
			continue
		}
		saved.SessionID, saved.LoadSession = record.SessionID, record.LoadSession
		if record.State != nil {
			saved.State = *record.State
		}
		for _, message := range record.Messages {
			if i, ok := index[message.ID]; ok {
				messages[i] = message
				continue
			}
			index[message.ID] = len(messages)
			messages = append(messages, message)
		}
		changed = true
	}
	if changed {
		messages = orderACPMessages(messages)
	}
	saved.State.Messages = messages
}

// orderACPMessages puts transcript entries in the order their PreviousID
// links give. Entries can be inserted before others, so journal order is not
// transcript order. Entries the links do not reach keep their relative order
// at the end rather than being lost.
func orderACPMessages(messages []ACPMessage) []ACPMessage {
	next := make(map[string]int, len(messages))
	head := -1
	for i, message := range messages {
		if message.PreviousID == "" {
			if head < 0 {
				head = i
			}
			continue
		}
		next[message.PreviousID] = i
	}
	ordered := make([]ACPMessage, 0, len(messages))
	placed := make([]bool, len(messages))
	for i := head; i >= 0 && !placed[i]; {
		ordered = append(ordered, messages[i])
		placed[i] = true
		following, ok := next[messages[i].ID]
		if !ok {
			break
		}
		i = following
	}
	for i, message := range messages {
		if !placed[i] {
			ordered = append(ordered, message)
		}
	}
	return ordered
}

// persistLocked saves the chat: it appends what changed since the last save
// to the journal, or writes a new snapshot when this owner has not written
// one yet or the journal has outgrown it.
func (a *ACP) persistLocked() error {
	if a.recordPath == "" {
		return nil
	}
	a.initializeMessagesLocked()
	metadata := a.state
	metadata.Messages = nil
	encoded, err := json.Marshal(acpJournalRecord{SessionID: a.sessionID, LoadSession: a.loadSession, State: &metadata}, json.Deterministic(true))
	if err != nil {
		return err
	}
	metadataSum := sha256.Sum256(encoded)
	if a.savedBase == "" || a.journalBytes >= max(a.snapshotBytes, acpJournalMinCompactBytes) {
		return a.writeSnapshotLocked(metadataSum)
	}
	record := acpJournalRecord{Base: a.savedBase, SessionID: a.sessionID, LoadSession: a.loadSession}
	if metadataSum != a.savedMetadata {
		record.State = &metadata
	}
	record.Messages = a.changedMessagesLocked(a.savedCursor)
	if record.State == nil && len(record.Messages) == 0 {
		return nil
	}
	line, err := json.Marshal(record, json.Deterministic(true))
	if err != nil {
		return err
	}
	if err := appendSynced(acpJournalPath(a.recordPath), append(line, '\n'), a.journalBytes == 0); err != nil {
		// A partial record would hide every record after it, so the next
		// save writes a full snapshot instead of appending.
		a.savedBase = ""
		return err
	}
	a.journalBytes += len(line) + 1
	a.savedCursor, a.savedMetadata = a.messageCursor, metadataSum
	return nil
}

func (a *ACP) writeSnapshotLocked(metadataSum [32]byte) error {
	base := rand.Text()
	data, err := json.Marshal(acpSavedSession{SessionID: a.sessionID, State: a.state, LoadSession: a.loadSession, Base: base}, json.Deterministic(true))
	if err != nil {
		return err
	}
	err = atomicfile.WriteFile(a.recordPath, data, atomicfile.WithPerm(0o600))
	if err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return err
	}
	a.savedBase, a.snapshotBytes, a.journalBytes = base, len(data), 0
	a.savedCursor, a.savedMetadata = a.messageCursor, metadataSum
	// The save is complete once the snapshot is published. Removing the old
	// journal is cleanup: records it keeps name the old Base and are skipped.
	if err := removeIfExists(acpJournalPath(a.recordPath)); err != nil {
		slog.Warn("remove replaced ACP chat journal", "path", acpJournalPath(a.recordPath), "err", err)
	}
	return nil
}

// changedMessagesLocked returns the entries published after cursor, plus the
// last entry while it holds streamed text not yet published.
func (a *ACP) changedMessagesLocked(cursor uint64) []ACPMessage {
	var messages []ACPMessage
	held := -1
	if a.heldBytes > 0 && len(a.state.Messages) > 0 {
		held = len(a.state.Messages) - 1
	}
	for entry := a.firstChangeAfterLocked(cursor); entry != nil; entry = entry.Next() {
		index := entry.Value.(*acpMessagePosition).index
		if a.state.Messages[index].Version <= cursor {
			continue
		}
		messages = append(messages, a.state.Messages[index])
		if index == held {
			held = -1
		}
	}
	if held >= 0 {
		messages = append(messages, a.state.Messages[held])
	}
	return messages
}

// appendSynced appends data and syncs it. The first append after a snapshot
// may create the file, and its directory entry is durable only once the
// directory is synced too.
func appendSynced(path string, data []byte, creating bool) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if creating {
		return atomicfile.SyncDir(filepath.Dir(path))
	}
	return nil
}

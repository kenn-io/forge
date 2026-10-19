package localruntime

import (
	"cmp"
	"container/list"
	"crypto/rand"
	"encoding/json/v2"
	"slices"
)

// Each entry appears once, at its latest publication. Updating it moves that
// entry to the back without retaining earlier copies of potentially large output.
type acpMessagePosition struct{ index int }

func (a *ACP) initializeMessagesLocked() {
	if a.messageIndex != nil {
		return
	}
	a.messageIndex = make(map[string]*list.Element)
	for i := range a.state.Messages {
		a.messageCursor = max(a.messageCursor, a.state.Messages[i].Version)
	}
	order := make([]int, len(a.state.Messages))
	for i := range a.state.Messages {
		message := &a.state.Messages[i]
		if message.ID == "" {
			message.ID = rand.Text()
		}
		if i > 0 {
			message.PreviousID = a.state.Messages[i-1].ID
		} else {
			message.PreviousID = ""
		}
		if message.Version == 0 {
			a.messageCursor++
			message.Version = a.messageCursor
		}
		order[i] = i
	}
	slices.SortFunc(order, func(i, j int) int { return cmp.Compare(a.state.Messages[i].Version, a.state.Messages[j].Version) })
	for _, i := range order {
		a.messageIndex[a.state.Messages[i].ID] = a.messageChanges.PushBack(&acpMessagePosition{index: i})
	}
}

func (a *ACP) publishMessageLocked(index int) {
	a.initializeMessagesLocked()
	a.messageCursor++
	message := &a.state.Messages[index]
	message.Version = a.messageCursor
	a.messageChanges.MoveToBack(a.messageIndex[message.ID])
}

func (a *ACP) insertMessageLocked(index int, message ACPMessage) {
	a.initializeMessagesLocked()
	message.ID = rand.Text()
	if index > 0 {
		message.PreviousID = a.state.Messages[index-1].ID
	}
	a.state.Messages = slices.Insert(a.state.Messages, index, message)
	a.messageIndex[message.ID] = a.messageChanges.PushBack(&acpMessagePosition{index: index})
	for i := index + 1; i < len(a.state.Messages); i++ {
		entry := a.messageIndex[a.state.Messages[i].ID]
		if entry == nil {
			panic("ACP transcript entry is missing its change index")
		}
		entry.Value.(*acpMessagePosition).index = i
	}
	a.publishMessageLocked(index)
	if index+1 < len(a.state.Messages) {
		a.state.Messages[index+1].PreviousID = message.ID
		a.publishMessageLocked(index + 1)
	}
}

// ACPChanges carries latest versions, not positional replacements. More keeps
// a client reading even when the agent has exited while this batch was pending.
type ACPChanges struct {
	ACPState
	More bool `json:"more"`
}

// ACPBatch owns its encoded frame so RPC and websocket writers never access
// mutable message or metadata slices after the owner lock is released.
type ACPBatch struct {
	Data          []byte
	MessageCursor uint64
	More          bool
}

func encodeACPBatch(state ACPState, more bool) (ACPBatch, error) {
	data, err := json.Marshal(struct {
		Updates ACPChanges `json:"updates"`
	}{ACPChanges{ACPState: state, More: more}})
	return ACPBatch{Data: data, MessageCursor: state.MessageCursor, More: more}, err
}

func (a *ACP) Updates(after uint64) (ACPBatch, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.publishedMetadataLocked()
	state.Messages = nil
	state.MessageOffset = 0
	state.MessageCursor = after
	// Walking backwards stops at unchanged history. A coalesced old tool edit
	// is at the tail, so finding it does not scan the rest of the transcript.
	first := a.messageChanges.Back()
	for first != nil && a.state.Messages[first.Value.(*acpMessagePosition).index].Version > after {
		previous := first.Prev()
		if previous == nil || a.state.Messages[previous.Value.(*acpMessagePosition).index].Version <= after {
			break
		}
		first = previous
	}
	bytes := 2
	for entry := first; entry != nil; entry = entry.Next() {
		index := entry.Value.(*acpMessagePosition).index
		version := a.state.Messages[index].Version
		if version <= after {
			continue
		}
		if index >= state.MessageCount {
			state.MessageCursor = version
			continue
		}
		message := a.publishedMessageLocked(index)
		data, err := json.Marshal(message)
		if err != nil {
			return ACPBatch{}, err
		}
		if len(state.Messages) > 0 && bytes+len(data)+1 > acpMessageBatchBytes {
			return encodeACPBatch(state, true)
		}
		bytes += len(data) + 1
		state.Messages = append(state.Messages, message)
		state.MessageCursor = version
	}
	state.MessageCursor = a.messageCursor
	return encodeACPBatch(state, false)
}

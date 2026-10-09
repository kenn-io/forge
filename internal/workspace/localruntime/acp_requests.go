package localruntime

import (
	"encoding/json/v2"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// acpAnsweredLimit bounds the answered-request ledger.
const acpAnsweredLimit = 100

// ErrACPStaleRequest rejects an answer to a request an earlier agent process
// made. The owner RPC carries it as a code; attachments restore the sentinel.
var ErrACPStaleRequest = errors.New("request belongs to an earlier agent process")

var errACPAlreadyAnswered = errors.New("request was already answered")

// ACPAnsweredRequest records one answered permission or elicitation.
type ACPAnsweredRequest struct {
	ID string `json:"id"`
	// Answer is the permission option ID, or the elicitation action followed
	// by its content serialized with json.Deterministic, so two answers with
	// the same action but different field values differ.
	Answer   string `json:"answer"`
	AnswerAt string `json:"answeredAt"`
}

// newRequestIDLocked returns a request ID unique to this owner process. The
// prefix is "p" for permissions and "e" for elicitations.
func (a *ACP) newRequestIDLocked(prefix string) string {
	a.nextRequest++
	return a.state.RuntimeGeneration + "-" + prefix + strconv.Itoa(a.nextRequest)
}

// recordAnswerLocked remembers an answered request, dropping the oldest entry
// beyond the ledger limit.
func (a *ACP) recordAnswerLocked(id, answer string) {
	a.state.Answered = append(a.state.Answered, ACPAnsweredRequest{
		ID: id, Answer: answer, AnswerAt: time.Now().UTC().Format(time.RFC3339),
	})
	if excess := len(a.state.Answered) - acpAnsweredLimit; excess > 0 {
		a.state.Answered = slices.Delete(a.state.Answered, 0, excess)
	}
}

// answeredLocked classifies an answer to a request that is not pending. It
// reports true for a repeat of the recorded answer, an error for a different
// answer or a request from another agent process, and false with no error when
// the request is simply no longer pending.
func (a *ACP) answeredLocked(id, answer string) (bool, error) {
	for _, entry := range a.state.Answered {
		if entry.ID != id {
			continue
		}
		if entry.Answer == answer {
			return true, nil
		}
		return false, errACPAlreadyAnswered
	}
	if !strings.HasPrefix(id, a.state.RuntimeGeneration+"-") {
		return false, ErrACPStaleRequest
	}
	return false, nil
}

// elicitationAnswerKey identifies an elicitation answer. Content keys are
// sorted so equal field values compare equal.
func elicitationAnswerKey(action string, content map[string]any) (string, error) {
	if action != "accept" {
		return action, nil
	}
	data, err := json.Marshal(content, json.Deterministic(true))
	if err != nil {
		return "", err
	}
	return action + string(data), nil
}

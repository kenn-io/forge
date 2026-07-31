package localruntime

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTerminalInputModeStateObserve(t *testing.T) {
	tests := []struct {
		name           string
		baselineChunks []string
		currentChunks  []string
		want           string
	}{
		{
			name:          "tracks effective input modes",
			currentChunks: []string{"\x1b[?1;9;1000;1002;1003;1004;1005;1006;1015;1016;2004h"},
			want:          "\x1b[?1;1003;1004;1005;1015;1016;2004h",
		},
		{
			name:           "emits resets before sets",
			baselineChunks: []string{"\x1b[?1003;1016h"},
			currentChunks:  []string{"\x1b[?1000;1006h"},
			want:           "\x1b[?1003;1016l\x1b[?1000;1006h",
		},
		{
			name:           "latest observation wins",
			baselineChunks: []string{"\x1b[?2004h"},
			currentChunks:  []string{"\x1b[?2004h", "\x1b[?2004l"},
			want:           "\x1b[?2004l",
		},
		{
			name:          "observed disabled modes reset an unobserved baseline",
			currentChunks: []string{"\x1b[?1004;2004h", "\x1b[?1004;2004l"},
			want:          "\x1b[?1004;2004l",
		},
		{
			name:          "observed disabled mouse groups use canonical resets",
			currentChunks: []string{"\x1b[?1003;1016h", "\x1b[?1000;1006l"},
			want:          "\x1b[?1000;1006l",
		},
		{
			name:          "ignores unrelated and alternate screen modes",
			currentChunks: []string{"\x1b[?25;1049h"},
			want:          "",
		},
		{
			name:          "extracts tracked modes from a mixed sequence",
			currentChunks: []string{"\x1b[?1049;1000;1006h"},
			want:          "\x1b[?1000;1006h",
		},
		{
			name:           "does not duplicate retained focus mode",
			baselineChunks: []string{"\x1b[?1004h"},
			currentChunks:  []string{"\x1b[?1004h"},
			want:           "",
		},
		{
			name:          "last mouse protocol set wins",
			currentChunks: []string{"\x1b[?1003h", "\x1b[?1000h"},
			want:          "\x1b[?1000h",
		},
		{
			name:           "resetting an inactive mouse protocol disables tracking",
			baselineChunks: []string{"\x1b[?1002h"},
			currentChunks:  []string{"\x1b[?1002h", "\x1b[?1000l"},
			want:           "\x1b[?1002l",
		},
		{
			name:          "last supported mouse encoding set wins",
			currentChunks: []string{"\x1b[?1016h", "\x1b[?1006h"},
			want:          "\x1b[?1006h",
		},
		{
			name:          "ignored urxvt encoding after SGR preserves both observations",
			currentChunks: []string{"\x1b[?1006h", "\x1b[?1015h"},
			want:          "\x1b[?1006;1015h",
		},
		{
			name:          "ignored UTF-8 encoding before SGR pixels preserves both observations",
			currentChunks: []string{"\x1b[?1005h", "\x1b[?1016h"},
			want:          "\x1b[?1005;1016h",
		},
		{
			name:           "truncated replay restores SGR after ignored urxvt encoding",
			baselineChunks: []string{"\x1b[?1015h"},
			currentChunks:  []string{"\x1b[?1006h", "\x1b[?1015h"},
			want:           "\x1b[?1006h",
		},
		{
			name:           "truncated replay preserves SGR before ignored urxvt encoding",
			baselineChunks: []string{"\x1b[?1006h"},
			currentChunks:  []string{"\x1b[?1006h", "\x1b[?1015h"},
			want:           "\x1b[?1015h",
		},
		{
			name:           "truncated replay restores SGR pixels after ignored UTF-8 encoding",
			baselineChunks: []string{"\x1b[?1005h"},
			currentChunks:  []string{"\x1b[?1016h", "\x1b[?1005h"},
			want:           "\x1b[?1016h",
		},
		{
			name: "private mode save and restore remain ignored",
			currentChunks: []string{
				"\x1b[?1;1000;1004;2004h",
				"\x1b[?1;1000;1004;2004s",
				"\x1b[?1;1000;1004;2004l",
				"\x1b[?1;1000;1004;2004r",
			},
			want: "\x1b[?1;1000;1004;2004l",
		},
		{
			name:           "full reset clears all effective input modes",
			baselineChunks: []string{"\x1b[?1;1003;1004;1005;1015;1016;2004h"},
			currentChunks:  []string{"\x1b[?1;1003;1004;1005;1015;1016;2004h", "\x1bc"},
			want:           "\x1b[?1;1003;1004;1005;1015;1016;2004l",
		},
		{
			name:           "soft reset clears core input modes only",
			baselineChunks: []string{"\x1b[?1;1000;1004;1006;2004h"},
			currentChunks:  []string{"\x1b[?1;1000;1004;1006;2004h", "\x1b[!p"},
			want:           "\x1b[?1;1004;2004l",
		},
		{
			name:          "accepts UTF-8 encoded C1 CSI split across chunks",
			currentChunks: []string{"\xc2", "\x9b?1h"},
			want:          "\x1b[?1h",
		},
		{
			name:          "ignores raw C1 CSI",
			currentChunks: []string{"\x9b?1h"},
			want:          "",
		},
		{
			name:          "raw C1 byte does not interrupt pending CSI",
			currentChunks: []string{"\x1b[?2004", "\x9b", "h"},
			want:          "\x1b[?2004h",
		},
		{
			name:          "ignores C1-valued continuation in Unicode output",
			currentChunks: []string{"\xd8\x9b?1004h"},
			want:          "",
		},
		{
			name:          "ignores split C1-valued continuation in Unicode output",
			currentChunks: []string{"\xe2\x80", "\x9b?2004h"},
			want:          "",
		},
		{
			name:          "split Unicode interrupts escape candidate",
			currentChunks: []string{"\x1b", "\xe2", "\x98", "\x83", "c"},
			want:          "",
		},
		{
			name:          "split Unicode interrupts CSI candidate",
			currentChunks: []string{"\x1b[?2004", "\xe2", "\x98", "\x83", "h"},
			want:          "",
		},
		{
			name:          "discarded BOM preserves CSI candidate",
			currentChunks: []string{"\x1b[?2004", "\xef", "\xbb", "\xbf", "h"},
			want:          "\x1b[?2004h",
		},
		{
			name:          "discarded overlong scalar preserves CSI candidate",
			currentChunks: []string{"\x1b[?2004", "\xe0", "\x80", "\x80", "h"},
			want:          "\x1b[?2004h",
		},
		{
			name:          "discarded surrogate preserves CSI candidate",
			currentChunks: []string{"\x1b[?2004", "\xed", "\xa0", "\x80", "h"},
			want:          "\x1b[?2004h",
		},
		{
			name:          "discarded out of range scalar preserves CSI candidate",
			currentChunks: []string{"\x1b[?2004", "\xf4", "\x90", "\x80", "\x80", "h"},
			want:          "\x1b[?2004h",
		},
		{
			name:          "ignores executable controls inside CSI",
			currentChunks: []string{"\x1b[?\x001\x7fh"},
			want:          "\x1b[?1h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var baseline, current terminalInputModeState
			for _, chunk := range tt.baselineChunks {
				baseline.observe([]byte(chunk))
			}
			for _, chunk := range tt.currentChunks {
				current.observe([]byte(chunk))
			}
			assert.Equal(t, tt.want, string(current.appendTransitions(nil, baseline)))
		})
	}
}

func TestTerminalInputModeStateAppendTransitionsPreservesPrefix(t *testing.T) {
	var state terminalInputModeState
	state.observe([]byte("\x1b[?1000h"))
	prefix := []byte("screen")
	original := append([]byte(nil), prefix...)

	got := state.appendTransitions(prefix, terminalInputModeState{})

	assert := assert.New(t)
	assert.Equal("screen\x1b[?1000h", string(got))
	assert.Equal(original, prefix)
}

func TestTerminalInputModeStateHandlesEverySplit(t *testing.T) {
	sequence := "\x1b[?1;1000;1002;1004;1006;2004h"
	want := "\x1b[?1;1002;1004;1006;2004h"
	for split := 1; split < len(sequence); split++ {
		t.Run(strconv.Itoa(split), func(t *testing.T) {
			var state terminalInputModeState
			state.observe([]byte(sequence[:split]))
			state.observe([]byte(sequence[split:]))
			assert.Equal(
				t,
				want,
				string(state.appendTransitions(nil, terminalInputModeState{})),
			)
		})
	}
}

func TestTerminalInputModeStateRecoversAfterOverlongSequence(t *testing.T) {
	var state terminalInputModeState
	state.observe([]byte("\x1b[?" + strings.Repeat("1", maxTerminalInputModeSequence)))
	state.observe([]byte("\x1b[?2004h"))

	assert.Equal(
		t,
		"\x1b[?2004h",
		string(state.appendTransitions(nil, terminalInputModeState{})),
	)
}

func TestTerminalInputModeStateHandlesSplitResetSequences(t *testing.T) {
	tests := []struct {
		name     string
		sequence string
		want     string
	}{
		{
			name:     "full reset",
			sequence: "\x1bc",
			want:     "\x1b[?1;1000;1004;1005;1006;1015;2004l",
		},
		{
			name:     "soft reset",
			sequence: "\x1b[!p",
			want:     "\x1b[?1;1004;2004l",
		},
	}

	for _, tt := range tests {
		for split := 1; split < len(tt.sequence); split++ {
			t.Run(tt.name+"/"+strconv.Itoa(split), func(t *testing.T) {
				var baseline, current terminalInputModeState
				baseline.observe([]byte("\x1b[?1;1000;1004;1006;2004h"))
				current.observe([]byte("\x1b[?1;1000;1004;1006;2004h"))
				current.observe([]byte(tt.sequence[:split]))
				current.observe([]byte(tt.sequence[split:]))
				assert.Equal(t, tt.want, string(current.appendTransitions(nil, baseline)))
			})
		}
	}
}

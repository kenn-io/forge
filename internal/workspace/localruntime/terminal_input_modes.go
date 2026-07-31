package localruntime

import (
	"strconv"
	"unicode/utf8"
)

const maxTerminalInputModeSequence = 128

const (
	terminalMouseProtocolResetMode = 1000
	terminalMouseEncodingResetMode = 1006
)

var trackedTerminalInputModes = [...]int{
	1,
	9,
	1000,
	1002,
	1003,
	1004,
	1005,
	1006,
	1015,
	1016,
	2004,
}

var trackedTerminalInputModeSet = map[int]struct{}{
	1:    {},
	9:    {},
	1000: {},
	1002: {},
	1003: {},
	1004: {},
	1005: {},
	1006: {},
	1015: {},
	1016: {},
	2004: {},
}

type terminalInputModeState struct {
	observed              map[int]bool
	mouseProtocol         int
	mouseProtocolObserved bool
	mouseEncoding         int
	mouseEncodingObserved bool
	pending               []byte
	utf8Pending           [utf8.UTFMax]byte
	utf8PendingLen        uint8
	utf8ExpectedLen       uint8
}

func (s *terminalInputModeState) observe(data []byte) {
	for _, current := range data {
		if s.consumeUTF8Byte(current) {
			continue
		}
		s.observeControlByte(current)
	}
}

func (s *terminalInputModeState) consumeUTF8Byte(current byte) bool {
	if s.utf8PendingLen > 0 {
		if current&0xc0 == 0x80 {
			s.utf8Pending[s.utf8PendingLen] = current
			s.utf8PendingLen++
			if s.utf8PendingLen == s.utf8ExpectedLen {
				decoded, size := utf8.DecodeRune(s.pendingUTF8())
				s.clearUTF8()
				if decoded == '\u009b' {
					s.observeControlByte('\x9b')
				} else if !isXtermDiscardedUTF8(decoded, size) {
					s.pending = nil
				}
			}
			return true
		}
		s.clearUTF8()
	}

	switch {
	case current >= 0xc2 && current <= 0xdf:
		s.startUTF8(current, 2)
		return true
	case current >= 0xe0 && current <= 0xef:
		s.startUTF8(current, 3)
		return true
	case current >= 0xf0 && current <= 0xf4:
		s.startUTF8(current, 4)
		return true
	default:
		// xterm's binary UTF-8 decoder discards standalone continuation
		// bytes and invalid leading bytes before VT parsing.
		return current >= utf8.RuneSelf
	}
}

func isXtermDiscardedUTF8(decoded rune, size int) bool {
	return decoded == '\ufeff' || decoded == utf8.RuneError && size == 1
}

func (s *terminalInputModeState) startUTF8(current byte, expectedLen uint8) {
	// Keep a byte-oriented candidate until the rune is complete. If the lead
	// byte is followed by invalid UTF-8, xterm drops it and parses the next
	// byte as the candidate's continuation.
	s.utf8Pending[0] = current
	s.utf8PendingLen = 1
	s.utf8ExpectedLen = expectedLen
}

func (s *terminalInputModeState) pendingUTF8() []byte {
	return s.utf8Pending[:s.utf8PendingLen]
}

func (s *terminalInputModeState) clearUTF8() {
	s.utf8PendingLen = 0
	s.utf8ExpectedLen = 0
}

func (s *terminalInputModeState) observeControlByte(current byte) {
	if len(s.pending) == 0 {
		s.startCandidate(current)
		return
	}
	if current == '\x18' || current == '\x1a' {
		s.pending = nil
		return
	}
	if isTerminalExecutableC0(current) || current == '\x7f' {
		return
	}
	if current == '\x9b' {
		s.pending = append(s.pending[:0], '\x1b', '[')
		return
	}

	switch len(s.pending) {
	case 1:
		if current == 'c' {
			s.fullReset()
			s.pending = nil
			return
		}
		if current != '[' {
			s.resetCandidate(current)
			return
		}
	case 2:
		if current != '?' && current != '!' {
			s.resetCandidate(current)
			return
		}
	default:
		if s.pending[2] == '!' {
			if len(s.pending) == 3 && current == 'p' {
				s.softReset()
				s.pending = nil
				return
			}
			s.resetCandidate(current)
			return
		}
		// xterm.js 6 does not implement DEC private-mode save/restore
		// (CSI ? Pm s/r), so only set/reset finals change effective state.
		switch {
		case current == 'h' || current == 'l':
			s.recordCandidate(current == 'h')
			s.pending = nil
			return
		case current < '0' || current > '9':
			if current != ';' {
				s.resetCandidate(current)
				return
			}
		}
	}

	s.pending = append(s.pending, current)
	if len(s.pending) >= maxTerminalInputModeSequence {
		s.pending = nil
	}
}

func (s *terminalInputModeState) startCandidate(current byte) {
	switch current {
	case '\x1b':
		s.pending = append(s.pending, current)
	case '\x9b':
		s.pending = append(s.pending, '\x1b', '[')
	}
}

func (s *terminalInputModeState) resetCandidate(current byte) {
	s.pending = nil
	s.startCandidate(current)
}

func (s *terminalInputModeState) recordCandidate(enabled bool) {
	parameters := s.pending[3:]
	start := 0
	for end := 0; end <= len(parameters); end++ {
		if end < len(parameters) && parameters[end] != ';' {
			continue
		}
		if end > start {
			mode, err := strconv.Atoi(string(parameters[start:end]))
			if err == nil {
				if _, tracked := trackedTerminalInputModeSet[mode]; tracked {
					s.recordMode(mode, enabled)
				}
			}
		}
		start = end + 1
	}
}

func (s *terminalInputModeState) recordMode(mode int, enabled bool) {
	switch {
	case isTerminalMouseProtocol(mode):
		s.mouseProtocolObserved = true
		if enabled {
			s.mouseProtocol = mode
		} else {
			s.mouseProtocol = 0
		}
	case isSupportedTerminalMouseEncoding(mode):
		s.mouseEncodingObserved = true
		if enabled {
			s.mouseEncoding = mode
		} else {
			s.mouseEncoding = 0
		}
	default:
		if s.observed == nil {
			s.observed = make(map[int]bool)
		}
		s.observed[mode] = enabled
	}
}

func (s *terminalInputModeState) fullReset() {
	s.mouseProtocol = 0
	s.mouseProtocolObserved = true
	s.mouseEncoding = 0
	s.mouseEncodingObserved = true
	if s.observed == nil {
		s.observed = make(map[int]bool)
	}
	for _, mode := range trackedTerminalInputModes {
		if !isTerminalMouseProtocol(mode) &&
			!isSupportedTerminalMouseEncoding(mode) {
			s.observed[mode] = false
		}
	}
}

func (s *terminalInputModeState) softReset() {
	if s.observed == nil {
		s.observed = make(map[int]bool)
	}
	// xterm DECSTR resets CoreService modes, but does not reset
	// CoreMouseService's active protocol or encoding.
	s.observed[1] = false
	s.observed[1004] = false
	s.observed[2004] = false
}

func (s *terminalInputModeState) appendTransitions(
	dst []byte,
	baseline terminalInputModeState,
) []byte {
	resetModes := make(map[int]bool)
	setModes := make(map[int]bool)
	for _, mode := range trackedTerminalInputModes {
		if isTerminalMouseProtocol(mode) ||
			isSupportedTerminalMouseEncoding(mode) {
			continue
		}
		current, observed := s.observed[mode]
		baselineCurrent, baselineObserved := baseline.observed[mode]
		if !observed || (baselineObserved && baselineCurrent == current) {
			continue
		}
		if current {
			setModes[mode] = true
		} else {
			resetModes[mode] = true
		}
	}
	appendGroupedTransition(
		resetModes,
		setModes,
		s.mouseProtocolObserved,
		s.mouseProtocol,
		baseline.mouseProtocolObserved,
		baseline.mouseProtocol,
		terminalMouseProtocolResetMode,
	)
	appendGroupedTransition(
		resetModes,
		setModes,
		s.mouseEncodingObserved,
		s.mouseEncoding,
		baseline.mouseEncodingObserved,
		baseline.mouseEncoding,
		terminalMouseEncodingResetMode,
	)

	resets := make([]int, 0, len(resetModes))
	sets := make([]int, 0, len(setModes))
	for _, mode := range trackedTerminalInputModes {
		if resetModes[mode] {
			resets = append(resets, mode)
		}
		if setModes[mode] {
			sets = append(sets, mode)
		}
	}
	dst = appendTerminalInputModeSequence(dst, resets, 'l')
	return appendTerminalInputModeSequence(dst, sets, 'h')
}

func appendGroupedTransition(
	resetModes map[int]bool,
	setModes map[int]bool,
	currentObserved bool,
	current int,
	baselineObserved bool,
	baseline int,
	canonicalReset int,
) {
	if !currentObserved {
		return
	}
	if !baselineObserved {
		if current == 0 {
			resetModes[canonicalReset] = true
		} else {
			setModes[current] = true
		}
		return
	}
	if current == baseline {
		return
	}
	if baseline != 0 {
		resetModes[baseline] = true
	}
	if current != 0 {
		setModes[current] = true
	}
}

func isTerminalMouseProtocol(mode int) bool {
	return mode == 9 || mode == 1000 || mode == 1002 || mode == 1003
}

func isSupportedTerminalMouseEncoding(mode int) bool {
	// xterm.js 6 ignores 1005 and 1015. Keeping them outside this group
	// preserves an active SGR encoding when either ignored mode follows it.
	return mode == 1006 || mode == 1016
}

func appendTerminalInputModeSequence(dst []byte, modes []int, final byte) []byte {
	if len(modes) == 0 {
		return dst
	}
	dst = append(dst, "\x1b[?"...)
	for index, mode := range modes {
		if index > 0 {
			dst = append(dst, ';')
		}
		dst = strconv.AppendInt(dst, int64(mode), 10)
	}
	return append(dst, final)
}

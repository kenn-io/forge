package localruntime

import "unicode/utf8"

type terminalSequenceState uint8

const (
	terminalSequenceGround terminalSequenceState = iota
	terminalSequenceEscape
	terminalSequenceEscapeIntermediate
	terminalSequenceCSI
	terminalSequenceOSC
	terminalSequenceString
	terminalSequenceOSCSTPending
	terminalSequenceStringSTPending
)

// trailingIncompleteTerminalDataLen returns the number of trailing bytes that
// must remain adjacent to the next PTY chunk. Subscriber-only mode transitions
// must precede that suffix so they cannot split a VT control sequence or a
// multibyte UTF-8 code point.
func trailingIncompleteTerminalDataLen(data []byte) int {
	var sc terminalSequenceScanner
	return sc.scan(data)
}

type terminalSequenceScanner struct {
	state         terminalSequenceState
	start         int
	stEscapeStart int
	next          int
}

func (sc *terminalSequenceScanner) rebase(n int) {
	sc.start -= n
	sc.stEscapeStart -= n
	sc.next -= n
}

func (sc *terminalSequenceScanner) startEscape(index int) {
	sc.start = index
	sc.state = terminalSequenceEscape
}

func (sc *terminalSequenceScanner) advanceC1(current byte, index int) {
	if current == 0x9c {
		sc.start = -1
		sc.state = terminalSequenceGround
		return
	}
	sc.start = index
	switch current {
	case 0x90, 0x98, 0x9e, 0x9f:
		sc.state = terminalSequenceString
	case 0x9b:
		sc.state = terminalSequenceCSI
	case 0x9d:
		sc.state = terminalSequenceOSC
	default:
		sc.start = -1
		sc.state = terminalSequenceGround
	}
}

func (sc *terminalSequenceScanner) advanceEscape(current byte, index int) {
	switch current {
	case '[':
		sc.state = terminalSequenceCSI
	case ']':
		sc.state = terminalSequenceOSC
	case 'P', 'X', '^', '_':
		sc.state = terminalSequenceString
	case '\x1b':
		sc.startEscape(index)
	default:
		switch {
		case isTerminalExecutableC0(current), current == '\x7f':
		case current >= 0x20 && current <= 0x2f:
			sc.state = terminalSequenceEscapeIntermediate
		case current >= 0x30 && current <= 0x7e:
			sc.start = -1
			sc.state = terminalSequenceGround
		default:
			sc.start = -1
			sc.state = terminalSequenceGround
		}
	}
}

func (sc *terminalSequenceScanner) advanceNonASCII() {
	switch sc.state {
	case terminalSequenceEscape,
		terminalSequenceEscapeIntermediate,
		terminalSequenceCSI,
		terminalSequenceOSCSTPending,
		terminalSequenceStringSTPending:
		sc.start = -1
		sc.state = terminalSequenceGround
	case terminalSequenceGround, terminalSequenceOSC, terminalSequenceString:
	}
}

func (sc *terminalSequenceScanner) scan(data []byte) int {
	if sc.state == terminalSequenceGround {
		sc.start = -1
	}
	for index := sc.next; index < len(data); {
		current := data[index]
		if current >= utf8.RuneSelf {
			if !utf8.FullRune(data[index:]) {
				sc.next = index
				if sc.start < 0 {
					sc.start = index
				}
				return len(data) - sc.start
			}
			decoded, size := utf8.DecodeRune(data[index:])
			if isXtermDiscardedUTF8(decoded, size) {
				// Xterm's binary decoder drops invalid scalars and BOM before
				// VT parsing, so they neither start nor interrupt a control.
				index++
				if size > 1 {
					index += size - 1
				}
				continue
			}
			if decoded >= '\u0080' && decoded <= '\u009f' {
				sc.advanceC1(byte(decoded), index)
			} else {
				sc.advanceNonASCII()
			}
			index += size
			continue
		}

		switch current {
		case '\x18', '\x1a':
			sc.start = -1
			sc.state = terminalSequenceGround
			index++
			continue
		case '\x1b':
			switch sc.state {
			case terminalSequenceOSC, terminalSequenceOSCSTPending:
				sc.stEscapeStart = index
				sc.state = terminalSequenceOSCSTPending
			case terminalSequenceString, terminalSequenceStringSTPending:
				sc.stEscapeStart = index
				sc.state = terminalSequenceStringSTPending
			default:
				sc.startEscape(index)
			}
			index++
			continue
		}

		switch sc.state {
		case terminalSequenceGround:
		case terminalSequenceEscape:
			sc.advanceEscape(current, index)
		case terminalSequenceEscapeIntermediate:
			switch {
			case isTerminalExecutableC0(current), current == '\x7f':
			case current >= 0x20 && current <= 0x2f:
			case current >= 0x30 && current <= 0x7e:
				sc.start = -1
				sc.state = terminalSequenceGround
			default:
				sc.start = -1
				sc.state = terminalSequenceGround
			}
		case terminalSequenceCSI:
			switch {
			case isTerminalExecutableC0(current), current == '\x7f':
			case current >= 0x20 && current <= 0x3f:
			case current >= 0x40 && current <= 0x7e:
				sc.start = -1
				sc.state = terminalSequenceGround
			default:
				sc.start = -1
				sc.state = terminalSequenceGround
			}
		case terminalSequenceOSC:
			if current == '\x07' {
				sc.start = -1
				sc.state = terminalSequenceGround
			}
		case terminalSequenceString:
		case terminalSequenceOSCSTPending, terminalSequenceStringSTPending:
			if current == '\\' {
				sc.start = -1
				sc.state = terminalSequenceGround
				index++
				continue
			}
			sc.start = sc.stEscapeStart
			sc.state = terminalSequenceEscape
			sc.advanceEscape(current, index)
		}
		index++
	}

	sc.next = len(data)
	if sc.state == terminalSequenceGround || sc.start < 0 {
		return 0
	}
	return len(data) - sc.start
}

func isTerminalExecutableC0(current byte) bool {
	return current <= 0x17 ||
		current == 0x19 ||
		current >= 0x1c && current <= 0x1f
}

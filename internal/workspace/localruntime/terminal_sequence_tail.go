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
	state := terminalSequenceGround
	start := -1
	stEscapeStart := -1

	startEscape := func(index int) {
		start = index
		state = terminalSequenceEscape
	}
	advanceC1 := func(current byte, index int) {
		if current == 0x9c {
			start = -1
			state = terminalSequenceGround
			return
		}
		start = index
		switch current {
		case 0x90, 0x98, 0x9e, 0x9f:
			state = terminalSequenceString
		case 0x9b:
			state = terminalSequenceCSI
		case 0x9d:
			state = terminalSequenceOSC
		default:
			start = -1
			state = terminalSequenceGround
		}
	}
	advanceEscape := func(current byte, index int) {
		switch current {
		case '[':
			state = terminalSequenceCSI
		case ']':
			state = terminalSequenceOSC
		case 'P', 'X', '^', '_':
			state = terminalSequenceString
		case '\x1b':
			startEscape(index)
		default:
			switch {
			case isTerminalExecutableC0(current), current == '\x7f':
			case current >= 0x20 && current <= 0x2f:
				state = terminalSequenceEscapeIntermediate
			case current >= 0x30 && current <= 0x7e:
				start = -1
				state = terminalSequenceGround
			default:
				start = -1
				state = terminalSequenceGround
			}
		}
	}
	advanceNonASCII := func() {
		switch state {
		case terminalSequenceEscape,
			terminalSequenceEscapeIntermediate,
			terminalSequenceCSI,
			terminalSequenceOSCSTPending,
			terminalSequenceStringSTPending:
			start = -1
			state = terminalSequenceGround
		}
	}

	for index := 0; index < len(data); {
		current := data[index]
		if current >= utf8.RuneSelf {
			if !utf8.FullRune(data[index:]) {
				if start < 0 {
					start = index
				}
				return len(data) - start
			}
			decoded, size := utf8.DecodeRune(data[index:])
			if decoded == utf8.RuneError && size == 1 {
				// xterm discards invalid standalone bytes in binary writes;
				// they neither start nor interrupt a VT control sequence.
				index++
				continue
			}
			if decoded >= '\u0080' && decoded <= '\u009f' {
				advanceC1(byte(decoded), index)
			} else {
				advanceNonASCII()
			}
			index += size
			continue
		}

		switch current {
		case '\x18', '\x1a':
			start = -1
			state = terminalSequenceGround
			index++
			continue
		case '\x1b':
			switch state {
			case terminalSequenceOSC, terminalSequenceOSCSTPending:
				stEscapeStart = index
				state = terminalSequenceOSCSTPending
			case terminalSequenceString, terminalSequenceStringSTPending:
				stEscapeStart = index
				state = terminalSequenceStringSTPending
			default:
				startEscape(index)
			}
			index++
			continue
		}

		switch state {
		case terminalSequenceGround:
		case terminalSequenceEscape:
			advanceEscape(current, index)
		case terminalSequenceEscapeIntermediate:
			switch {
			case isTerminalExecutableC0(current), current == '\x7f':
			case current >= 0x20 && current <= 0x2f:
			case current >= 0x30 && current <= 0x7e:
				start = -1
				state = terminalSequenceGround
			default:
				start = -1
				state = terminalSequenceGround
			}
		case terminalSequenceCSI:
			switch {
			case isTerminalExecutableC0(current), current == '\x7f':
			case current >= 0x20 && current <= 0x3f:
			case current >= 0x40 && current <= 0x7e:
				start = -1
				state = terminalSequenceGround
			default:
				start = -1
				state = terminalSequenceGround
			}
		case terminalSequenceOSC:
			if current == '\x07' {
				start = -1
				state = terminalSequenceGround
			}
		case terminalSequenceString:
		case terminalSequenceOSCSTPending, terminalSequenceStringSTPending:
			if current == '\\' {
				start = -1
				state = terminalSequenceGround
				index++
				continue
			}
			start = stEscapeStart
			state = terminalSequenceEscape
			advanceEscape(current, index)
		}
		index++
	}

	if state == terminalSequenceGround || start < 0 {
		return 0
	}
	return len(data) - start
}

func isTerminalExecutableC0(current byte) bool {
	return current <= 0x17 ||
		current == 0x19 ||
		current >= 0x1c && current <= 0x1f
}

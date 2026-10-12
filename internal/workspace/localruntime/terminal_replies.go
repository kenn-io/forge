package localruntime

// TerminalRepliesOnly reports whether input contains only automatic terminal replies.
func TerminalRepliesOnly(input []byte) bool {
	if len(input) == 0 {
		return false
	}
	for len(input) > 0 {
		if len(input) < 3 || input[0] != '\x1b' {
			return false
		}
		switch input[1] {
		case '[':
			i := 2
			for i < len(input) && input[i] >= 0x20 && input[i] <= 0x3f {
				i++
			}
			if i == len(input) {
				return false
			}
			switch input[i] {
			case 'I', 'O':
				if i != 2 {
					return false
				}
			case 'R', 'c', 'n', 't':
				for _, b := range input[2:i] {
					if (b < '0' || b > '9') && b != ';' && b != '?' && b != '>' {
						return false
					}
				}
			case 'y':
				if i < 5 || input[2] != '?' || input[i-1] != '$' {
					return false
				}
				for _, b := range input[3 : i-1] {
					if (b < '0' || b > '9') && b != ';' {
						return false
					}
				}
			default:
				return false
			}
			input = input[i+1:]
		case ']', 'P':
			i := 2
			for ; i < len(input); i++ {
				if input[1] == ']' && input[i] == '\a' {
					break
				}
				if input[i] == '\x1b' && i+1 < len(input) && input[i+1] == '\\' {
					i++
					break
				}
			}
			if i == len(input) {
				return false
			}
			input = input[i+1:]
		default:
			return false
		}
	}
	return true
}

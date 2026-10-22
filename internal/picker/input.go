package picker

import (
	"bytes"
	"unicode/utf8"
)

// decoder retains split UTF-8 runes and escape sequences across terminal reads.
// A bare Escape becomes cancellation after a short input timeout.
type decoder struct {
	pending []byte
	paste   bool
}

func (d *decoder) next(expired bool) (input, bool) {
	if len(d.pending) == 0 {
		return input{}, false
	}
	if d.paste {
		end := bytes.Index(d.pending, []byte("\x1b[201~"))
		if end < 0 {
			return input{}, false
		}
		text := sanitize(string(d.pending[:end]))
		d.pending = d.pending[end+6:]
		d.paste = false
		return input{key: keyText, text: text}, true
	}
	b := d.pending[0]
	if b == '\x1b' {
		if len(d.pending) == 1 {
			if !expired {
				return input{}, false
			}
			d.pending = nil
			return input{key: keyCancel}, true
		}
		if d.pending[1] != '[' && d.pending[1] != 'O' {
			d.pending = d.pending[1:]
			return input{key: keyCancel}, true
		}
		end := 2
		for end < len(d.pending) && (d.pending[end] < 0x40 || d.pending[end] > 0x7e) {
			end++
		}
		if end == len(d.pending) {
			if expired || end > 64 {
				d.pending = nil
				return input{key: keyIgnore}, true
			}
			return input{}, false
		}
		sequence := string(d.pending[2 : end+1])
		d.pending = d.pending[end+1:]
		if sequence == "200~" {
			d.paste = true
			return input{key: keyIgnore}, true
		}
		k := keyIgnore
		switch sequence[len(sequence)-1] {
		case 'A':
			k = keyUp
		case 'B':
			k = keyDown
		case 'H':
			k = keyHome
		case 'F':
			k = keyEnd
		case '~':
			switch sequence {
			case "1~", "7~":
				k = keyHome
			case "4~", "8~":
				k = keyEnd
			case "5~":
				k = keyPageUp
			case "6~":
				k = keyPageDown
			}
		}
		return input{key: k}, true
	}
	if b < 0x20 || b == 0x7f {
		d.pending = d.pending[1:]
		k := keyIgnore
		switch b {
		case '\r', '\n':
			k = keyEnter
		case 3, 4:
			k = keyCancel
		case 8, 127:
			k = keyBackspace
		case 14:
			k = keyDown
		case 16:
			k = keyUp
		case 21:
			k = keyClear
		}
		return input{key: k}, true
	}
	if !utf8.FullRune(d.pending) {
		if expired {
			d.pending = nil
			return input{key: keyIgnore}, true
		}
		return input{}, false
	}
	r, size := utf8.DecodeRune(d.pending)
	d.pending = d.pending[size:]
	if r == utf8.RuneError && size == 1 {
		return input{key: keyIgnore}, true
	}
	return input{key: keyText, text: string(r)}, true
}

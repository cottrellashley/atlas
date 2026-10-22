package picker

import "testing"

func TestDecodeKeys(t *testing.T) {
	for _, tt := range []struct {
		sequence string
		key      key
	}{{"\x1b[A", keyUp}, {"\x1bOB", keyDown}, {"\x1b[1;5A", keyUp}, {"\x1b[5~", keyPageUp},
		{"\x1b[6~", keyPageDown}, {"\x1bOH", keyHome}, {"\x1b[4~", keyEnd}, {"\x7f", keyBackspace},
		{"\x15", keyClear}, {"\x03", keyCancel}, {"\x04", keyCancel}, {"\r", keyEnter}, {"\x1b[3~", keyIgnore}} {
		d := decoder{pending: []byte(tt.sequence)}
		got, ok := d.next(false)
		if !ok || got.key != tt.key || len(d.pending) != 0 {
			t.Errorf("decode(%q): %+v, ready=%t, remaining=%q", tt.sequence, got, ok, d.pending)
		}
	}
}

func TestDecodeFragmentedEscapeAndUTF8(t *testing.T) {
	d := decoder{pending: []byte("\x1b")}
	if _, ready := d.next(false); ready {
		t.Fatal("bare escape must wait for possible arrow-key suffix")
	}
	d.pending = append(d.pending, '[')
	if _, ready := d.next(false); ready {
		t.Fatal("incomplete CSI must wait")
	}
	d.pending = append(d.pending, 'B')
	if key, ready := d.next(false); !ready || key.key != keyDown {
		t.Fatalf("fragmented arrow: %+v, %t", key, ready)
	}
	d.pending = []byte("\x1b")
	if key, ready := d.next(true); !ready || key.key != keyCancel {
		t.Fatalf("escape timeout: %+v, %t", key, ready)
	}
	d.pending = []byte{0xc3}
	if _, ready := d.next(false); ready {
		t.Fatal("incomplete UTF-8 must wait")
	}
	d.pending = append(d.pending, 0xa9)
	if key, ready := d.next(false); !ready || key.text != "é" {
		t.Fatalf("fragmented UTF-8: %+v, %t", key, ready)
	}
}

func TestBracketedPasteCannotSubmitSelection(t *testing.T) {
	d := decoder{pending: []byte("\x1b[200~research\nnotes\x1b[201~\r")}
	if key, ready := d.next(false); !ready || key.key != keyIgnore {
		t.Fatal("paste must begin without selecting")
	}
	if key, ready := d.next(false); !ready || key.key != keyText || key.text != "research notes" {
		t.Fatalf("pasted newline must become search text: %+v", key)
	}
	if key, ready := d.next(false); !ready || key.key != keyEnter {
		t.Fatalf("explicit Enter after paste: %+v", key)
	}
}

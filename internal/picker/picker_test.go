package picker

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSearchAndGroupedSelection(t *testing.T) {
	items := []Item{
		{Title: "Review docs", Group: "Claude Code", Detail: "/work/atlas", Search: "claude-123"},
		{Title: "Fix setup", Group: "OpenCode", Detail: "/work/atlas", Search: "open-456"},
		{Title: "Research docs", Group: "Claude Code", Detail: "/work/research", Search: "claude-789"},
	}
	m := newModel("Continue a session", items)
	if !reflect.DeepEqual(m.matches, []int{0, 2, 1}) {
		t.Fatalf("group order: %v", m.matches)
	}
	m.update(input{key: keyDown})
	index, done, err := m.update(input{key: keyEnter})
	if err != nil || !done || index != 2 {
		t.Fatalf("selection must preserve original index: %d, %t, %v", index, done, err)
	}
	for _, query := range []string{"CLAUDE research", "789", "docs /work/research"} {
		m.query = query
		m.filter()
		if !reflect.DeepEqual(m.matches, []int{2}) {
			t.Errorf("search %q: %v", query, m.matches)
		}
	}
	m.query = "nothing matches"
	m.filter()
	if _, done, _ := m.update(input{key: keyEnter}); done {
		t.Fatal("Enter must not select a hidden item when there are no matches")
	}
	m.update(input{key: keyClear})
	if len(m.matches) != len(items) {
		t.Fatal("clear must restore all items")
	}
}

func TestTypedSearchAndUnicodeBackspace(t *testing.T) {
	m := newModel("Pick", []Item{{Title: "résumé plan"}, {Title: "resume"}})
	for _, r := range "résumé plan" {
		m.update(input{key: keyText, text: string(r)})
	}
	if m.query != "résumé plan" || !reflect.DeepEqual(m.matches, []int{0}) {
		t.Fatalf("typed search: %q %v", m.query, m.matches)
	}
	m.update(input{key: keyClear})
	m.update(input{key: keyText, text: "é"})
	m.update(input{key: keyBackspace})
	if m.query != "" {
		t.Fatalf("backspace split a rune: %q", m.query)
	}
}

func TestRenderingScrollsAndKeepsGroupVisible(t *testing.T) {
	items := make([]Item, 20)
	for i := range items {
		items[i] = Item{Title: "session", Group: "OpenCode"}
	}
	m := newModel("Sessions", items)
	m.update(input{key: keyEnd})
	rendered := m.render(60, 8)
	if m.scroll == 0 || !strings.Contains(rendered, "20/20 · OpenCode") {
		t.Fatalf("selected item did not scroll into view: %q", rendered)
	}
	if lines := strings.Count(rendered, "\r\n"); lines != 7 {
		t.Fatalf("render exceeded terminal height: %d newlines", lines)
	}
	m.update(input{key: keyHome})
	m.render(60, 8)
	if m.scroll > 1 {
		t.Fatalf("Home did not restore first visible item: scroll=%d", m.scroll)
	}
}

func TestSanitizeUntrustedMetadata(t *testing.T) {
	cases := map[string]string{
		"safe title":                    "safe title",
		"\x1b[31mred\x1b[0m":            "red",
		"a\x1b]52;c;Y2xpcGJvYXJk\ab":    "ab",
		"a\x1b]0;window title\x1b\\b":   "ab",
		"a\x1bPterminal command\x1b\\b": "ab",
		"a\u009b2Jb":                    "ab",
		"a\u009d0;window title\u009cb":  "ab",
		"a\x1b]unterminated":            "a",
		"name\r\n\tpath\x00\x7f":        "name path",
		"ab\u202Ecd\u2066ef":            "abcdef",
		"\x1b(Bok":                      "ok",
		"Résumé 研究":                     "Résumé 研究",
	}
	for source, want := range cases {
		if got := sanitize(source); got != want {
			t.Errorf("sanitize(%q) = %q; want %q", source, got, want)
		}
	}
	m := newModel("\x1b]0;evil\aTitle", []Item{{Title: "\x1b[2Jhello", Group: "\x1b]52;x\aGroup", Detail: "/safe\npath"}})
	rendered := m.render(80, 10)
	if strings.Contains(rendered, "evil") || strings.Contains(rendered, "]52") || strings.Contains(rendered, "\x1b[2Jhello") {
		t.Fatalf("untrusted escape string reached terminal: %q", rendered)
	}
}

func TestClipWideRunes(t *testing.T) {
	for _, tt := range []struct {
		text  string
		width int
		want  string
	}{{"abc", 2, "ab"}, {"研究 abc", 3, "研"}, {"e\u0301x", 1, "e\u0301"}, {"😀abc", 3, "😀a"}} {
		if got := clip(tt.text, tt.width); got != tt.want {
			t.Errorf("clip(%q, %d) = %q; want %q", tt.text, tt.width, got, tt.want)
		}
	}
}

func TestNonterminalAndEmptyInputs(t *testing.T) {
	var output bytes.Buffer
	if IsTerminal(strings.NewReader(""), &output) {
		t.Fatal("buffers are not terminals")
	}
	index, err := Choose(context.Background(), strings.NewReader(""), &output, "Pick", []Item{{Title: "one"}})
	if index != -1 || err == nil || !strings.Contains(err.Error(), "requires a terminal") {
		t.Fatalf("nonterminal selection: %d %v", index, err)
	}
	if _, err := Choose(context.Background(), strings.NewReader(""), &output, "Pick", nil); err == nil {
		t.Fatal("empty input should return an error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Choose(ctx, strings.NewReader(""), &output, "Pick", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: %v", err)
	}
}

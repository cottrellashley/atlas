// Package picker provides a small searchable terminal menu. It owns the terminal
// only during Choose; callers can immediately hand off to a native application.
package picker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
)

// ErrCanceled means the user dismissed the picker without choosing an item.
var ErrCanceled = errors.New("selection canceled")

// Item is one choice. Group names are shown above their items; Search adds hidden
// searchable text, such as a full session ID or project path.
type Item struct {
	Title  string
	Detail string
	Group  string
	Search string
}

type descriptor interface{ Fd() uintptr }

// IsTerminal reports whether both streams are terminal file descriptors.
func IsTerminal(in io.Reader, out io.Writer) bool {
	i, iok := in.(descriptor)
	o, ook := out.(descriptor)
	return iok && ook && term.IsTerminal(int(i.Fd())) && term.IsTerminal(int(o.Fd()))
}

// Choose displays items grouped in first-seen order and returns the index in the
// original items slice. Arrow keys navigate, typing filters, and Enter selects.
// It restores the terminal before returning, including on errors and cancellation.
// Nonterminal streams are rejected rather than read interactively.
func Choose(ctx context.Context, in io.Reader, out io.Writer, title string, items []Item) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	if len(items) == 0 {
		return -1, errors.New("no items to choose from")
	}
	if !IsTerminal(in, out) {
		return -1, errors.New("interactive selection requires a terminal; select an item explicitly")
	}
	return chooseTerminal(ctx, int(in.(descriptor).Fd()), int(out.(descriptor).Fd()), out, newModel(title, items))
}

type model struct {
	title    string
	items    []Item
	order    []int
	matches  []int
	query    string
	cursor   int
	scroll   int
	pageSize int
}

func newModel(title string, items []Item) *model {
	m := &model{title: sanitize(title), items: make([]Item, len(items)), pageSize: 10}
	groups := make(map[string][]int)
	var groupOrder []string
	for i, item := range items {
		item = Item{sanitize(item.Title), sanitize(item.Detail), sanitize(item.Group), sanitize(item.Search)}
		m.items[i] = item
		if _, exists := groups[item.Group]; !exists {
			groupOrder = append(groupOrder, item.Group)
		}
		groups[item.Group] = append(groups[item.Group], i)
	}
	for _, group := range groupOrder {
		m.order = append(m.order, groups[group]...)
	}
	m.filter()
	return m
}

func (m *model) filter() {
	terms := strings.Fields(strings.ToLower(m.query))
	m.matches = m.matches[:0]
	for _, index := range m.order {
		item := m.items[index]
		haystack := strings.ToLower(item.Title + " " + item.Detail + " " + item.Group + " " + item.Search)
		match := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				match = false
				break
			}
		}
		if match {
			m.matches = append(m.matches, index)
		}
	}
	m.cursor, m.scroll = 0, 0
}

type key int

const (
	keyText key = iota
	keyUp
	keyDown
	keyHome
	keyEnd
	keyPageUp
	keyPageDown
	keyBackspace
	keyClear
	keyEnter
	keyCancel
	keyIgnore
)

type input struct {
	key  key
	text string
}

func (m *model) update(in input) (selected int, done bool, err error) {
	switch in.key {
	case keyCancel:
		return -1, true, ErrCanceled
	case keyEnter:
		if len(m.matches) > 0 {
			return m.matches[m.cursor], true, nil
		}
	case keyUp:
		m.cursor--
	case keyDown:
		m.cursor++
	case keyHome:
		m.cursor = 0
	case keyEnd:
		m.cursor = len(m.matches) - 1
	case keyPageUp:
		m.cursor -= m.pageSize
	case keyPageDown:
		m.cursor += m.pageSize
	case keyBackspace:
		if len(m.query) > 0 {
			_, size := utf8.DecodeLastRuneInString(m.query)
			m.query = m.query[:len(m.query)-size]
			m.filter()
		}
	case keyClear:
		m.query = ""
		m.filter()
	case keyText:
		m.query += sanitize(in.text)
		// Preserve spaces while typing a multiword search.
		if in.text == " " {
			m.query += " "
		}
		m.filter()
	}
	if m.cursor >= len(m.matches) {
		m.cursor = len(m.matches) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	return -1, false, nil
}

type row struct {
	text     string
	group    bool
	selected bool
}

func (m *model) render(width, height int) string {
	width = max(width-1, 1) // Keep the final column free to avoid terminal wrapping.
	if height < 6 {
		return "\x1b[H\x1b[2J" + clip("Enlarge terminal to choose an item (Esc cancels)", width)
	}
	var rows []row
	lastGroup := ""
	selectedRow := 0
	for position, index := range m.matches {
		item := m.items[index]
		if item.Group != "" && (position == 0 || item.Group != lastGroup) {
			rows = append(rows, row{text: item.Group, group: true})
		}
		lastGroup = item.Group
		prefix := "  "
		if position == m.cursor {
			selectedRow = len(rows)
			prefix = "> "
		}
		label := prefix + item.Title
		if item.Detail != "" {
			label += "  " + item.Detail
		}
		rows = append(rows, row{text: label, selected: position == m.cursor})
	}
	bodyHeight := height - 4
	m.pageSize = max(bodyHeight-1, 1)
	if selectedRow < m.scroll {
		m.scroll = selectedRow
	}
	if selectedRow >= m.scroll+bodyHeight {
		m.scroll = selectedRow - bodyHeight + 1
	}
	m.scroll = min(m.scroll, max(len(rows)-bodyHeight, 0))
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	line := func(value, style string) {
		b.WriteString(style)
		b.WriteString(clip(value, width))
		b.WriteString("\x1b[0m\r\n")
	}
	line(m.title, "\x1b[1m")
	line("Type to search · ↑/↓ move · Enter open · Esc cancel", "\x1b[2m")
	line("Search: "+m.query, "")
	for i := 0; i < bodyHeight; i++ {
		position := m.scroll + i
		if position >= len(rows) {
			if len(rows) == 0 && i == 0 {
				line("No matches. Backspace or Ctrl-U clears your search.", "\x1b[2m")
			} else {
				line("", "")
			}
			continue
		}
		r := rows[position]
		style := ""
		if r.group {
			style = "\x1b[1;36m"
		} else if r.selected {
			style = "\x1b[7m"
		}
		line(r.text, style)
	}
	foot := fmt.Sprintf("%d of %d matches", len(m.matches), len(m.items))
	if len(m.matches) > 0 {
		foot += fmt.Sprintf(" · %d/%d", m.cursor+1, len(m.matches))
		// Keep the selected item's group visible even after its header scrolls away.
		if group := m.items[m.matches[m.cursor]].Group; group != "" {
			foot += " · " + group
		}
	}
	b.WriteString("\x1b[2m" + clip(foot, width) + "\x1b[0m")
	return b.String()
}

// Sanitize makes untrusted session metadata safe for terminal output, including
// plain-text listings. It removes escape strings and control/format characters.
func Sanitize(value string) string { return sanitize(value) }

// sanitize strips terminal escape strings (including OSC clipboard/title commands)
// and control/format characters from untrusted native session metadata.
func sanitize(value string) string {
	runes := []rune(value)
	var b strings.Builder
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\x1b' {
			i++
			if i == len(runes) {
				break
			}
			switch runes[i] {
			case '[':
				i = skipCSI(runes, i+1)
			case ']', 'P', 'X', '^', '_':
				i = skipString(runes, i+1)
			default:
				// Skip ANSI escape intermediates and their final byte.
				for i < len(runes) && runes[i] >= 0x20 && runes[i] <= 0x2f {
					i++
				}
			}
			continue
		}
		if r == '\u009b' {
			i = skipCSI(runes, i+1)
			continue
		}
		if r == '\u009d' || r == '\u0090' || r == '\u0098' || r == '\u009e' || r == '\u009f' {
			i = skipString(runes, i+1)
			continue
		}
		if unicode.IsSpace(r) {
			b.WriteByte(' ')
		} else if !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func skipCSI(value []rune, start int) int {
	for i := start; i < len(value); i++ {
		if value[i] >= 0x40 && value[i] <= 0x7e {
			return i
		}
	}
	return len(value)
}

func skipString(value []rune, start int) int {
	for i := start; i < len(value); i++ {
		if value[i] == '\a' || value[i] == '\u009c' {
			return i
		}
		if value[i] == '\x1b' && i+1 < len(value) && value[i+1] == '\\' {
			return i + 1
		}
	}
	return len(value)
}

func clip(value string, width int) string {
	used := 0
	for index, r := range value {
		cells := 1
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			cells = 0
		} else if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
			(r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe10 && r <= 0xfe6f) ||
			(r >= 0xff00 && r <= 0xff60) || (r >= 0xffe0 && r <= 0xffe6) ||
			(r >= 0x1f000 && r <= 0x1ffff) || r >= 0x20000) {
			cells = 2
		}
		if used+cells > width {
			return value[:index]
		}
		used += cells
	}
	return value
}

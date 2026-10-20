package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// This small JSONC syntax tree records byte offsets so changes leave unrelated
// comments and formatting intact. Object members must be unique. We deliberately
// do not address array indices: package ownership of arrays is by exact element.
type jsonNode struct {
	kind       byte
	start, end int
	value      any
	fields     []jsonField
	items      []*jsonNode
	commas     []int
}
type jsonField struct {
	key   string
	start int
	node  *jsonNode
}
type jsonParser struct {
	data []byte
	pos  int
}
type jsonDocument struct{ data []byte }

func (p *jsonParser) space() error {
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			p.pos++
			continue
		}
		if c == '/' && p.pos+1 < len(p.data) {
			switch p.data[p.pos+1] {
			case '/':
				p.pos += 2
				for p.pos < len(p.data) && p.data[p.pos] != '\n' {
					p.pos++
				}
				continue
			case '*':
				p.pos += 2
				i := bytes.Index(p.data[p.pos:], []byte("*/"))
				if i < 0 {
					return errors.New("unterminated JSONC comment")
				}
				p.pos += i + 2
				continue
			}
		}
		break
	}
	return nil
}
func (p *jsonParser) str() (string, error) {
	start := p.pos
	if start >= len(p.data) || p.data[start] != '"' {
		return "", fmt.Errorf("expected JSON string at byte %d", start)
	}
	p.pos++
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++
		if c == '\\' {
			if p.pos < len(p.data) {
				p.pos++
			}
			continue
		}
		if c == '"' {
			var s string
			if e := json.Unmarshal(p.data[start:p.pos], &s); e != nil {
				return "", e
			}
			return s, nil
		}
	}
	return "", errors.New("unterminated JSON string")
}
func (p *jsonParser) node(depth int) (*jsonNode, error) {
	if depth > 128 {
		return nil, errors.New("JSON nesting is too deep")
	}
	if e := p.space(); e != nil {
		return nil, e
	}
	if p.pos >= len(p.data) {
		return nil, errors.New("unexpected end of JSON")
	}
	n := &jsonNode{kind: p.data[p.pos], start: p.pos}
	switch n.kind {
	case '{':
		p.pos++
		m := map[string]any{}
		n.value = m
		if e := p.space(); e != nil {
			return nil, e
		}
		if p.pos < len(p.data) && p.data[p.pos] == '}' {
			p.pos++
			break
		}
		for {
			if e := p.space(); e != nil {
				return nil, e
			}
			start := p.pos
			key, e := p.str()
			if e != nil {
				return nil, e
			}
			if _, ok := m[key]; ok {
				return nil, fmt.Errorf("duplicate JSON key %q", key)
			}
			if e = p.space(); e != nil {
				return nil, e
			}
			if p.pos >= len(p.data) || p.data[p.pos] != ':' {
				return nil, errors.New("expected colon after JSON key")
			}
			p.pos++
			child, e := p.node(depth + 1)
			if e != nil {
				return nil, e
			}
			m[key] = child.value
			n.fields = append(n.fields, jsonField{key, start, child})
			if e = p.space(); e != nil {
				return nil, e
			}
			if p.pos >= len(p.data) {
				return nil, errors.New("unterminated JSON object")
			}
			if p.data[p.pos] == '}' {
				p.pos++
				break
			}
			if p.data[p.pos] != ',' {
				return nil, errors.New("expected comma in JSON object")
			}
			n.commas = append(n.commas, p.pos)
			p.pos++
			if e = p.space(); e != nil {
				return nil, e
			}
			if p.pos < len(p.data) && p.data[p.pos] == '}' {
				p.pos++
				break
			}
		}
	case '[':
		p.pos++
		values := []any{}
		if e := p.space(); e != nil {
			return nil, e
		}
		if p.pos < len(p.data) && p.data[p.pos] == ']' {
			p.pos++
			n.value = values
			break
		}
		for {
			child, e := p.node(depth + 1)
			if e != nil {
				return nil, e
			}
			values = append(values, child.value)
			n.items = append(n.items, child)
			if e = p.space(); e != nil {
				return nil, e
			}
			if p.pos >= len(p.data) {
				return nil, errors.New("unterminated JSON array")
			}
			if p.data[p.pos] == ']' {
				p.pos++
				break
			}
			if p.data[p.pos] != ',' {
				return nil, errors.New("expected comma in JSON array")
			}
			n.commas = append(n.commas, p.pos)
			p.pos++
			if e = p.space(); e != nil {
				return nil, e
			}
			if p.pos < len(p.data) && p.data[p.pos] == ']' {
				p.pos++
				break
			}
		}
		n.value = values
	case '"':
		v, e := p.str()
		if e != nil {
			return nil, e
		}
		n.value = v
	default:
		start := p.pos
		for p.pos < len(p.data) && !strings.ContainsRune(" \t\n\r,]}/", rune(p.data[p.pos])) {
			p.pos++
		}
		if p.pos == start {
			return nil, fmt.Errorf("invalid JSON at byte %d", start)
		}
		if !json.Valid(p.data[start:p.pos]) {
			return nil, fmt.Errorf("invalid JSON primitive at byte %d", start)
		}
		d := json.NewDecoder(bytes.NewReader(p.data[start:p.pos]))
		d.UseNumber()
		if e := d.Decode(&n.value); e != nil {
			return nil, e
		}
		var extra any
		if d.Decode(&extra) == nil {
			return nil, errors.New("invalid JSON primitive")
		}
		if _, ok := n.value.(map[string]any); ok {
			return nil, errors.New("invalid JSON primitive")
		}
	}
	n.end = p.pos
	return n, nil
}
func parseTree(data []byte) (*jsonNode, error) {
	p := jsonParser{data: data}
	n, e := p.node(0)
	if e != nil {
		return nil, e
	}
	if e = p.space(); e != nil {
		return nil, e
	}
	if p.pos != len(data) {
		return nil, fmt.Errorf("unexpected JSON content at byte %d", p.pos)
	}
	return n, nil
}
func parseDocument(data []byte) (*jsonDocument, error) {
	n, e := parseTree(data)
	if e != nil {
		return nil, e
	}
	if n.kind != '{' {
		return nil, errors.New("configuration must be a JSON object")
	}
	return &jsonDocument{append([]byte(nil), data...)}, nil
}
func pointerParts(pointer string) ([]string, error) {
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("configuration pointer must name an object member: %q", pointer)
	}
	parts := strings.Split(pointer[1:], "/")
	for i, p := range parts {
		for j := 0; j < len(p); j++ {
			if p[j] == '~' {
				if j+1 == len(p) || (p[j+1] != '0' && p[j+1] != '1') {
					return nil, fmt.Errorf("invalid JSON pointer %q", pointer)
				}
				j++
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}
	return parts, nil
}
func pointerFor(parts []string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~", "~0"), "/", "~1")
	}
	return "/" + strings.Join(out, "/")
}
func field(n *jsonNode, key string) (*jsonNode, int) {
	for i, f := range n.fields {
		if f.key == key {
			return f.node, i
		}
	}
	return nil, -1
}
func lookup(n *jsonNode, parts []string) (*jsonNode, bool, error) {
	for _, part := range parts {
		if n.kind != '{' {
			return nil, false, errors.New("configuration pointer traverses a non-object")
		}
		child, _ := field(n, part)
		if child == nil {
			return nil, false, nil
		}
		n = child
	}
	return n, true, nil
}
func (n *jsonNode) raw() json.RawMessage { b, _ := json.Marshal(n.value); return b }
func (d *jsonDocument) get(pointer string) (json.RawMessage, bool, error) {
	parts, e := pointerParts(pointer)
	if e != nil {
		return nil, false, e
	}
	tree, e := parseTree(d.data)
	if e != nil {
		return nil, false, e
	}
	n, ok, e := lookup(tree, parts)
	if e != nil || !ok {
		return nil, ok, e
	}
	return n.raw(), true, nil
}
func (d *jsonDocument) replace(start, end int, b []byte) {
	out := make([]byte, 0, len(d.data)-(end-start)+len(b))
	out = append(out, d.data[:start]...)
	out = append(out, b...)
	out = append(out, d.data[end:]...)
	d.data = out
}
func (d *jsonDocument) insert(n *jsonNode, key string, value []byte) {
	quoted, _ := json.Marshal(key)
	addition := append([]byte("\n  "), quoted...)
	addition = append(addition, ':', ' ')
	addition = append(addition, value...)
	addition = append(addition, '\n')
	closing := n.end - 1
	if len(n.fields) > 0 && len(n.commas) < len(n.fields) {
		at := n.fields[len(n.fields)-1].node.end
		d.replace(at, at, []byte(","))
		closing++
	}
	d.replace(closing, closing, addition)
}
func (d *jsonDocument) set(pointer string, value json.RawMessage) ([]string, error) {
	parts, e := pointerParts(pointer)
	if e != nil {
		return nil, e
	}
	if !json.Valid(value) {
		return nil, errors.New("invalid configuration value")
	}
	tree, e := parseTree(d.data)
	if e != nil {
		return nil, e
	}
	n := tree
	for i, key := range parts {
		if n.kind != '{' {
			return nil, fmt.Errorf("configuration pointer %s traverses a non-object", pointer)
		}
		child, _ := field(n, key)
		if i == len(parts)-1 {
			if child == nil {
				d.insert(n, key, value)
			} else if !jsonEqual(child.raw(), value) {
				d.replace(child.start, child.end, value)
			}
			return nil, nil
		}
		if child == nil {
			wrapped := []byte(value)
			for j := len(parts) - 1; j > i; j-- {
				quoted, _ := json.Marshal(parts[j])
				b := append([]byte{'{'}, quoted...)
				b = append(b, ':')
				b = append(b, wrapped...)
				wrapped = append(b, '}')
			}
			d.insert(n, key, wrapped)
			created := []string{}
			for j := i; j < len(parts)-1; j++ {
				created = append(created, pointerFor(parts[:j+1]))
			}
			return created, nil
		}
		n = child
	}
	return nil, nil
}
func (d *jsonDocument) remove(pointer string) error {
	parts, e := pointerParts(pointer)
	if e != nil {
		return e
	}
	tree, e := parseTree(d.data)
	if e != nil {
		return e
	}
	parent, ok, e := lookup(tree, parts[:len(parts)-1])
	if e != nil || !ok {
		return e
	}
	if parent.kind != '{' {
		return errors.New("configuration pointer parent is not an object")
	}
	n, index := field(parent, parts[len(parts)-1])
	if n == nil {
		return nil
	}
	start, end := parent.fields[index].start, n.end
	if index < len(parent.commas) {
		end = parent.commas[index] + 1
		d.replace(start, end, nil)
	} else {
		d.replace(start, end, nil)
		if index > 0 {
			at := parent.commas[index-1]
			d.replace(at, at+1, nil)
		}
	}
	return nil
}
func jsonEqual(a, b []byte) bool {
	an, ae := parseTree(a)
	bn, be := parseTree(b)
	return ae == nil && be == nil && bytes.Equal(an.raw(), bn.raw())
}
func arrayContains(a, b []byte) bool {
	n, e := parseTree(a)
	if e != nil || n.kind != '[' {
		return false
	}
	for _, v := range n.items {
		if jsonEqual(v.raw(), b) {
			return true
		}
	}
	return false
}
func (d *jsonDocument) array(pointer string, value json.RawMessage, add bool) error {
	parts, e := pointerParts(pointer)
	if e != nil {
		return e
	}
	tree, e := parseTree(d.data)
	if e != nil {
		return e
	}
	n, ok, e := lookup(tree, parts)
	if e != nil {
		return e
	}
	if !ok || n.kind != '[' {
		return fmt.Errorf("configuration %s must be an array", pointer)
	}
	for i, item := range n.items {
		if !jsonEqual(item.raw(), value) {
			continue
		}
		if add {
			return nil
		}
		start, end := item.start, item.end
		if i < len(n.commas) {
			end = n.commas[i] + 1
			d.replace(start, end, nil)
		} else {
			d.replace(start, end, nil)
			if i > 0 {
				at := n.commas[i-1]
				d.replace(at, at+1, nil)
			}
		}
		return nil
	}
	if !add {
		return nil
	}
	closing := n.end - 1
	if len(n.items) > 0 && len(n.commas) < len(n.items) {
		at := n.items[len(n.items)-1].end
		d.replace(at, at, []byte(","))
		closing++
	}
	addition := append([]byte("\n  "), value...)
	addition = append(addition, '\n')
	d.replace(closing, closing, addition)
	return nil
}
func (d *jsonDocument) empty() bool {
	n, e := parseTree(d.data)
	return e == nil && n.kind == '{' && len(n.fields) == 0
}

package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAML nodes retain comments, scalar spelling and quoting styles. Unchanged
// documents return their original bytes, including whitespace and comments.
type yamlDocument struct {
	original []byte
	root     *yaml.Node
	dirty    bool
}

func parseYAMLDocument(data []byte) (*yamlDocument, error) {
	// YAML version directives can change the meaning of plain scalar values in
	// other native parsers. Do not normalize a versioned document implicitly.
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) > 0 && line[0] == '%' {
			return nil, errors.New("YAML directives are not supported for managed configuration")
		}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	err := decoder.Decode(&root)
	if errors.Is(err, io.EOF) {
		// Empty/comment-only YAML is a useful starting point. Parse an explicit
		// empty mapping so its comments survive the first real edit.
		decoder = yaml.NewDecoder(bytes.NewReader(append(append([]byte{}, data...), []byte("\n{}\n")...)))
		err = decoder.Decode(&root)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid YAML configuration: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("invalid YAML configuration: %w", err)
		}
		return nil, errors.New("YAML configuration must contain one document")
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("configuration must be a YAML mapping")
	}
	if _, err := yamlJSONValue(root.Content[0], 0); err != nil {
		return nil, err
	}
	return &yamlDocument{original: append([]byte{}, data...), root: &root}, nil
}

// Only JSON-compatible YAML is safe for field ownership. Reject YAML features
// whose meaning depends on graph references, implicit key coercion or custom
// tags rather than silently changing them during reconciliation.
func yamlJSONValue(n *yaml.Node, depth int) (any, error) {
	if depth > 128 {
		return nil, errors.New("YAML nesting is too deep")
	}
	if n.Anchor != "" || n.Kind == yaml.AliasNode || n.Alias != nil {
		return nil, errors.New("YAML anchors and aliases are not supported for managed configuration")
	}
	switch n.Kind {
	case yaml.MappingNode:
		if n.Tag != "!!map" {
			return nil, fmt.Errorf("unsupported YAML mapping tag %q", n.Tag)
		}
		out := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" || key.Value == "<<" {
				return nil, errors.New("YAML configuration keys must be strings without merge keys or anchors")
			}
			if _, exists := out[key.Value]; exists {
				return nil, fmt.Errorf("duplicate YAML key %q", key.Value)
			}
			value, err := yamlJSONValue(n.Content[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			out[key.Value] = value
		}
		return out, nil
	case yaml.SequenceNode:
		if n.Tag != "!!seq" {
			return nil, fmt.Errorf("unsupported YAML sequence tag %q", n.Tag)
		}
		out := make([]any, len(n.Content))
		for i, child := range n.Content {
			var err error
			out[i], err = yamlJSONValue(child, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case yaml.ScalarNode:
		if n.Tag == "!!str" {
			return n.Value, nil
		}
		switch n.Tag {
		case "!!null", "!!bool", "!!int", "!!float":
		default:
			return nil, fmt.Errorf("unsupported YAML scalar tag %q", n.Tag)
		}
		var value any
		if err := n.Decode(&value); err != nil {
			return nil, fmt.Errorf("YAML scalar is not JSON compatible: %w", err)
		}
		if f, ok := value.(float64); ok && (math.IsInf(f, 0) || math.IsNaN(f)) {
			return nil, errors.New("YAML non-finite numbers are not JSON compatible")
		}
		// Preserve JSON numeric spelling for stable ownership comparisons, while
		// native YAML spellings (such as 0x10) use their exact decoded value.
		if (n.Tag == "!!int" || n.Tag == "!!float") && json.Valid([]byte(n.Value)) {
			var number json.Number
			if json.Unmarshal([]byte(n.Value), &number) == nil {
				return number, nil
			}
		}
		if _, err := json.Marshal(value); err != nil {
			return nil, fmt.Errorf("YAML scalar is not JSON compatible: %w", err)
		}
		return value, nil
	default:
		return nil, errors.New("unsupported YAML node")
	}
}

func yamlRaw(n *yaml.Node) (json.RawMessage, error) {
	value, err := yamlJSONValue(n, 0)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func yamlField(n *yaml.Node, key string) (*yaml.Node, int) {
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1], i
		}
	}
	return nil, -1
}

func yamlLookup(n *yaml.Node, parts []string) (*yaml.Node, bool, error) {
	for _, key := range parts {
		if n.Kind != yaml.MappingNode {
			return nil, false, errors.New("configuration pointer traverses a non-object")
		}
		child, _ := yamlField(n, key)
		if child == nil {
			return nil, false, nil
		}
		n = child
	}
	return n, true, nil
}

func (d *yamlDocument) get(pointer string) (json.RawMessage, bool, error) {
	parts, err := pointerParts(pointer)
	if err != nil {
		return nil, false, err
	}
	n, ok, err := yamlLookup(d.root.Content[0], parts)
	if err != nil || !ok {
		return nil, ok, err
	}
	value, err := yamlRaw(n)
	return value, true, err
}

func yamlValue(value json.RawMessage) (*yaml.Node, error) {
	if !json.Valid(value) {
		return nil, errors.New("invalid configuration value")
	}
	// JSON is valid YAML, and parsing it as nodes keeps numeric precision and
	// string types without a lossy float64/map conversion.
	var document yaml.Node
	if err := yaml.Unmarshal(value, &document); err != nil {
		return nil, err
	}
	if len(document.Content) != 1 {
		return nil, errors.New("invalid configuration value")
	}
	n := document.Content[0]
	if _, err := yamlJSONValue(n, 0); err != nil {
		return nil, err
	}
	var blockStyle func(*yaml.Node)
	blockStyle = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
			n.Style = 0
		}
		for _, child := range n.Content {
			blockStyle(child)
		}
	}
	blockStyle(n)
	return n, nil
}

func (d *yamlDocument) set(pointer string, value json.RawMessage) ([]string, error) {
	parts, err := pointerParts(pointer)
	if err != nil {
		return nil, err
	}
	if len(parts) > 128 {
		return nil, errors.New("YAML nesting is too deep")
	}
	added, err := yamlValue(value)
	if err != nil {
		return nil, err
	}
	n := d.root.Content[0]
	created := []string{}
	for i, key := range parts {
		if n.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("configuration pointer %s traverses a non-object", pointer)
		}
		child, index := yamlField(n, key)
		if i == len(parts)-1 {
			if child != nil {
				current, err := yamlRaw(child)
				if err != nil {
					return nil, err
				}
				if jsonEqual(current, value) {
					return created, nil
				}
				added.HeadComment, added.LineComment, added.FootComment = child.HeadComment, child.LineComment, child.FootComment
				if added.Kind == child.Kind && added.Tag == child.Tag {
					added.Style = child.Style
				}
				n.Content[index+1] = added
			} else {
				n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, added)
			}
			d.dirty = true
			return created, nil
		}
		if child == nil {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
			created = append(created, pointerFor(parts[:i+1]))
			d.dirty = true
		}
		n = child
	}
	return created, nil
}

func retainYAMLComments(parent *yaml.Node, next int, removed ...*yaml.Node) {
	var comments []string
	for _, n := range removed {
		for _, comment := range []string{n.HeadComment, n.LineComment, n.FootComment} {
			if comment != "" {
				comments = append(comments, comment)
			}
		}
	}
	if len(comments) == 0 {
		return
	}
	if next < len(parent.Content) {
		comments = append(comments, parent.Content[next].HeadComment)
		parent.Content[next].HeadComment = strings.TrimSpace(strings.Join(comments, "\n"))
	} else {
		comments = append(comments, parent.FootComment)
		parent.FootComment = strings.TrimSpace(strings.Join(comments, "\n"))
	}
}

func (d *yamlDocument) remove(pointer string) error {
	parts, err := pointerParts(pointer)
	if err != nil {
		return err
	}
	parent, ok, err := yamlLookup(d.root.Content[0], parts[:len(parts)-1])
	if err != nil || !ok {
		return err
	}
	if parent.Kind != yaml.MappingNode {
		return errors.New("configuration pointer parent is not an object")
	}
	child, index := yamlField(parent, parts[len(parts)-1])
	if child == nil {
		return nil
	}
	key := parent.Content[index]
	parent.Content = append(parent.Content[:index], parent.Content[index+2:]...)
	retainYAMLComments(parent, index, key, child)
	d.dirty = true
	return nil
}

func (d *yamlDocument) array(pointer string, value json.RawMessage, add bool) error {
	parts, err := pointerParts(pointer)
	if err != nil {
		return err
	}
	n, ok, err := yamlLookup(d.root.Content[0], parts)
	if err != nil {
		return err
	}
	if !ok || n.Kind != yaml.SequenceNode {
		return fmt.Errorf("configuration %s must be an array", pointer)
	}
	added, err := yamlValue(value)
	if err != nil {
		return err
	}
	for i, child := range n.Content {
		raw, err := yamlRaw(child)
		if err != nil {
			return err
		}
		if !jsonEqual(raw, value) {
			continue
		}
		if add {
			return nil
		}
		n.Content = append(n.Content[:i], n.Content[i+1:]...)
		retainYAMLComments(n, i, child)
		d.dirty = true
		return nil
	}
	if add {
		n.Content = append(n.Content, added)
		d.dirty = true
	}
	return nil
}

func (d *yamlDocument) empty() bool {
	if len(d.root.Content[0].Content) != 0 {
		return false
	}
	// Keep comments added by a user even when the final managed field is gone.
	for _, n := range []*yaml.Node{d.root, d.root.Content[0]} {
		if n.HeadComment != "" || n.LineComment != "" || n.FootComment != "" {
			return false
		}
	}
	return true
}

func (d *yamlDocument) bytes() ([]byte, error) {
	if !d.dirty {
		return append([]byte{}, d.original...), nil
	}
	if _, err := yamlJSONValue(d.root.Content[0], 0); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(d.root); err != nil {
		return nil, fmt.Errorf("encode YAML configuration: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

package install

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// configurationDocument exposes ownership operations without flattening the
// native syntax tree or replacing unrelated configuration.
type configurationDocument interface {
	get(string) (json.RawMessage, bool, error)
	set(string, json.RawMessage) ([]string, error)
	remove(string) error
	array(string, json.RawMessage, bool) error
	empty() bool
	bytes() ([]byte, error)
}

func parseConfiguration(path string, data []byte) (configurationDocument, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return parseYAMLDocument(data)
	default:
		return parseDocument(data)
	}
}

func (d *jsonDocument) bytes() ([]byte, error) { return d.data, nil }

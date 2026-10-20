// Package artifact describes and acquires immutable Atlas package releases.
package artifact

import (
	"io/fs"
	"os"
)

// Manifest adds distribution metadata around native harness resources.
// Paths are relative to the package root. Dependencies use exact package
// versions or immutable Git references when a release is published.
type Manifest struct {
	SchemaVersion int                          `json:"schemaVersion"`
	ID            string                       `json:"id"`
	Version       string                       `json:"version"`
	Kind          string                       `json:"kind"`
	Description   string                       `json:"description,omitempty"`
	Harnesses     []string                     `json:"harnesses,omitempty"`
	Dependencies  []Dependency                 `json:"dependencies,omitempty"`
	Skills        []string                     `json:"skills,omitempty"`
	MCP           string                       `json:"mcp,omitempty"`
	Config        map[string]string            `json:"config,omitempty"`
	Files         map[string]map[string]string `json:"files,omitempty"`
	Native        *Native                      `json:"native,omitempty"`
	Template      string                       `json:"template,omitempty"`
}

type Dependency struct {
	Source    string   `json:"source"`
	Harnesses []string `json:"harnesses,omitempty"`
}

// Native identifies a provider-native package. Path selects a directory in
// this artifact; Source selects a native Git/npm source. Exactly one is used.
type Native struct {
	Path        string         `json:"path,omitempty"`
	Source      string         `json:"source,omitempty"`
	Name        string         `json:"name,omitempty"`
	Marketplace string         `json:"marketplace,omitempty"`
	Target      string         `json:"target,omitempty"`
	Options     map[string]any `json:"options,omitempty"`
}

type Package struct {
	Manifest    Manifest `json:"manifest"`
	Source      string   `json:"source"`
	Digest      string   `json:"digest"`
	Root        string   `json:"-"`
	cleanupRoot string
}

// Close releases this acquisition's private extraction tree. The immutable
// archive remains cached for subsequent locked/offline use. Manually constructed
// Packages never delete their Root, and changing Root cannot redirect cleanup.
func (p Package) Close() error {
	if p.cleanupRoot == "" {
		return nil
	}
	return os.RemoveAll(p.cleanupRoot)
}

type File struct {
	Path string      `json:"path"`
	Data []byte      `json:"data"`
	Mode fs.FileMode `json:"mode"`
}
